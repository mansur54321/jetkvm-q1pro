package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

type storageFacts struct {
	mtdNumber                     int
	mtdName, mtdType              string
	mtdSize, eraseSize, writeSize int64
	rootLEBs, dataLEBs, lebSize   int64
}

var q1StorageFacts = storageFacts{
	mtdNumber: 6, mtdName: "rootfs", mtdType: "nand", mtdSize: 83 << 20, eraseSize: 131072, writeSize: 2048,
	rootLEBs: 206, dataLEBs: 434, lebSize: 126976,
}

const softwareVolume = "/sys/class/ubi/ubi0_0"

type volumeTarget struct {
	facts    storageFacts
	rdev     uint64
	capacity int64
}

func storageAttribute(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func expectStorageAttr(dir, key, want string) error {
	path := filepath.Join(dir, key)
	got, err := storageAttribute(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: got %q, expected %q", path, got, want)
	}
	return nil
}

func volumeDeviceNumber() (uint64, error) {
	text, err := storageAttribute(softwareVolume + "/dev")
	if err != nil {
		return 0, err
	}
	a, b, ok := strings.Cut(text, ":")
	major, errA := strconv.ParseUint(a, 10, 32)
	minor, errB := strconv.ParseUint(b, 10, 32)
	if !ok || errA != nil || errB != nil || major == 0 || minor != 1 {
		return 0, fmt.Errorf("invalid ubi0_0 device number %q", text)
	}
	return unix.Mkdev(uint32(major), uint32(minor)), nil
}

func discoverTarget() (*volumeTarget, error) {
	return discoverStorage(q1StorageFacts)
}

func discoverStorage(f storageFacts) (*volumeTarget, error) {
	mtd := fmt.Sprintf("/sys/class/mtd/mtd%d", f.mtdNumber)
	num := func(value int64) string { return strconv.FormatInt(value, 10) }
	for _, a := range []struct{ dir, key, want string }{
		{"/sys/class/ubi/ubi0", "mtd_num", strconv.Itoa(f.mtdNumber)},
		{mtd, "name", f.mtdName}, {mtd, "type", f.mtdType}, {mtd, "size", num(f.mtdSize)}, {mtd, "erasesize", num(f.eraseSize)},
		{mtd, "writesize", num(f.writeSize)},
		{softwareVolume, "name", "rootfs"}, {softwareVolume, "type", "dynamic"},
		{softwareVolume, "reserved_ebs", num(f.rootLEBs)}, {softwareVolume, "usable_eb_size", num(f.lebSize)},
		{"/sys/class/ubi/ubi0_1", "name", "rootfs_data"}, {"/sys/class/ubi/ubi0_1", "type", "dynamic"},
		{"/sys/class/ubi/ubi0_1", "reserved_ebs", num(f.dataLEBs)}, {"/sys/class/ubi/ubi0_1", "usable_eb_size", num(f.lebSize)},
	} {
		if err := expectStorageAttr(a.dir, a.key, a.want); err != nil {
			return nil, err
		}
	}
	dev, err := volumeDeviceNumber()
	if err != nil {
		return nil, err
	}
	return &volumeTarget{facts: f, rdev: dev, capacity: f.rootLEBs * f.lebSize}, nil
}

func (t *volumeTarget) open(flags int) (*os.File, error) {
	// O_PATH pins the inode before invoking any character driver's open routine.
	fd, err := unix.Open("/dev/ubi0_0", unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != t.rdev {
		return nil, fmt.Errorf("device /dev/ubi0_0 does not match sysfs")
	}
	vol, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", fd), flags, 0)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(vol.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		var live *volumeTarget
		live, err = discoverStorage(t.facts)
		if err == nil && *live != *t {
			err = errors.New("UBI volume identity changed")
		}
	}
	if err != nil {
		vol.Close()
		return nil, err
	}
	return vol, nil
}

func interruptedVolume() (bool, error) {
	marker, err := storageAttribute(softwareVolume + "/upd_marker")
	if err != nil {
		return false, err
	}
	switch marker {
	case "0":
		return false, expectStorageAttr(softwareVolume, "corrupted", "0")
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("invalid UBI update marker %q", marker)
	}
}

func (t *volumeTarget) backup(dest string) ([32]byte, error) {
	vol, err := t.open(os.O_RDONLY)
	if err != nil {
		return [32]byte{}, err
	}
	defer vol.Close()
	interrupted, err := interruptedVolume()
	if err != nil {
		return [32]byte{}, err
	}
	if interrupted {
		return [32]byte{}, errors.New("cannot back up a volume with an interrupted update")
	}
	return newBackup(dest, vol, t.capacity)
}

func newBackup(dest string, source io.Reader, size int64) ([32]byte, error) {
	var digest [32]byte
	dir := filepath.Dir(dest)
	out, err := os.CreateTemp(dir, ".q1-software-backup-*")
	if err != nil {
		return digest, err
	}
	defer os.Remove(out.Name())
	h := sha256.New()
	_, err = io.CopyN(io.MultiWriter(out, h), source, size)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return digest, err
	}
	err = unix.Renameat2(unix.AT_FDCWD, out.Name(), unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
		err = os.Link(out.Name(), dest)
	}
	if err != nil {
		return digest, err
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return digest, fmt.Errorf("complete backup created at %s; syncing its directory failed: %w", dest, err)
	}
	h.Sum(digest[:0])
	return digest, nil
}

func volumeHash(vol *os.File, size int64) ([32]byte, error) {
	var sum [32]byte
	h := sha256.New()
	_, err := io.CopyN(h, io.NewSectionReader(vol, 0, size), size)
	h.Sum(sum[:0])
	return sum, err
}

func (t *volumeTarget) install(b *verifiedBundle) error {
	if b == nil || len(b.image) == 0 || int64(len(b.image)) > t.capacity || sha256.Sum256(b.image) != b.digest {
		return errors.New("invalid in-memory software image")
	}
	vol, err := t.open(os.O_RDWR)
	if err != nil {
		return err
	}
	defer vol.Close()
	size := int64(len(b.image))
	interrupted, err := interruptedVolume()
	if err != nil {
		return err
	}
	if !interrupted {
		sum, err := volumeHash(vol, size)
		if err != nil {
			return err
		}
		if sum == b.digest {
			return nil
		}
	}
	// The kernel takes exclusive ownership before setting the on-flash update marker.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, vol.Fd(), unix.UBI_IOCVOLUP, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return fmt.Errorf("begin exclusive UBI software-volume update: %w", errno)
	}
	if _, err := vol.Write(b.image); err != nil {
		return fmt.Errorf("write UBI software volume: %w", err)
	}
	if err := vol.Sync(); err != nil {
		return err
	}
	if err := expectStorageAttr(softwareVolume, "upd_marker", "0"); err != nil {
		return err
	}
	if err := expectStorageAttr(softwareVolume, "corrupted", "0"); err != nil {
		return err
	}
	sum, err := volumeHash(vol, size)
	if err != nil {
		return err
	}
	if sum != b.digest {
		return errors.New("UBI software-volume readback digest mismatch")
	}
	return nil
}
