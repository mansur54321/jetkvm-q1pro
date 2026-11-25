package kvm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestQ1ControlLEDMarker(t *testing.T) {
	marker := controlLEDMarker{path: filepath.Join(t.TempDir(), "control")}
	if err := marker.set(true); err != nil {
		t.Fatal(err)
	}
	defer marker.set(false)
	probe, err := os.Open(marker.path)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := unix.Flock(int(probe.Fd()), unix.LOCK_SH|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("published indicator was not exclusively held: %v", err)
	}
	if err := marker.set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent marker on disconnect: %v", err)
	}
	if err := marker.set(true); err != nil {
		t.Fatal(err)
	}
	if err := marker.file.Close(); err != nil {
		t.Fatal(err)
	}
	marker.file = nil // A process exit releases its lock and leaves the path.
	stale, err := os.Open(marker.path)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	if err := unix.Flock(int(stale.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		t.Fatalf("stale marker stayed active: %v", err)
	}
	if err := marker.set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale marker survived restart: %v", err)
	}
}
