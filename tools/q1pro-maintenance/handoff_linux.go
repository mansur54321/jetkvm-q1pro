package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const handoffDir = "/run/jetkvm-handoff"
const handoffRoot = handoffDir + "/root"
const handoffBin = handoffDir + "/maintenance"

type handoffMachine struct {
	kernel           string
	storage          storageFacts
	afterInstall     int
	watchdogIdentity string
	forwardWrite     func(*volumeTarget, *verifiedBundle) error
}

var boardHandoff = handoffMachine{kernel: "5.10.160-android", storage: q1StorageFacts, afterInstall: unix.LINUX_REBOOT_CMD_RESTART, watchdogIdentity: "Synopsys DesignWare Watchdog", forwardWrite: (*volumeTarget).install}

type handoffPlan struct {
	Current string
	Next    string
}

func textFile(path string) (string, error) {
	b, e := os.ReadFile(path)
	return strings.TrimSpace(string(b)), e
}

func (h handoffMachine) target() (*volumeTarget, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("the Q1 update requires root")
	}
	if err := checkBoard(); err != nil {
		return nil, err
	}
	k, e := textFile("/proc/sys/kernel/osrelease")
	if e != nil {
		return nil, e
	}
	sku, e := textFile("/etc/jetkvm-sku")
	if e != nil {
		return nil, e
	}
	if k != h.kernel || sku != "oraybox-q1pro" {
		return nil, errors.New("unexpected Q1 OS or kernel")
	}
	return discoverStorage(h.storage)
}

// The factory kernel reports SFOO (the on-media magic) from statfs.
// The open BSP reports the standard SquashFS magic for a normalized image.
func q1SquashType(kind uint32) bool {
	return kind == 0x4f4f4653 || kind == unix.SQUASHFS_MAGIC
}

func checkBootedVolume() error {
	var root, block unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Stat("/", &root); err != nil {
		return err
	}
	if err := unix.Statfs("/", &fs); err != nil {
		return err
	}
	if err := unix.Stat("/dev/ubiblock0_0", &block); err != nil {
		return err
	}
	if root.Dev != block.Rdev || block.Mode&unix.S_IFMT != unix.S_IFBLK || !q1SquashType(uint32(fs.Type)) || fs.Flags&unix.ST_RDONLY == 0 {
		return errors.New("expected the running read-only UBI0 software root")
	}
	expectedInit := "/bin/busybox"
	if os.Getpid() == 1 {
		expectedInit = handoffBin
	}
	shell, e := os.Stat(expectedInit)
	if e != nil {
		return e
	}
	init, e := os.Stat("/proc/1/exe")
	if e != nil {
		return e
	}
	if !os.SameFile(shell, init) {
		return errors.New("unexpected PID 1 executable")
	}
	return nil
}

func ramFS(path string) error {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return err
	}
	if uint32(fs.Type) != unix.TMPFS_MAGIC && uint32(fs.Type) != unix.RAMFS_MAGIC {
		return fmt.Errorf("%s must be in RAM", path)
	}
	return nil
}
func bindRO(from, to string) error {
	if err := unix.Mount(from, to, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", to, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, "")
}
func stageBind(source, name string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if err := ramFS(source); err != nil {
		return err
	}
	file, err := openBundleFile(source, bundleArchiveLimit)
	if err != nil {
		return err
	}
	defer file.Close()
	target := handoffDir + "/" + name
	if err := os.WriteFile(target, nil, 0400); err != nil {
		return err
	}
	return bindRO(fmt.Sprintf("/proc/self/fd/%d", file.Fd()), target)
}
func marker(path string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	return errors.Join(e, f.Close())
}
func underMount(path string) (bool, error) {
	content, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return false, e
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		f := strings.Fields(line)
		if len(f) > 5 && f[4] == path {
			return true, nil
		}
	}
	return false, nil
}
func detachIfMounted(path string) error {
	active, e := underMount(path)
	if e != nil {
		return e
	}
	if active {
		return unix.Unmount(path, 0)
	}
	return nil
}
func clearHandoff() error {
	for _, p := range []string{handoffRoot + "/etc/rcS", handoffRoot, handoffDir + "/previous", handoffDir + "/update", handoffDir + "/key"} {
		if e := detachIfMounted(p); e != nil {
			return e
		}
	}
	for _, n := range []string{"root", "previous", "update", "key", "root.img", "rcS.ram", "inittab", "maintenance", "plan.json", "intent", "owner"} {
		if e := os.Remove(handoffDir + "/" + n); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return os.Remove(handoffDir)
}

func mountHandoffRoot() error {
	image, e := os.Open(handoffDir + "/root.img")
	if e != nil {
		return e
	}
	defer image.Close()
	loop, e := os.Open("/dev/loop0")
	if e != nil {
		return e
	}
	defer loop.Close()
	if e := unix.IoctlSetInt(int(loop.Fd()), unix.LOOP_SET_FD, int(image.Fd())); e != nil {
		return fmt.Errorf("loop0 is unavailable: %w", e)
	}
	if e := unix.IoctlLoopSetStatus64(int(loop.Fd()), &unix.LoopInfo64{Flags: unix.LO_FLAGS_READ_ONLY | unix.LO_FLAGS_AUTOCLEAR}); e != nil {
		_ = unix.IoctlSetInt(int(loop.Fd()), unix.LOOP_CLR_FD, 0)
		return e
	}
	if e := unix.Mount("/dev/loop0", handoffRoot, "squashfs", unix.MS_RDONLY, ""); e != nil {
		return e
	}
	rc, e := os.ReadFile(handoffRoot + "/etc/rcS")
	if e != nil {
		return e
	}
	const old = `[ "$(cat /sys/class/ubi/ubi0_1/name)" = rootfs_data ]
mount -t ubifs ubi0:rootfs_data /mnt/data`
	const replace = "mount -t tmpfs -o size=16m,mode=0755,nosuid,nodev tmpfs /mnt/data"
	if bytes.Count(rc, []byte(old)) != 1 {
		return errors.New("unrecognized release storage initialization")
	}
	if e := marker(handoffDir+"/rcS.ram", bytes.Replace(rc, []byte(old), []byte(replace), 1), 0500); e != nil {
		return e
	}
	return bindRO(handoffDir+"/rcS.ram", handoffRoot+"/etc/rcS")
}

func prefixMatches(t *volumeTarget, b *verifiedBundle) error {
	f, e := t.open(os.O_RDONLY)
	if e != nil {
		return e
	}
	defer f.Close()
	d, e := volumeHash(f, int64(len(b.image)))
	if e != nil {
		return e
	}
	if d != b.digest {
		return errors.New("signed previous package does not match the running software volume")
	}
	return nil
}
func prepareHandoff(t *volumeTarget, update, previous, key string) (err error) {
	if err = checkBootedVolume(); err != nil {
		return err
	}
	if err = ramFS("/run"); err != nil {
		return err
	}
	if err = os.Mkdir(handoffDir, 0700); err != nil {
		return fmt.Errorf("create private staging directory (discard an earlier prepared stage first): %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, clearHandoff())
		}
	}()
	lock, err := os.Open(handoffDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	if err = marker(handoffDir+"/owner", []byte("q1-volume-handoff-v1"), 0400); err != nil {
		return err
	}
	for _, p := range [][2]string{{update, "update"}, {previous, "previous"}, {key, "key"}} {
		if err = stageBind(p[0], p[1]); err != nil {
			return err
		}
	}
	old, err := verifyBundle(handoffDir+"/previous", handoffDir+"/key")
	if err != nil {
		return err
	}
	if err = prefixMatches(t, old); err != nil {
		return err
	}
	plan := handoffPlan{Current: hex.EncodeToString(old.digest[:])}
	if err = marker(handoffDir+"/root.img", old.image, 0400); err != nil {
		return err
	}
	old = nil
	next, err := verifyBundle(handoffDir+"/update", handoffDir+"/key")
	if err != nil {
		return err
	}
	plan.Next = hex.EncodeToString(next.digest[:])
	next = nil
	if plan.Next == plan.Current {
		return errors.New("the requested software is already installed")
	}
	src, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer src.Close()
	dest, err := os.OpenFile(handoffBin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if err != nil {
		return err
	}
	_, err = io.Copy(dest, src)
	err = errors.Join(err, dest.Close())
	if err != nil {
		return err
	}
	if err = os.Mkdir(handoffRoot, 0700); err != nil {
		return err
	}
	if err = mountHandoffRoot(); err != nil {
		return err
	}
	if mounted, me := underMount("/etc/inittab"); me != nil {
		return me
	} else if mounted {
		return errors.New("existing custom inittab mount")
	}
	table, err := os.ReadFile("/etc/inittab")
	if err != nil {
		return err
	}
	if bytes.Contains(table, []byte("::restart:")) {
		return errors.New("existing BusyBox restart action")
	}
	table = append(table, []byte("\n::restart:"+handoffBin+" handoff-pid1\n::respawn:"+handoffBin+" handoff-notify\n")...)
	if err = marker(handoffDir+"/inittab", table, 0400); err != nil {
		return err
	}
	record, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err = marker(handoffDir+"/plan.json", record, 0400); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "PREPARED: original system still running; software %s -> %s\n", plan.Current, plan.Next)
	return nil
}

func loadPlan() (handoffPlan, error) {
	var p handoffPlan
	raw, e := os.ReadFile(handoffDir + "/plan.json")
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return p, e
	}
	for _, sum := range []string{p.Current, p.Next} {
		h, e := hex.DecodeString(sum)
		if e != nil || len(h) != sha256.Size {
			return p, errors.New("invalid handoff plan")
		}
	}
	return p, nil
}
func checkPlan(t *volumeTarget, plan handoffPlan) error {
	for _, item := range [][2]string{{"update", plan.Next}, {"previous", plan.Current}} {
		b, e := verifyBundle(handoffDir+"/"+item[0], handoffDir+"/key")
		if e != nil {
			return e
		}
		if hex.EncodeToString(b.digest[:]) != item[1] {
			return errors.New("staged package changed")
		}
		if item[0] == "previous" {
			if e := prefixMatches(t, b); e != nil {
				return e
			}
		}
	}
	return nil
}
func activateHandoff(t *volumeTarget, pin string) error {
	if e := checkBootedVolume(); e != nil {
		return e
	}
	if busy, err := underMount("/etc/inittab"); err != nil {
		return err
	} else if busy {
		return errors.New("existing inittab mount; refusing to replace it")
	}
	plan, e := loadPlan()
	if e != nil {
		return e
	}
	if pin != plan.Next {
		return errors.New("activation requires the exact staged software SHA-256")
	}
	if e = checkPlan(t, plan); e != nil {
		return e
	}
	if e = marker(handoffDir+"/intent", []byte(pin), 0400); e != nil {
		return e
	}
	if e = bindRO(handoffDir+"/inittab", "/etc/inittab"); e == nil {
		e = unix.Kill(1, unix.SIGHUP)
	}
	if e != nil {
		e = errors.Join(e, detachIfMounted("/etc/inittab"), os.Remove(handoffDir+"/intent"))
	}
	return e
}
func pivotHandoff() error {
	c, e := unix.Open("/dev/console", unix.O_RDWR|unix.O_NOCTTY, 0)
	if e != nil {
		return e
	}
	for n := 0; n < 3; n++ {
		if c != n {
			if e = unix.Dup3(c, n, 0); e != nil {
				return e
			}
		}
	}
	if c > 2 {
		unix.Close(c)
	}
	fmt.Fprintln(os.Stdout, "Q1 handoff: BusyBox restarted PID 1 from RAM")
	if e = unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); e != nil {
		return e
	}
	if e = unix.Chdir(handoffRoot); e != nil {
		return e
	}
	if e = unix.PivotRoot(".", "root"); e != nil {
		return e
	}
	if e = unix.Chdir("/"); e != nil {
		return e
	}
	for _, name := range []string{"dev", "proc", "sys", "run", "tmp"} {
		if e = unix.Mount("/root/"+name, "/"+name, "", unix.MS_MOVE, ""); e != nil {
			return e
		}
	}
	if e = unix.Unmount("/root/etc/inittab", 0); e != nil {
		return e
	}
	unix.Sync()
	for _, name := range []string{"/root/userdata", "/root/mnt/data", "/root"} {
		if e = unix.Unmount(name, 0); e != nil {
			return fmt.Errorf("unmount %s: %w", name, e)
		}
	}
	mounts, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return e
	}
	if bytes.Contains(mounts, []byte(" - ubifs ")) {
		return errors.New("a UBIFS mount is still active")
	}
	fmt.Fprintln(os.Stdout, "Q1 handoff: old ubiblock root and UBIFS cleanly unmounted")
	return checkRAMRoot()
}

func runHandoff(m handoffMachine, args []string) error {
	if len(args) == 0 {
		return errors.New("missing handoff operation")
	}
	t, e := m.target()
	if e != nil {
		return e
	}
	if args[0] == "handoff-prepare" && len(args) == 4 {
		if _, err := handoffWatchdogPresent(m.watchdogIdentity); err != nil {
			return err
		}
		return prepareHandoff(t, args[1], args[2], args[3])
	}
	info, e := os.Lstat(handoffDir)
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("expected a private handoff staging directory")
	}
	if e = ramFS(handoffDir); e != nil {
		return e
	}
	owner, e := textFile(handoffDir + "/owner")
	if e != nil || owner != "q1-volume-handoff-v1" {
		return errors.New("unrecognized staged directory")
	}
	lock, e := os.Open(handoffDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return e
	}
	switch args[0] {
	case "handoff-discard":
		if len(args) != 1 {
			return errors.New("unexpected discard arguments")
		}
		if _, e := os.Lstat(handoffDir + "/intent"); !errors.Is(e, os.ErrNotExist) {
			return errors.New("activation is in progress")
		}
		return clearHandoff()
	case "handoff-activate":
		if len(args) != 2 {
			return errors.New("provide staged software SHA-256")
		}
		return activateHandoff(t, args[1])
	case "handoff-notify", "handoff-pid1":
		plan, e := loadPlan()
		if e != nil {
			return e
		}
		pin, e := textFile(handoffDir + "/intent")
		if e != nil || pin != plan.Next {
			return errors.New("missing verified activation intent")
		}
		if args[0] == "handoff-notify" {
			if os.Getppid() != 1 {
				return errors.New("notify requires BusyBox parent")
			}
			// PID 1 has read the new inittab before it can launch this respawn action.
			if e = unix.Kill(1, unix.SIGQUIT); e != nil {
				return e
			}
			lock.Close()
			for {
				time.Sleep(time.Hour)
			}
		}
		if os.Getpid() != 1 {
			return errors.New("restart helper must have PID 1")
		}
		if e = checkBootedVolume(); e != nil {
			return e
		}
		// Any error before UBI_IOCVOLUP leaves the original bootable software in place.
		wd, e := acquireHandoffWatchdog(m.watchdogIdentity)
		if e == nil {
			e = pivotHandoff()
		}
		if e == nil {
			e = m.installFromRAM(plan, wd)
		}
		if e != nil {
			fmt.Fprintf(os.Stderr, "Q1 handoff: %v\n", e)
		}
		unix.Sync()
		if closeErr := wd.finish(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Q1 watchdog final close: %v\n", closeErr)
		}
		if r := unix.Reboot(m.afterInstall); r != nil {
			fmt.Fprintln(os.Stderr, r)
			for {
				time.Sleep(time.Hour)
			}
		}
		return e
	default:
		return errors.New("unknown handoff operation")
	}
}
func (m handoffMachine) installFromRAM(plan handoffPlan, wd *handoffWatchdog) error {
	t, e := m.target()
	if e != nil {
		return e
	}
	old, e := verifyBundle(handoffDir+"/previous", handoffDir+"/key")
	if e != nil {
		return e
	}
	next, e := verifyBundle(handoffDir+"/update", handoffDir+"/key")
	if e != nil {
		return e
	}
	if hex.EncodeToString(old.digest[:]) != plan.Current || hex.EncodeToString(next.digest[:]) != plan.Next {
		return errors.New("signed software plan changed before update")
	}
	if e = prefixMatches(t, old); e != nil {
		return e
	}
	if e = wd.kick(); e != nil {
		return fmt.Errorf("watchdog before UBI update: %w", e)
	}
	if e = m.forwardWrite(t, next); e == nil {
		fmt.Fprintf(os.Stdout, "Q1_HANDOFF_SIGNED_UPDATE_PASS software %x\n", next.digest)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Q1 handoff: update failed (%v), checking signed previous software\n", e)
	if rollback := t.install(old); rollback != nil {
		fmt.Fprintf(os.Stderr, "Q1 handoff: previous software could not be verified (%v); remaining in RAM. No reboot.\n", rollback)
		for {
			time.Sleep(time.Hour)
		}
	}
	fmt.Fprintf(os.Stdout, "Q1_HANDOFF_PREVIOUS_SOFTWARE_RESTORED %x\n", old.digest)
	return fmt.Errorf("update failed, signed previous software restored: %w", e)
}
