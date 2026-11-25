package kvm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/pojntfx/go-nbd/pkg/protocol"
	"github.com/rs/zerolog"
)

type remoteImageBackend struct {
	io.ReaderAt
	size int64
}

const nbdDevicePath = "/dev/nbd0"

type NBDDevice struct {
	backend      remoteImageBackend
	serverConn   net.Conn
	kernelSocket *os.File
	dev          *os.File
	finished     chan struct{}
	l            *zerolog.Logger
}

func NewNBDDevice() *NBDDevice { return &NBDDevice{} }

func (d *NBDDevice) Start() error {
	if d.backend.ReaderAt == nil {
		state, _ := rpcGetVirtualMediaState()
		if state == nil || state.Source != HTTP {
			return errors.New("no remote image")
		}
		d.backend = remoteImageBackend{ReaderAt: remoteHTTPImage{url: state.URL, size: state.Size}, size: state.Size}
	}
	scopedLogger := nbdLogger.With().Str("device_path", nbdDevicePath).Logger()
	d.l = &scopedLogger
	dev, err := os.OpenFile(nbdDevicePath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open NBD block device: %w", err)
	}
	d.dev = dev
	if err := d.openConnections(); err != nil {
		_ = dev.Close()
		return err
	}
	go func() {
		defer d.serverConn.Close()
		err := serveRemoteImage(d.serverConn, d.backend)
		d.l.Debug().Err(err).Msg("NBD server stopped")
	}()
	return d.connect()
}

// This socket talks only to our kernel NBD client. The capacity and sector size
// are supplied by ioctl. The wire carries standard NBD transmission requests.
func serveRemoteImage(conn net.Conn, image remoteImageBackend) error {
	var buf []byte
	for {
		var req protocol.TransmissionRequestHeader
		if err := binary.Read(conn, binary.BigEndian, &req); err != nil {
			return err
		}
		if req.RequestMagic != protocol.TRANSMISSION_MAGIC_REQUEST || req.Length > 32<<20 {
			return errors.New("invalid kernel NBD request")
		}
		rep := protocol.TransmissionReplyHeader{ReplyMagic: protocol.TRANSMISSION_MAGIC_REPLY, Handle: req.Handle}
		var data []byte
		switch req.Type & 0xffff {
		case protocol.TRANSMISSION_TYPE_REQUEST_READ:
			if req.Offset > uint64(image.size) || uint64(req.Length) > uint64(image.size)-req.Offset {
				rep.Error = uint32(syscall.EINVAL)
				break
			}
			if cap(buf) < int(req.Length) {
				buf = make([]byte, req.Length)
			}
			buf = buf[:req.Length]
			n, err := image.ReadAt(buf, int64(req.Offset))
			if err != nil || n != len(buf) {
				rep.Error = uint32(syscall.EIO)
			} else {
				data = buf
			}
		case protocol.TRANSMISSION_TYPE_REQUEST_DISC:
			return nil
		case protocol.TRANSMISSION_TYPE_REQUEST_WRITE:
			if _, err := io.CopyN(io.Discard, conn, int64(req.Length)); err != nil {
				return err
			}
			rep.Error = uint32(syscall.EROFS)
		case 3: // NBD_CMD_FLUSH
			// A read-only image has no buffered writes.
		default:
			rep.Error = uint32(syscall.EINVAL)
		}
		if err := binary.Write(conn, binary.BigEndian, rep); err != nil {
			return err
		}
		if len(data) > 0 {
			n, err := conn.Write(data)
			if err != nil {
				return err
			}
			if n != len(data) {
				return io.ErrShortWrite
			}
		}
	}
}
