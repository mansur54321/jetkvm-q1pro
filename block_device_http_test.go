package kvm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/pojntfx/go-nbd/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteISOReadAtChecksRangeAfterMount(t *testing.T) {
	const size = int64(5<<30) + 4096
	first, last := size-2048, size-1
	good := fmt.Sprintf("bytes %d-%d/%d", first, last, size)
	for _, tc := range []struct {
		name         string
		status       int
		contentRange string
		data         []byte
		ok           bool
	}{
		{"last sector", 206, good, bytes.Repeat([]byte{0xa3}, 2048), true},
		{"ignored range or changed ETag", 200, "", bytes.Repeat([]byte{1}, 2048), false},
		{"wrong offset", 206, fmt.Sprintf("bytes 0-2047/%d", size), bytes.Repeat([]byte{1}, 2048), false},
		{"resource changed size", 206, fmt.Sprintf("bytes %d-%d/%d", first, last, size+2048), bytes.Repeat([]byte{1}, 2048), false},
		{"short body", 206, good, []byte{1, 2, 3}, false},
		{"HTTP error page", 503, "", bytes.Repeat([]byte{1}, 2048), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-%d", first, last) || r.Header.Get("If-Range") != "\"stable\"" {
					t.Errorf("unexpected range headers")
				}
				w.Header().Set("Content-Range", tc.contentRange)
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.data)
			}))
			defer s.Close()
			buf := make([]byte, 2048)
			n, err := (remoteHTTPImage{url: s.URL, size: size, validator: "\"stable\""}).ReadAt(buf, first)
			if (err == nil) != tc.ok || (tc.ok && (n != len(buf) || !bytes.Equal(buf, tc.data))) {
				t.Fatalf("n=%d err=%v", n, err)
			}
		})
	}
}

func TestRemoteISORejectsChangedRepresentation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-0/4096")
		w.Header().Set("ETag", "\"changed\"")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{1})
	}))
	defer s.Close()
	if _, err := (remoteHTTPImage{url: s.URL, size: 4096, validator: "\"original\""}).ReadAt(make([]byte, 1), 0); err == nil {
		t.Fatal("server changed ETag during an attached image")
	}
}

func TestRemoteNBDReadErrorLeavesNextRequestUsable(t *testing.T) {
	server, kernel := net.Pipe()
	defer kernel.Close()
	_ = kernel.SetDeadline(time.Now().Add(time.Second))
	finished := make(chan error, 1)
	go func() {
		defer server.Close()
		// The second sector is advertised but deliberately absent in the backing reader.
		finished <- serveRemoteImage(server, remoteImageBackend{ReaderAt: bytes.NewReader([]byte("valid")), size: 10})
	}()
	request := func(handle, off uint64, wantError uint32) {
		t.Helper()
		req := protocol.TransmissionRequestHeader{RequestMagic: protocol.TRANSMISSION_MAGIC_REQUEST, Type: protocol.TRANSMISSION_TYPE_REQUEST_READ, Handle: handle, Offset: off, Length: 5}
		if err := binary.Write(kernel, binary.BigEndian, req); err != nil {
			t.Fatal(err)
		}
		var reply protocol.TransmissionReplyHeader
		if err := binary.Read(kernel, binary.BigEndian, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ReplyMagic != protocol.TRANSMISSION_MAGIC_REPLY || reply.Handle != handle || reply.Error != wantError {
			t.Fatalf("invalid NBD reply: %+v", reply)
		}
	}
	request(1, 5, uint32(syscall.EIO))
	request(2, 0, 0)
	data := make([]byte, 5)
	if _, err := io.ReadFull(kernel, data); err != nil || string(data) != "valid" {
		t.Fatalf("%q, %v", data, err)
	}
	if err := binary.Write(kernel, binary.BigEndian, protocol.TransmissionRequestHeader{RequestMagic: protocol.TRANSMISSION_MAGIC_REQUEST, Type: protocol.TRANSMISSION_TYPE_REQUEST_DISC}); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
