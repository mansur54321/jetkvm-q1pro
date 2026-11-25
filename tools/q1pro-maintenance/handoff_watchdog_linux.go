package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type handoffWatchdog struct {
	file   *os.File
	stop   chan struct{}
	closed chan struct{}
	mu     sync.Mutex
	pings  int
}

// The device is opened only by the replacement PID 1 after BusyBox has stopped the previous owner.
func handoffWatchdogPresent(identity string) (bool, error) {
	var node unix.Stat_t
	if err := unix.Lstat("/dev/watchdog", &node); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if node.Mode&unix.S_IFMT != unix.S_IFCHR || node.Rdev != unix.Mkdev(10, 130) {
		return false, errors.New("unexpected /dev/watchdog device")
	}
	if identity == "Synopsys DesignWare Watchdog" {
		driver, err := filepath.EvalSymlinks("/sys/class/watchdog/watchdog0/device/driver")
		if err != nil || driver != "/sys/bus/platform/drivers/dw_wdt" {
			return false, errors.New("watchdog0 does not match the Q1 DesignWare platform driver")
		}
	}
	return true, nil
}

func acquireHandoffWatchdog(identity string) (*handoffWatchdog, error) {
	present, err := handoffWatchdogPresent(identity)
	if err != nil || !present {
		return nil, err
	}
	path := "/dev/watchdog"
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("take over watchdog before updating: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var info struct {
		Options, Version uint32
		Identity         [32]byte
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.WDIOC_GETSUPPORT, uintptr(unsafe.Pointer(&info)))
	if errno != 0 || string(bytes.TrimRight(info.Identity[:], "\x00")) != identity || info.Options&0x100 == 0 {
		// The old software volume is still intact at this point.
		f.Close()
		return nil, errors.New("unexpected watchdog identity or missing magic-close support")
	}
	timeout, err := unix.IoctlGetInt(fd, unix.WDIOC_GETTIMEOUT)
	if err != nil || timeout <= 0 {
		// Try magic close without hiding the acquisition error.
		_, _ = f.Write([]byte("V"))
		f.Close()
		return nil, fmt.Errorf("read watchdog timeout: %d (%v)", timeout, err)
	}
	w := &handoffWatchdog{file: f, stop: make(chan struct{}), closed: make(chan struct{})}
	if err := w.kick(); err != nil {
		// Preserve the first keepalive error if magic close also fails.
		_, _ = f.Write([]byte("V"))
		f.Close()
		return nil, err
	}
	period := min(time.Second, time.Duration(timeout)*time.Second/4)
	fmt.Fprintf(os.Stdout, "Q1 handoff: %s, actual timeout %ds; keepalive every %s\n", identity, timeout, period)
	go func() {
		defer close(w.closed)
		tick := time.NewTicker(period)
		defer tick.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-tick.C:
				if err := w.kick(); err != nil {
					fmt.Fprintf(os.Stderr, "Q1 watchdog keepalive: %v\n", err)
				}
			}
		}
	}()
	return w, nil
}
func (w *handoffWatchdog) kick() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := unix.IoctlSetPointerInt(int(w.file.Fd()), unix.WDIOC_KEEPALIVE, 0)
	if err != nil {
		_, err = w.file.Write([]byte("K"))
	}
	if err == nil {
		w.pings++
	}
	return err
}
func (w *handoffWatchdog) finish() error {
	if w == nil {
		return nil
	}
	close(w.stop)
	<-w.closed
	_, err := w.file.Write([]byte("V"))
	closeErr := w.file.Close()
	fmt.Fprintf(os.Stdout, "Q1 handoff: %d watchdog keepalives before final magic close\n", w.pings)
	return errors.Join(err, closeErr)
}
