package kvm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func useQ1Pro(t *testing.T) {
	t.Helper()
	previousSKU := GetDeviceSKU()
	oldConfig, oldPath := config, configPath
	deviceSKU = q1ProSKU
	config = nil
	configPath = filepath.Join(t.TempDir(), "kvm_config.json")
	t.Cleanup(func() {
		deviceSKU, config, configPath = previousSKU, oldConfig, oldPath
	})
	LoadConfig()
}

func TestQ1ProAudioAndNetwork(t *testing.T) {
	useQ1Pro(t)
	if config.NetworkConfig.DHCPClient.String != "jetdhcpc" ||
		!slices.Equal(config.NetworkConfig.TimeSyncOrdering, []string{"ntp_dhcp"}) {
		t.Fatal("Q1 needs its own DHCP client and LAN time source")
	}
	if config.UsbDevices.Audio || alsaCaptureDevice() != "hw:CARD=rockchipdpin,DEV=0" {
		t.Fatal("Q1 must capture DisplayPort audio independently of USB")
	}
	if err := rpcSetAudioConfig(AudioConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	config = nil
	LoadConfig()
	if !effectiveAudioEnabled() || config.UsbDevices.Audio {
		t.Fatal("DisplayPort capture did not survive config reload")
	}
	caps, _ := rpcGetDeviceCapabilities()
	if !slices.Contains(caps, "displayport_audio") {
		t.Fatal("the audio settings must advertise DisplayPort capture")
	}
	if config.AutoUpdateEnabled || rpcTryUpdate() == nil {
		t.Fatal("Q1 must not apply JetKVM v2 updates")
	}
	if enabled, err := rpcSetAutoUpdateState(true); enabled || err == nil {
		t.Fatal("incompatible automatic updates were enabled")
	}
}

func TestQ1ProPreservesBridgeEDID(t *testing.T) {
	useQ1Pro(t)
	if err := reapplyHostDisplayAdvertisement("test_start"); err != nil {
		t.Fatal(err)
	}
	if _, err := rpcGetEDID(); err != errFixedEDID {
		t.Fatal("reported a configurable EDID without driver support")
	}
	if err := rpcSetEDID(""); err != errFixedEDID || rpcSetHostDisplayIdleMode(true) != errFixedEDID {
		t.Fatal("accepted a display configuration that cannot be applied")
	}
	caps, _ := rpcGetDeviceCapabilities()
	presets, _ := rpcGetEDIDPresets()
	if !slices.Contains(caps, "fixed_edid") || slices.Contains(caps, "custom_edid") || len(presets) != 0 {
		t.Fatal("video settings offered unsupported EDID changes")
	}
}

func TestQ1ProLocalSetupCanRetryAndPersist(t *testing.T) {
	useQ1Pro(t)
	router := setupRouter()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	expectStatus(t, request("POST", "/device/setup", `{"localAuthMode":"password","password":"short"}`),
		http.StatusBadRequest, "invalid password")
	status := request("GET", "/device/status", "")
	var first struct{ IsSetup bool }
	if err := json.Unmarshal(status.Body.Bytes(), &first); err != nil || first.IsSetup {
		t.Fatalf("an invalid first password changed setup status: %s", status.Body.String())
	}
	firstPath := configPath
	configPath = filepath.Join(filepath.Dir(firstPath), "missing", "kvm.json")
	validSetup := `{"localAuthMode":"password","password":"local-test-password"}`
	failed := request("POST", "/device/setup", validSetup)
	expectStatus(t, failed, http.StatusInternalServerError, "no writable configuration directory")
	if config.LocalAuthMode != "" || len(failed.Result().Cookies()) != 0 {
		t.Fatal("failed setup changed the live credentials")
	}
	configPath = firstPath
	expectStatus(t, request("POST", "/device/setup", validSetup), http.StatusOK, "local password setup")
	config = nil
	LoadConfig()
	if config.LocalAuthMode != "password" || config.HashedPassword == "" || config.CloudToken != "" {
		t.Fatal("local-only setup did not survive reload")
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("new credentials must be stored in a private file")
	}
	expectStatus(t, loginRequest(router, "local-test-password"), http.StatusOK, "local login after reload")
	expectStatus(t, request("POST", "/device/setup", validSetup), http.StatusBadRequest, "second setup")
}
