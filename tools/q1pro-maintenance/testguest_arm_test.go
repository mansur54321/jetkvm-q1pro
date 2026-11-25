//go:build linux && arm && q1maintenanceguest

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func fixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureHash(t *testing.T, path string, size int64) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != size {
		t.Fatalf("read %s: %d bytes, expected %d: %v", path, n, size, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestVirtualUBIMaintenance(t *testing.T) {
	// Direct tests on the workstation or a physical board must never touch a volume.
	if !bytes.Contains(fixtureBytes(t, "/proc/cmdline"), []byte("q1maintenance_test=1")) {
		t.Skip("offline QEMU fixture only")
	}
	if !bytes.HasPrefix(fixtureBytes(t, "/proc/device-tree/compatible"), []byte("linux,dummy-virt\x00")) {
		t.Fatal("expected QEMU virt device tree")
	}
	if string(fixtureBytes(t, "/sys/class/mtd/mtd0/name")) != "block2mtd: /dev/vda\n" {
		t.Fatal("expected virtual disk-backed MTD")
	}
	if err := checkRAMRoot(); err != nil {
		t.Fatal(err)
	}
	t.Log("Q1_MAINT genuine read-only loop0 root and RAM rcS confirmed")

	const software = "/dev/ubi0_0"
	const settings = "/dev/ubi0_1"
	const capacity = 206 * 126976
	const userCapacity = 434 * 126976
	virtual := storageFacts{mtdNumber: 0, mtdName: "block2mtd: /dev/vda", mtdType: "ram", mtdSize: 83 << 20, eraseSize: 131072,
		writeSize: 1, rootLEBs: 206, dataLEBs: 434, lebSize: 126976}
	target, err := discoverStorage(virtual)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	initial := string(fixtureBytes(t, "initial.sha256"))
	userDigest := string(fixtureBytes(t, "userdata.sha256"))
	if got := fixtureHash(t, settings, userCapacity); got != userDigest {
		t.Fatalf("userdata differs from host reference: %s, expected %s", got, userDigest)
	}
	same := func(expected string) {
		t.Helper()
		if got := fixtureHash(t, software, capacity); got != expected {
			t.Fatalf("software volume SHA-256 %s, want %s", got, expected)
		}
	}
	if strings.Contains(string(fixtureBytes(t, "/proc/cmdline")), "q1maintenance_phase=recover") {
		if string(fixtureBytes(t, softwareVolume+"/upd_marker")) != "1\n" {
			t.Fatal("expected interrupted update after cold boot")
		}
		partial := filepath.Join(tmp, "interrupted-after-boot.blob")
		if _, err := target.backup(partial); err == nil {
			t.Fatal("accepted backup after cold boot of interrupted volume")
		}
		if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("interrupted backup published an output", err)
		}
		t.Log("Q1_MAINT cold boot observed the persistent update marker; incomplete backup rejected")
		mount := filepath.Join(tmp, "sentinel-mount")
		if err := os.Mkdir(mount, 0700); err != nil {
			t.Fatal(err)
		}
		factory, err := verifyBundle("factory.tar", "owner.pub")
		if err != nil {
			t.Fatal(err)
		}
		if err := target.install(factory); err != nil {
			t.Fatal(err)
		}
		same(string(fixtureBytes(t, "factory.sha256")))
		if string(fixtureBytes(t, softwareVolume+"/upd_marker")) != "0\n" {
			t.Fatal("UBI update marker not cleared")
		}
		t.Logf("Q1_MAINT signed factory software restored after interruption; payload SHA-256 %x", factory.digest)

		if fixtureHash(t, settings, userCapacity) != userDigest {
			t.Fatal("unrelated userdata volume changed")
		}
		if err := unix.Mount("ubi0:rootfs_data", mount, "ubifs", unix.MS_RDONLY, ""); err != nil {
			t.Fatal(err)
		}
		if got := string(fixtureBytes(t, filepath.Join(mount, "vendor-marker"))); got != "preserve this file\n" {
			t.Fatalf("unexpected UBIFS sentinel %q", got)
		}
		if err := unix.Unmount(mount, 0); err != nil {
			t.Fatal(err)
		}
		t.Logf("Q1_MAINT entire %d-byte userdata volume preserved SHA-256 %s; UBIFS sentinel intact", userCapacity, userDigest)
		return
	}
	if !strings.Contains(string(fixtureBytes(t, "/proc/cmdline")), "q1maintenance_phase=interrupt") {
		t.Fatal("unknown fixture phase")
	}
	same(initial)
	if _, err := exec.Command("./maintenance", "verify", "approved.tar", "owner.pub").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("./maintenance", "install", "approved.tar", "owner.pub").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "5.10.160-android") {
		t.Fatalf("ordinary CLI must reject the test kernel: %v %s", err, out)
	}
	same(initial)
	blockedBackup := filepath.Join(tmp, "wrong-target.blob")
	if _, err := exec.Command("./maintenance", "backup", blockedBackup).CombinedOutput(); err == nil {
		t.Fatal("ordinary CLI accepted a different MTD device")
	}
	if _, err := os.Lstat(blockedBackup); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused backup produced an output", err)
	}
	t.Log("Q1_MAINT production CLI verifies signature, refuses the emulated kernel and foreign backup target")

	for _, invalid := range []string{"bad-signature.tar", "wrong-volume.tar"} {
		if _, err := verifyBundle(invalid, "owner.pub"); err == nil {
			t.Fatalf("accepted invalid package %s", invalid)
		}
		rejected, err := exec.Command("./maintenance", "verify", invalid, "owner.pub").CombinedOutput()
		if err == nil {
			t.Fatalf("verify accepted %s", invalid)
		}
		installError, err := exec.Command("./maintenance", "install", invalid, "owner.pub").CombinedOutput()
		if err == nil || !bytes.Equal(installError, rejected) {
			t.Fatalf("install did not reject %s during package verification: %v %s", invalid, err, installError)
		}
		same(initial)
	}
	t.Log("Q1_MAINT bad signature and trusted signature for volume 1 rejected; software unchanged")

	backup := filepath.Join(tmp, "before.blob")
	sum, err := target.backup(backup)
	if err != nil || fmt.Sprintf("%x", sum) != initial || fixtureHash(t, backup, capacity) != initial {
		t.Fatalf("backup: %x %v", sum, err)
	}
	if _, err := target.backup(backup); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing backup was not protected: %v", err)
	}
	t.Logf("Q1_MAINT raw software backup %d bytes sha256=%s, existing output protected", capacity, initial)

	valid, err := verifyBundle("approved.tar", "owner.pub")
	if err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(tmp, "mounted")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("/dev/ubiblock0_0", mount, "squashfs", unix.MS_RDONLY, ""); err != nil {
		t.Fatal(err)
	}
	if err := target.install(valid); !errors.Is(err, unix.EBUSY) {
		t.Fatalf("mounted target did not return EBUSY: %v", err)
	}
	if err := unix.Unmount(mount, 0); err != nil {
		t.Fatal(err)
	}
	same(initial)
	t.Log("Q1_MAINT mounted SquashFS held the target; kernel refused update; software unchanged")
	installed := string(fixtureBytes(t, "installed.sha256"))
	for i := 0; i < 2; i++ {
		if err := target.install(valid); err != nil {
			t.Fatal(err)
		}
		same(installed)
	}
	t.Logf("Q1_MAINT install and retry read back expected software SHA-256 %x", valid.digest)

	// Truncate only the marked test volume, using the kernel's ordinary update ioctl.
	vol, err := target.open(os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	length := int64(len(valid.image))
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, vol.Fd(), unix.UBI_IOCVOLUP, uintptr(unsafe.Pointer(&length)))
	if errno != 0 {
		vol.Close()
		t.Fatal(errno)
	}
	if _, err := vol.Write(valid.image[:len(valid.image)/3]); err != nil {
		vol.Close()
		t.Fatal(err)
	}
	if err := vol.Close(); err != nil {
		t.Fatal(err)
	}
	if string(fixtureBytes(t, softwareVolume+"/upd_marker")) != "1\n" {
		t.Fatal("interrupted update was not marked by UBI")
	}
	partial := filepath.Join(tmp, "interrupted.blob")
	if _, err := target.backup(partial); err == nil {
		t.Fatal("accepted backup of interrupted volume")
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted backup left an output", err)
	}
	t.Log("Q1_MAINT real interrupted UBI update flagged; backup refused without publishing any file")
	t.Log("Q1_MAINT interrupt phase complete; leaving the persistent update marker for the next guest boot")
}
