//go:build linux

package q1ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func (c *Controller) Run(ctx context.Context) {
	for ctx.Err() == nil {
		paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, name := range paths {
			data, err := os.ReadFile(name)
			if err != nil || strings.TrimSpace(string(data)) != "adc-keys" {
				continue
			}
			event := filepath.Base(filepath.Dir(filepath.Dir(name)))
			file, err := os.OpenFile(filepath.Join("/dev/input", event), os.O_RDONLY|unix.O_NONBLOCK, 0)
			if err != nil {
				continue
			}
			c.readButton(ctx, file)
			file.Close()
			break
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// KEY_RESTART is the factory Linux DT assignment for the single ADC button.
// Count completed short presses only. A held key or a dropped event never authorizes Wi-Fi.
func (c *Controller) readButton(ctx context.Context, f *os.File) {
	event := make([]byte, int(unsafe.Sizeof(unix.Timeval{}))+8)
	tail := event[len(event)-8:]
	var down time.Time
	var dropped bool
	poll := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for ctx.Err() == nil {
		n, err := unix.Poll(poll, 500)
		if err == unix.EINTR || n == 0 {
			continue
		}
		if err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return
		}
		n, err = unix.Read(int(f.Fd()), event)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil || n != len(event) {
			return
		}
		t, k, v := binary.NativeEndian.Uint16(tail), binary.NativeEndian.Uint16(tail[2:]), binary.NativeEndian.Uint32(tail[4:])
		if t == 0 && k == 3 {
			down = time.Time{}
			dropped = true
			continue
		}
		if dropped {
			if t == 0 && k == 0 {
				dropped = false
			}
			continue
		}
		if t != 1 || k != 0x198 {
			continue
		}
		var stamp unix.Timeval
		if err := binary.Read(bytes.NewReader(event[:len(event)-8]), binary.NativeEndian, &stamp); err != nil {
			return
		}
		now := time.Unix(int64(stamp.Sec), int64(stamp.Usec)*1000)
		if v == 1 {
			down = now
		}
		if v == 0 && !down.IsZero() {
			if duration := now.Sub(down); duration >= 20*time.Millisecond && duration < 900*time.Millisecond {
				c.tap(ctx, now)
			}
			down = time.Time{}
		}
	}
}
