//go:build linux

package q1ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCompletedADCClicksAndOwnerWindow(t *testing.T) {
	for _, tc := range []struct {
		name           string
		claimed, grant bool
		count          int
	}{
		{"new", false, false, 2}, {"owner handoff after restart", true, true, 2}, {"owner unavailable", true, false, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "handoff")
			c := New(path, func() bool { return tc.claimed }, nil)
			if tc.grant {
				if _, err := c.Handoff(true); err != nil {
					t.Fatal(err)
				}
				c = New(path, func() bool { return tc.claimed }, nil)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := make(chan *session, 1)
			c.radio = func(ctx context.Context, s *session) error { opened <- s; <-ctx.Done(); return nil }
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			if err := unix.SetNonblock(int(r.Fd()), true); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			go func() { c.readButton(ctx, r); close(stopped) }()
			send := func(at int64, code uint16, value int32) {
				t.Helper()
				var b bytes.Buffer
				stamp := unix.NsecToTimeval(at * int64(time.Millisecond))
				for _, field := range []any{stamp, uint16(1), code, value} {
					if err := binary.Write(&b, binary.NativeEndian, field); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := w.Write(b.Bytes()); err != nil {
					t.Fatal(err)
				}
			}
			// Buffered events still describe a long, ignored press by their kernel timestamps.
			send(1000, 0x198, 1)
			send(6000, 0x198, 0)
			send(6100, 0x074, 1)
			send(6190, 0x074, 0)
			for i := 0; i < tc.count-1; i++ {
				at := int64(7000 + 300*i)
				send(at, 0x198, 1)
				send(at+120, 0x198, 0)
			}
			select {
			case <-opened:
				t.Fatal("early activation")
			case <-time.After(70 * time.Millisecond):
			}
			at := int64(7000 + 300*(tc.count-1))
			send(at, 0x198, 1)
			send(at+120, 0x198, 0)
			var s *session
			select {
			case s = <-opened:
			case <-time.After(time.Second):
				t.Fatal("did not open after physical gesture")
			}
			if tc.grant && c.Status().HandoffUntil != 0 {
				t.Fatal("one-time grant survived use")
			}
			s.connected("phone", true)
			send(at+1200, 0x198, 1)
			send(at+1350, 0x198, 0)
			select {
			case <-s.updates:
			case <-time.After(time.Second):
				t.Fatal("confirmation not delivered")
			}
			if !s.mayPair("phone") {
				t.Fatal("confirmed peer could not pair")
			}
			cancel()
			w.Close()
			<-stopped
		})
	}
}
