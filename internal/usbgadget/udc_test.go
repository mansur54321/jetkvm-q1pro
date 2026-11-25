package usbgadget

import (
	"os"
	"path/filepath"
	"testing"
)

// Register dumps taken on an RV1106 (kernel 5.10) with the host attached.
const (
	regdumpConnected    = "DCFG = 0x00080804\nDCTL = 0x80f00a00\nDSTS = 0x00826600\n"
	regdumpDisconnected = "DCFG = 0x00080804\nDCTL = 0x00f00a00\nDSTS = 0x00d35cd9\n"
)

func TestDwc3RunStop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		regdump string
		want    bool
		wantErr bool
	}{
		{"connected", regdumpConnected, true, false},
		{"soft disconnected", regdumpDisconnected, false, false},
		{"no DCTL", "DCFG = 0x00080804\n", false, true},
		{"garbage", "DCTL = zz\n", false, true},
	} {
		got, err := dwc3RunStop([]byte(tc.regdump))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRegisteredUDCMatchesDeviceTreeWithRenamedController(t *testing.T) {
	root := t.TempDir()
	class := filepath.Join(root, "udc")
	node := filepath.Join(root, "firmware", "usbdrd", "usb@ffb00000")
	if err := os.MkdirAll(node, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first-unrelated", "renamed-controller"} {
		if err := os.MkdirAll(filepath.Join(class, name, "device"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := registeredUDCForDevice(class, node); got != "" || err != nil {
		t.Fatalf("unregistered node matched %q, %v", got, err)
	}
	if err := os.Symlink(node, filepath.Join(class, "renamed-controller", "device", "of_node")); err != nil {
		t.Fatal(err)
	}
	if got, err := registeredUDCForDevice(class, node); got != "renamed-controller" || err != nil {
		t.Fatalf("wrong controller for device tree node: %q, %v", got, err)
	}
}
