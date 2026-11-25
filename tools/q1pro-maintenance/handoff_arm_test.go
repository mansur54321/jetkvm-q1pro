//go:build linux && arm && q1maintenanceguest

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "handoff-") {
		model, err := os.ReadFile("/sys/firmware/devicetree/base/model")
		if err != nil || string(model) != "linux,dummy-virt\x00" {
			fmt.Fprintln(os.Stderr, "only the isolated ARM QEMU handoff fixture is supported")
			os.Exit(1)
		}
		machine := handoffMachine{kernel: "5.10.160-q1-systest", storage: storageFacts{mtdNumber: 0, mtdName: "block2mtd: /dev/vda", mtdType: "ram", mtdSize: 83 << 20,
			eraseSize: 131072, writeSize: 1, rootLEBs: 206, dataLEBs: 434, lebSize: 126976}, afterInstall: unix.LINUX_REBOOT_CMD_POWER_OFF}
		machine.watchdogIdentity = "Software Watchdog"
		machine.forwardWrite = (*volumeTarget).install
		cmdline, _ := os.ReadFile("/proc/cmdline")
		if strings.Contains(string(cmdline), "q1test-injected-write=1") {
			machine.forwardWrite = func(v *volumeTarget, b *verifiedBundle) error {
				// The real softdog is armed at 15 s. Fail if independent PID 1 keepalives are not effective.
				time.Sleep(17 * time.Second)
				file, err := v.open(os.O_RDWR)
				if err != nil {
					return err
				}
				size := int64(len(b.image))
				_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), unix.UBI_IOCVOLUP, uintptr(unsafe.Pointer(&size)))
				if errno != 0 {
					file.Close()
					return errno
				}
				_, err = file.Write(b.image[:131072])
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					return err
				}
				if value, e := interruptedVolume(); e != nil || !value {
					return errors.New("expected a real interrupted update marker")
				}
				fmt.Println("Q1_HANDOFF injected real 131072-byte incomplete UBI update; marker set")
				return errors.New("deliberately interrupted test update")
			}
		}
		if err := runHandoff(machine, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
