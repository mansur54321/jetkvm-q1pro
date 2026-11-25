package q1wifi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSIDAndKey(t *testing.T) {
	value, ok := decodeSSID(`\xd0\x94\\\"\t`)
	if !ok || value != "Д\\\"\t" {
		t.Fatalf("SSID decode: %q", value)
	}
	if _, ok := decodeSSID(`bad\xfz`); ok {
		t.Fatal("accepted a malformed escape")
	}
	key, err := profileKey("IEEE", "password")
	if err != nil || key != "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e" {
		t.Fatal("WPA passphrase does not match the standard PBKDF2 vector")
	}
	for _, pass := range []string{"tiny", "control\nchar", strings.Repeat("g", 64)} {
		if _, err := profileKey("net", pass); err == nil {
			t.Fatal("invalid passphrase accepted")
		}
	}
}

func TestScanAuthentication(t *testing.T) {
	networks := scanEntries("aa\t2412\t-55\t[WPA2-OWE-CCMP][ESS]\tGuest\n" +
		"bb\t2412\t-35\t[WPA2-PSK-CCMP][ESS]\tHome\n" +
		"cc\t2412\t-50\t[ESS]\tOpen")
	if networks["Guest"].Secure || !networks["Guest"].enhancedOpen || !networks["Home"].Secure || networks["Open"].Secure {
		t.Fatal("scan must request a password only for networks that require one")
	}
}

// The two virtual radios live in a private VM without a host network adapter.
func TestStationWithVirtualRadio(t *testing.T) {
	if os.Getenv("Q1_WIFI_HWSIM") != "1" {
		t.Skip("requires the isolated hwsim guest")
	}
	dir := os.Getenv("Q1_WIFI_TEST_DIR")
	s := New(filepath.Join(dir, "ctrl", "wlan0"), filepath.Join(dir, "wifi.conf"))
	await := func(label string, deadline time.Duration, f func() bool) {
		t.Helper()
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			if f() {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		state, err := s.Settings()
		t.Fatalf("%s: %+v %v", label, state, err)
	}
	const name = "Q1-radio-test"
	security := os.Getenv("Q1_WIFI_SECURITY")
	password := "wifi-simulation-key"
	if security == "OWE" {
		password = ""
	}
	result, err := s.Scan(true)
	if err != nil {
		t.Fatal(err)
	}
	await("discover access point", 12*time.Second, func() bool {
		if !result.Scanning {
			for _, n := range result.Networks {
				if n.SSID == name && n.Secure == (password != "") {
					return true
				}
			}
		}
		result, err = s.Scan(false)
		if err != nil {
			t.Fatal(err)
		}
		return false
	})
	if _, err := s.Save(name, password, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect(name); err != nil {
		t.Fatal(err)
	}
	associated := func() bool {
		state, err := s.Settings()
		return err == nil && !state.Applying && state.Connected && state.Channel == 1 && len(state.Networks) == 1 && state.Networks[0].Connected
	}
	await("associate on channel 1", 35*time.Second, associated)
	status, err := s.cmd("STATUS")
	if err != nil {
		t.Fatal(err)
	}
	method := fields(status)["key_mgmt"]
	if (security == "SAE" || security == "OWE") && method != security {
		t.Fatalf("expected %s, negotiated %s", security, method)
	}
	t.Logf("scan and association verified using %s", method)
	if security == "SAE" || security == "OWE" {
		return
	}
	// An unavailable band must leave the working profile usable after restart.
	if err := s.SetBand("5ghz"); err != nil {
		t.Fatal(err)
	}
	await("restore working band", 45*time.Second, func() bool {
		state, err := s.Settings()
		return err == nil && state.ApplyFailed && state.Band == "auto" && associated()
	})
	t.Log("5 GHz change rolled back on the 2.4 GHz-only access point")
	if ok, err := s.Save(name, "mistyped-network-key", true); !ok || err != nil {
		t.Fatalf("retry: %t %v", ok, err)
	}
	await("restore original credentials", 48*time.Second, func() bool {
		state, err := s.Settings()
		return err == nil && state.ApplyFailed && associated()
	})
	t.Log("previous Wi-Fi password restored after failed change")
	if err := s.Remove(name); err != nil {
		t.Fatal(err)
	}
}

// Run with tools/tests/test-q1-wifi.sh to provide an isolated wpa_supplicant.
func TestStationWithSupplicant(t *testing.T) {
	dir := os.Getenv("Q1_WIFI_TEST_DIR")
	if dir == "" {
		t.Skip("requires the isolated test supplicant")
	}
	s := New(filepath.Join(dir, "ctrl", "wlan0"), filepath.Join(dir, "wifi.conf"))
	if info, err := s.Settings(); err != nil || !info.Available || info.Connected || len(info.Networks) != 0 {
		t.Fatalf("unexpected initial radio state: %+v %v", info, err)
	}
	const ssid = "Q1 лаборатория\\\""
	if _, err := s.Save(ssid, "first-test-key", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ssid, "second-test-key", false); err != nil {
		t.Fatal(err)
	}
	if err := s.ok("RECONFIGURE"); err != nil {
		t.Fatal(err)
	}
	if info, err := s.Settings(); err != nil || len(info.Networks) != 1 || info.Networks[0].SSID != ssid || !info.Networks[0].Secure {
		t.Fatalf("updated profile did not survive reload: %+v %v", info, err)
	}
	if _, err := s.Save("open-lab", "", false); err != nil {
		t.Fatal(err)
	}
	for _, band := range []string{"5ghz", "2.4ghz", "auto"} {
		if err := s.SetBand(band); err != nil {
			t.Fatal(err)
		}
		if info, err := s.Settings(); err != nil || info.Band != band {
			t.Fatalf("band: %+v %v", info, err)
		}
	}
	if err := s.Remove(ssid); err != nil {
		t.Fatal(err)
	}
	if info, err := s.Settings(); err != nil || len(info.Networks) != 1 || info.Networks[0].SSID != "open-lab" || info.Networks[0].Secure {
		t.Fatalf("unexpected saved networks: %+v %v", info, err)
	}
	if err := s.Remove("open-lab"); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect("missing"); err == nil {
		t.Fatal("connected to an unknown profile")
	}
	if err := s.SetBand("60ghz"); err == nil {
		t.Fatal("accepted unsupported frequency band")
	}
}
