//go:build linux && amd64

// PID 1 for the disposable USB test VM.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func run() error {
	for _, name := range []string{"dev", "proc", "sys"} {
		fs := map[string]string{"dev": "devtmpfs", "proc": "proc", "sys": "sysfs"}[name]
		if err := os.MkdirAll("/"+name, 0755); err != nil {
			return err
		}
		if err := syscall.Mount(fs, "/"+name, fs, 0, ""); err != nil {
			return err
		}
	}
	f, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	os.Stdout, os.Stderr = f, f
	order, err := os.ReadFile("/modules/order")
	if err != nil {
		return err
	}
	for _, path := range strings.Fields(string(order)) {
		if path == "/modules/dummy_hcd.ko" {
			continue // the test registers the controller after JetKVM starts
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = unix.FinitModule(int(file.Fd()), "", 0)
		file.Close()
		if err != nil {
			return fmt.Errorf("load %s: %w", path, err)
		}
	}
	if err := syscall.Mount("none", "/sys/kernel/config", "configfs", 0, ""); err != nil {
		return err
	}
	s, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	iface, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	iface.SetUint16(unix.IFF_UP)
	err = unix.IoctlIfreq(s, unix.SIOCSIFFLAGS, iface)
	unix.Close(s)
	if err != nil {
		return err
	}
	cmd := exec.Command("/check", "-test.v", "-test.run", "^TestDisposableDummyHCD$", "-test.timeout", "45s")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Println("Q1 DUMMY HCD PASS")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Q1 DUMMY HCD FAIL:", err)
	}
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
