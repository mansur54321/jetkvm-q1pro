//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func read(path string) string {
	data, err := os.ReadFile(path)
	must(err)
	return strings.TrimSpace(string(data))
}

func wait(description string, ok func() bool) {
	deadline := time.Now().Add(8 * time.Second)
	for !ok() {
		require(time.Now().Before(deadline), "timed out waiting for "+description)
		time.Sleep(20 * time.Millisecond)
	}
}

func checkNode(pattern string, mode uint32) string {
	var node, path string
	var stat unix.Stat_t
	wait(pattern, func() bool {
		paths, err := filepath.Glob(pattern)
		must(err)
		for _, path = range paths {
			raw, err := os.ReadFile(filepath.Join(path, "uevent"))
			if err != nil {
				continue
			}
			fields := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				if key, value, ok := strings.Cut(line, "="); ok {
					fields[key] = value
				}
			}
			if fields["DEVNAME"] == "" {
				continue
			}
			node = "/dev/" + fields["DEVNAME"]
			if err := unix.Stat(node, &stat); err != nil {
				continue
			}
			major, err := strconv.Atoi(fields["MAJOR"])
			must(err)
			minor, err := strconv.Atoi(fields["MINOR"])
			must(err)
			require(stat.Rdev == unix.Mkdev(uint32(major), uint32(minor)), "wrong device number: "+node)
			require(stat.Mode&unix.S_IFMT == unix.S_IFCHR, "expected char device "+node)
			require(stat.Mode&0777 == mode && stat.Uid == 0 && stat.Gid == 0, "wrong owner or mode: "+node)
			return true
		}
		return false
	})
	fmt.Fprintf(os.Stdout, "%s -> %s %d:%d %04o\n", path, node, unix.Major(stat.Rdev), unix.Minor(stat.Rdev), mode)
	return node
}

func remove(node string) {
	wait(node+" removal", func() bool {
		_, err := os.Lstat(node)
		return errors.Is(err, os.ErrNotExist)
	})
	fmt.Fprintf(os.Stdout, "removed %s\n", node)
}

func insmod(name string) {
	cmd := exec.Command("/sbin/insmod", "/modules/"+name+".ko")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	must(cmd.Run())
}

func main() {
	pid1, err := os.Readlink("/proc/1/exe")
	must(err)
	require(pid1 == "/bin/busybox", "expected packaged BusyBox as PID 1")
	fmt.Fprintln(os.Stdout, "PID 1:", pid1)
	require(read("/sys/firmware/devicetree/base/model") == "linux,dummy-virt\x00", "expected QEMU virt")
	require(!strings.Contains(read("/proc/filesystems"), "devtmpfs"), "expected CONFIG_DEVTMPFS=n")
	require(read("/proc/sys/kernel/hotplug") == "", "unexpected kernel uevent helper")
	require(strings.Contains(read("/proc/mounts"), " /dev tmpfs "), "/dev is not tmpfs")
	require(strings.Contains(read("/proc/mounts"), " /dev/pts devpts "), "missing devpts")
	checkNode("/sys/class/mem/null", 0666)
	checkNode("/sys/class/tty/console", 0600)
	checkNode("/sys/class/tty/ptmx", 0666)
	checkNode("/sys/class/misc/loop-control", 0660)
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	must(err)
	must(unix.Close(fd))

	insmod("q1_test_nodes")
	mpi := checkNode("/sys/class/misc/q1-test-mpi", 0660)
	bsg := checkNode("/sys/class/misc/q1-test-bsg", 0660)
	video := checkNode("/sys/class/video4linux/video*", 0660)
	require(mpi == "/dev/mpi/q1-test" && bsg == "/dev/bsg/q1-test", "ignored kernel DEVNAME")
	must(os.Remove(mpi))
	must(exec.Command("/sbin/mdev", "-s").Run())
	checkNode("/sys/class/misc/q1-test-mpi", 0660)
	must(unix.DeleteModule("q1_test_nodes", unix.O_NONBLOCK))
	remove(mpi)
	remove(bsg)
	remove(video)

	insmod("snd-dummy")
	pcm := checkNode("/sys/class/sound/pcmC*D0p", 0660)
	require(strings.HasPrefix(pcm, "/dev/snd/pcm"), "incorrect ALSA path")
	must(unix.DeleteModule("snd_dummy", unix.O_NONBLOCK))
	remove(pcm)

	insmod("dummy_hcd")
	insmod("libcomposite")
	insmod("usb_f_hid")
	g := "/sys/kernel/config/usb_gadget/mdevtest"
	must(os.Mkdir(g, 0755))
	write := func(name string, value []byte) { must(os.WriteFile(filepath.Join(g, name), value, 0)) }
	write("idVendor", []byte("0x1d6b"))
	write("idProduct", []byte("0x0104"))
	must(os.Mkdir(filepath.Join(g, "configs/c.1"), 0755))
	must(os.Mkdir(filepath.Join(g, "functions/hid.kbd"), 0755))
	write("functions/hid.kbd/protocol", []byte("1"))
	write("functions/hid.kbd/subclass", []byte("1"))
	write("functions/hid.kbd/report_length", []byte("8"))
	write("functions/hid.kbd/report_desc", []byte{
		0x05, 1, 0x09, 6, 0xa1, 1, 0x05, 7, 0x19, 0xe0, 0x29, 0xe7, 0x15, 0, 0x25, 1, 0x75, 1, 0x95, 8,
		0x81, 2, 0x95, 1, 0x75, 8, 0x81, 1, 0x95, 6, 0x75, 8, 0x15, 0, 0x25, 0x65, 0x19, 0, 0x29, 0x65,
		0x81, 0, 0xc0,
	})
	must(os.Symlink(filepath.Join(g, "functions/hid.kbd"), filepath.Join(g, "configs/c.1/hid.kbd")))
	udcs, err := os.ReadDir("/sys/class/udc")
	must(err)
	require(len(udcs) == 1 && strings.HasPrefix(udcs[0].Name(), "dummy_udc"), "expected dummy UDC")
	for cycle := 1; cycle <= 2; cycle++ {
		write("UDC", []byte(udcs[0].Name()))
		hid := checkNode("/sys/class/hidg/hidg*", 0660)
		input := checkNode("/sys/class/input/event*", 0660)
		require(strings.HasPrefix(input, "/dev/input/event"), "incorrect input path")
		file, err := os.OpenFile(hid, os.O_RDWR, 0)
		must(err)
		must(file.Close())
		write("UDC", []byte("\n"))
		remove(hid)
		remove(input)
		fmt.Fprintf(os.Stdout, "dummy HCD bind/unbind %d passed\n", cycle)
	}
	fmt.Fprintln(os.Stdout, "Q1 mdev QEMU PASS")
	for {
		time.Sleep(time.Hour)
	}
}
