//go:build linux

package kvm

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pojntfx/go-nbd/pkg/ioctl"
	"golang.org/x/sys/unix"
)

const (
	nbdSysfs = "/sys/class/block/nbd0"
	// linux/nbd.h: consecutive _IO(0xab, n) command numbers.
	nbdClearSock    = ioctl.NEGOTIATION_IOCTL_SET_SOCK + 4
	nbdDisconnect   = ioctl.NEGOTIATION_IOCTL_SET_SOCK + 8
	nbdSetFlags     = ioctl.NEGOTIATION_IOCTL_SET_SOCK + 10
	nbdFlagReadOnly = 1 << 1
)

func (d *NBDDevice) openConnections() error {
	if _, err := os.Stat(nbdSysfs + "/pid"); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return errors.New("nbd0 is already connected")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	d.kernelSocket = os.NewFile(uintptr(fds[0]), "nbd-kernel")
	serverFile := os.NewFile(uintptr(fds[1]), "nbd-server")
	defer serverFile.Close()
	d.serverConn, err = net.FileConn(serverFile)
	if err != nil {
		_ = d.kernelSocket.Close()
	}
	return err
}

func (d *NBDDevice) runKernelClient() error {
	// Linux records the calling task as task_setup from SET_SOCK through DO_IT.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sectors := uint64(d.backend.size / 512)
	if d.backend.size < 512 || uint64(uintptr(sectors)) != sectors {
		return errors.New("image size is outside the NBD sector range")
	}
	control := func(cmd, value uintptr) error {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.dev.Fd(), cmd, value)
		if errno != 0 {
			return errno
		}
		return nil
	}
	if err := control(ioctl.NEGOTIATION_IOCTL_SET_SOCK, d.kernelSocket.Fd()); err != nil {
		return err
	}
	defer func() { _ = control(nbdClearSock, 0) }()
	for _, setting := range []struct{ cmd, value uintptr }{
		{ioctl.NEGOTIATION_IOCTL_SET_BLOCKSIZE, 512},
		{ioctl.NEGOTIATION_IOCTL_SET_SIZE_BLOCKS, uintptr(sectors)},
		{nbdSetFlags, nbdFlagReadOnly},
		{ioctl.NEGOTIATION_IOCTL_SET_TIMEOUT, 30},
	} {
		if err := control(setting.cmd, setting.value); err != nil {
			return err
		}
	}
	return control(ioctl.NEGOTIATION_IOCTL_DO_IT, 0)
}

func (d *NBDDevice) connect() (err error) {
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	finished := make(chan error, 1)
	d.finished = make(chan struct{})
	go func() {
		defer close(d.finished)
		err := d.runKernelClient()
		finished <- err
		d.l.Debug().Err(err).Msg("NBD kernel client stopped")
	}()
	timeout := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer timeout.Stop()
	defer ticker.Stop()
	for {
		select {
		case err := <-finished:
			if err == nil {
				return errors.New("NBD disconnected before the image was ready")
			}
			return err
		case <-timeout.C:
			return errors.New("timed out waiting for NBD image capacity")
		case <-ticker.C:
			// The BSP publishes pid when NBD_DO_IT starts. The block layer
			// publishes capacity separately, in 512-byte sectors.
			if d.ownsConnection() {
				size, err := os.ReadFile(nbdSysfs + "/size")
				if err != nil {
					return err
				}
				sectors, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
				if err != nil {
					return err
				}
				if sectors > 0 && sectors == d.backend.size/512 {
					return nil
				}
			}
		}
	}
}

func (d *NBDDevice) ownsConnection() bool {
	b, err := os.ReadFile(nbdSysfs + "/pid")
	if err != nil {
		return false
	}
	tid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || tid <= 0 {
		return false
	}
	_, err = os.Stat(fmt.Sprintf("/proc/self/task/%d", tid))
	return err == nil
}

func (d *NBDDevice) Close() {
	if d.dev != nil && d.ownsConnection() {
		// NBD_DISCONNECT asks the kernel to finish in-flight requests and close the socket.
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.dev.Fd(), nbdDisconnect, 0)
		if errno != 0 {
			d.l.Warn().Err(errno).Msg("error disconnecting nbd client")
		}
	}
	if d.serverConn != nil {
		_ = d.serverConn.Close()
	}
	if d.finished != nil {
		<-d.finished
	}
	if d.kernelSocket != nil {
		_ = d.kernelSocket.Close()
	}
	if d.dev != nil {
		_ = d.dev.Close()
	}
}
