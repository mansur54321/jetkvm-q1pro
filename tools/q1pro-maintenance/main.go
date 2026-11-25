//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

func checkBoard() error {
	tree, err := os.ReadFile("/sys/firmware/devicetree/base/compatible")
	if err != nil {
		return fmt.Errorf("read board identity: %w", err)
	}
	compat := strings.Split(string(tree), "\x00")
	if !slices.Contains(compat, "oraybox,q1pro") || !slices.Contains(compat, "rockchip,rv1106") {
		return errors.New("this image requires an Oray Q1 Pro with RV1106")
	}
	return nil
}

// The maintenance RAM image binds /etc/rcS from the same RAM filesystem that
// contains the loop backing file. A normal boot from ubiblock fails this check.
func checkRAMRoot() error {
	var root, rc unix.Stat_t
	var fs, rcFS unix.Statfs_t
	if err := unix.Stat("/", &root); err != nil {
		return err
	}
	if err := unix.Statfs("/", &fs); err != nil {
		return err
	}
	if unix.Major(root.Dev) != 7 || unix.Minor(root.Dev) != 0 || fs.Flags&unix.ST_RDONLY == 0 {
		return errors.New("installation requires the read-only loop0 root from the Q1 RAM image")
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	squashRoot := false
	for line := range strings.SplitSeq(string(mounts), "\n") {
		left, right, ok := strings.Cut(line, " - ")
		fields := strings.Fields(left)
		if ok && len(fields) >= 6 && fields[2] == "7:0" && fields[3] == "/" && fields[4] == "/" && strings.HasPrefix(right, "squashfs ") {
			squashRoot = true
			break
		}
	}
	if !squashRoot {
		return errors.New("loop0 must provide the SquashFS root filesystem")
	}
	if err := unix.Stat("/etc/rcS", &rc); err != nil {
		return err
	}
	if err := unix.Statfs("/etc/rcS", &rcFS); err != nil {
		return err
	}
	if (uint32(rcFS.Type) != unix.RAMFS_MAGIC && uint32(rcFS.Type) != unix.TMPFS_MAGIC) ||
		rcFS.Flags&unix.ST_RDONLY == 0 || rc.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("the Q1 startup script must be bound read-only from RAM")
	}
	fd, err := unix.Open("/dev/loop0", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var dev unix.Stat_t
	if err := unix.Fstat(fd, &dev); err != nil {
		return err
	}
	if dev.Mode&unix.S_IFMT != unix.S_IFBLK || dev.Rdev != root.Dev {
		return errors.New("loop0 does not match the running root filesystem")
	}
	status, err := unix.IoctlLoopGetStatus64(fd)
	if err != nil {
		return fmt.Errorf("inspect loop backing device: %w", err)
	}
	if status.Device != rc.Dev || status.Flags&unix.LO_FLAGS_READ_ONLY == 0 {
		return errors.New("the root image must be backed by the same RAM filesystem as /etc/rcS")
	}
	return nil
}

func checkInstallContext() error {
	if err := checkBoard(); err != nil {
		return err
	}
	kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(kernel)) != "5.10.160-android" {
		return errors.New("this software volume targets the 5.10.160-android kernel")
	}
	sku, err := os.ReadFile("/etc/jetkvm-sku")
	if err != nil || strings.TrimSpace(string(sku)) != "oraybox-q1pro" {
		return errors.New("the running userspace must be the Q1 JetKVM RAM image")
	}
	return checkRAMRoot()
}

func installTarget() (*volumeTarget, error) {
	if err := checkInstallContext(); err != nil {
		return nil, err
	}
	return discoverTarget()
}

func run(args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "handoff-") {
		return runHandoff(boardHandoff, args)
	}
	if len(args) == 1 && args[0] == "check" {
		target, err := installTarget()
		if err != nil {
			return err
		}
		fmt.Printf("Q1 RAM root and UBI layout verified; software volume capacity %d bytes\n", target.capacity)
		return nil
	}
	if len(args) == 2 && args[0] == "backup" {
		if err := checkBoard(); err != nil {
			return err
		}
		target, err := discoverTarget()
		if err != nil {
			return err
		}
		digest, err := target.backup(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Software volume backup SHA-256 %x\n", digest)
		return nil
	}
	if len(args) != 3 || (args[0] != "verify" && args[0] != "install") {
		return errors.New("usage: q1pro-maintenance check | verify PACKAGE PUBLIC_KEY | backup NEW_FILE | install PACKAGE PUBLIC_KEY")
	}
	bundle, err := verifyBundle(args[1], args[2])
	if err != nil {
		return err
	}
	if args[0] == "verify" {
		fmt.Printf("Verified %s (%d bytes), SHA-256 %x\n", bundle.app, len(bundle.image), bundle.digest)
		return nil
	}
	target, err := installTarget()
	if err != nil {
		return err
	}
	if err := target.install(bundle); err != nil {
		return err
	}
	fmt.Printf("Verified %d payload bytes in UBI volume 0, SHA-256 %x\n", len(bundle.image), bundle.digest)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if os.Getpid() == 1 {
			fmt.Fprintln(os.Stderr, "Q1 maintenance stopped in RAM; no automatic retry")
			for {
				unix.Pause()
			}
		}
		os.Exit(1)
	}
}
