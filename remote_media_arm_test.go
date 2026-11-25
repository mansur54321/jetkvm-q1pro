//go:build linux && arm && q1medialab

package kvm

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/jetkvm/kvm/internal/osservices"
	"github.com/jetkvm/kvm/internal/osservices/ffs"
	"github.com/jetkvm/kvm/internal/usbgadget"
	"golang.org/x/sys/unix"
)

type mediaSCSIHeader struct {
	Interface, Direction              int32
	CmdLen, SenseLen                  byte
	IOVec                             uint16
	Length                            uint32
	Data, Command, Sense              uintptr
	Timeout, Flags                    uint32
	PackID                            int32
	User                              uintptr
	Status, Masked, Msg, SenseWritten byte
	Host, Driver                      uint16
	Residual                          int32
	Duration, Info                    uint32
}

func mediaSCSI(fd *os.File, command, data []byte) error {
	var sense [64]byte
	h := mediaSCSIHeader{Interface: 'S', Direction: -3, CmdLen: byte(len(command)), SenseLen: byte(len(sense)), Length: uint32(len(data)), Timeout: 12000,
		Data: uintptr(unsafe.Pointer(&data[0])), Command: uintptr(unsafe.Pointer(&command[0])), Sense: uintptr(unsafe.Pointer(&sense[0]))}
	_, _, err := unix.Syscall(unix.SYS_IOCTL, fd.Fd(), 0x2285, uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(command)
	runtime.KeepAlive(data)
	runtime.KeepAlive(sense)
	if err != 0 {
		return err
	}
	if h.Status != 0 || h.Host != 0 || h.Driver != 0 || h.Residual != 0 {
		return fmt.Errorf("SCSI status=%x host=%x driver=%x residual=%d sense=%x", h.Status, h.Host, h.Driver, h.Residual, sense[:h.SenseWritten])
	}
	return nil
}

// Linux USBDEVFS_CONTROL from the host side of dummy_hcd.
func hostOSServicesRequest() ([]byte, error) {
	interfaces, _ := filepath.Glob("/sys/bus/usb/devices/*:*/bInterfaceSubClass")
	for _, subclass := range interfaces {
		value, _ := os.ReadFile(subclass)
		if strings.TrimSpace(string(value)) != "4a" {
			continue
		}
		dir := filepath.Dir(subclass)
		physical, _ := filepath.EvalSymlinks(dir)
		if !strings.Contains(physical, "dummy_hcd") {
			continue
		}
		parent := filepath.Dir(physical)
		number := func(f string, base int) (int, error) {
			b, e := os.ReadFile(f)
			if e != nil {
				return 0, e
			}
			n, e := strconv.ParseInt(strings.TrimSpace(string(b)), base, 32)
			return int(n), e
		}
		bus, err := number(filepath.Join(parent, "busnum"), 10)
		if err != nil {
			return nil, err
		}
		dev, err := number(filepath.Join(parent, "devnum"), 10)
		if err != nil {
			return nil, err
		}
		idx, err := number(filepath.Join(physical, "bInterfaceNumber"), 16)
		if err != nil {
			return nil, err
		}
		fd, err := os.OpenFile(fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev), os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		defer fd.Close()
		data := make([]byte, osservices.USBStatusLen)
		req := struct {
			Kind, Request        byte
			Value, Index, Length uint16
			Timeout              uint32
			Data                 uintptr
		}{
			0xc1, osservices.USBRequestStatus, osservices.ProtocolMajor, uint16(idx), uint16(len(data)), 1000, uintptr(unsafe.Pointer(&data[0])),
		}
		const usbControl = 0xC0005500 | (unsafe.Sizeof(req) << 16)
		n, _, errno := unix.Syscall(unix.SYS_IOCTL, fd.Fd(), usbControl, uintptr(unsafe.Pointer(&req)))
		runtime.KeepAlive(data)
		if errno != 0 {
			return nil, errno
		}
		if n != uintptr(len(data)) {
			return nil, fmt.Errorf("USB status length %d", n)
		}
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestQ1RemoteISOOverUSB(t *testing.T) {
	c, _ := os.ReadFile("/proc/cmdline")
	model, _ := os.ReadFile("/proc/device-tree/model")
	if !bytes.Contains(c, []byte("q1media_disposable=1")) || strings.TrimRight(string(model), "\x00") != "linux,dummy-virt" {
		t.Skip("isolated BSP QEMU only")
	}
	useQ1Pro(t)
	const configfs = "/sys/kernel/config/usb_gadget"
	if err := q1FunctionFSAvailable(configfs); err != nil {
		t.Fatal("FunctionFS probe", err)
	}
	ffsGadget := configfs + "/test-ffs"
	inst := ffsGadget + "/functions/ffs.media_check"
	if err := os.Mkdir(ffsGadget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(inst, 0755); err != nil {
		t.Fatal(err)
	}
	mnt := t.TempDir()
	if err := ffs.Mount(mnt, "media_check"); err != nil {
		t.Fatal(err)
	}
	f, err := ffs.Open(mnt, false)
	if err != nil {
		t.Fatal("real OS Services descriptors", err)
	}
	f.Close()
	if err := unix.Unmount(mnt, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(inst); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ffsGadget); err != nil {
		t.Fatal(err)
	}
	t.Log("Q1_MEDIA FunctionFS accepts real OS Services descriptors")

	caps, err := rpcGetDeviceCapabilities()
	if err != nil || slices.Contains(caps, "storage_download_only") {
		t.Fatal(caps, err)
	}
	config.OSServices = defaultOSServicesConfig()
	config.OSServices.Enabled = true
	initOSServices()
	if osServices == nil {
		t.Fatal("OS Services failed to initialize")
	}
	gadget = usbgadget.NewUsbGadgetForUDC("q1-media", "/sys/bus/platform/devices/dummy_udc.0", &usbgadget.Devices{MassStorage: true}, nil, usbLogger)
	udcs, _ := filepath.Glob("/sys/class/udc/*")
	attached, err := gadget.AttachRegisteredUDC()
	if err != nil || !attached {
		t.Fatalf("test UDC unavailable: controllers=%v attached=%t err=%v", udcs, attached, err)
	}
	const size = int64(5<<30) + 2048*32
	const sector = 2048
	const last = size/sector - 1
	if _, err := rpcSetOsServicesSettings(map[string]any{"enabled": true}); err != nil {
		t.Fatal("enable OS Services over genuine gadget", err)
	}
	var status []byte
	var statusErr error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		status, statusErr = hostOSServicesRequest()
		if statusErr == nil && bytes.HasPrefix(status, []byte("JKV5")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.HasPrefix(status, []byte("JKV5")) {
		t.Fatalf("host OS Services STATUS failed: %v", statusErr)
	}
	t.Logf("Q1_MEDIA genuine USB host OS Services STATUS returned JKV5 (%d bytes)", len(status))
	var offline atomic.Bool
	var mu sync.Mutex
	var transferred int64
	var requests int
	seen := map[int64]bool{}
	pattern := func(off int64) byte { return byte((off>>32)*47 + (off>>11)*31 + (off & 255)) }
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var first, end int64
		h := r.Header.Get("Range")
		if _, err := fmt.Sscanf(h, "bytes=%d-%d", &first, &end); err != nil || end < first || first < 0 || end >= size || end-first > 512<<10 {
			http.Error(w, "require bounded valid byte range", http.StatusBadRequest)
			return
		}
		if offline.Load() {
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		buf := make([]byte, end-first+1)
		for i := range buf {
			buf[i] = pattern(first + int64(i))
		}
		mu.Lock()
		transferred += int64(len(buf))
		requests++
		for _, off := range []int64{0, (4 << 30) + sector*17, last * sector} {
			if first <= off && off <= end {
				seen[off] = true
			}
		}
		mu.Unlock()
		w.Header().Set("ETag", "\"fixed-iso\"")
		w.Header().Set("Content-Length", fmt.Sprint(len(buf)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, end, size))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(buf)
	}))
	defer origin.Close()
	// Trust the generated private test CA on the two stock HTTP clients inside this guest.
	pool := origin.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	prev := http.DefaultTransport
	tr := prev.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = tr
	defer func() { http.DefaultTransport = prev; tr.CloseIdleConnections() }()
	if err := rpcMountWithHTTP(origin.URL+"/large.iso", CDROM); err != nil {
		t.Fatal("real mount RPC", err)
	}
	defer rpcUnmountImage()
	var sg *os.File
	var capData [8]byte
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		candidates, _ := filepath.Glob("/sys/class/scsi_generic/sg*")
		for _, p := range candidates {
			actual, _ := filepath.EvalSymlinks(p)
			if !strings.Contains(actual, "dummy_hcd") {
				continue
			}
			fd, e := os.OpenFile("/dev/"+filepath.Base(p), os.O_RDWR|unix.O_NONBLOCK, 0)
			if e != nil {
				continue
			}
			cmd := make([]byte, 10)
			cmd[0] = 0x25
			if mediaSCSI(fd, cmd, capData[:]) == nil && binary.BigEndian.Uint32(capData[:4]) == uint32(last) {
				sg = fd
				break
			}
			fd.Close()
		}
		if sg != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sg == nil {
		t.Fatalf("host did not report ISO capacity: %x", capData)
	}
	defer sg.Close()
	if binary.BigEndian.Uint32(capData[4:]) != sector {
		t.Fatalf("wrong block size: %x", capData)
	}
	read := func(lba int64) ([]byte, error) {
		cmd := make([]byte, 10)
		cmd[0] = 0x28
		binary.BigEndian.PutUint32(cmd[2:], uint32(lba))
		binary.BigEndian.PutUint16(cmd[7:], 1)
		data := make([]byte, sector)
		return data, mediaSCSI(sg, cmd, data)
	}
	for _, lba := range []int64{(4<<30)/sector + 17, 0, last} {
		data, e := read(lba)
		if e != nil {
			t.Fatalf("READ(10) lba=%d: %v", lba, e)
		}
		for i, b := range data {
			if b != pattern(lba*sector+int64(i)) {
				t.Fatalf("wrong byte at %d", lba*sector+int64(i))
			}
		}
	}
	mu.Lock()
	ok := seen[0] && seen[(4<<30)+sector*17] && seen[last*sector]
	total, n := transferred, requests
	mu.Unlock()
	if !ok || total > 4<<20 {
		t.Fatalf("unexpected transfer: offsets present=%v total=%d", ok, total)
	}
	t.Logf("Q1_MEDIA TLS Range -> NBD -> USB host SCSI CD capacity=%d last_lba=%d block=%d, verified first/beyond-4GiB/last; fetched=%d requests=%d", size, last, sector, total, n)
	offline.Store(true)
	failedAt := time.Now()
	_, err = read(last - 30000)
	if err == nil || errors.Is(err, unix.ETIMEDOUT) {
		t.Fatalf("HTTP failure did not produce host I/O error: %v", err)
	}
	if time.Since(failedAt) > 8*time.Second {
		t.Fatalf("HTTP failure was not bounded: %v", time.Since(failedAt))
	}
	t.Logf("Q1_MEDIA HTTP 503 produces SCSI error after %v: %v", time.Since(failedAt), err)
	offline.Store(false)
	retryLBA := last - 32000
	data, e := read(retryLBA)
	if e != nil {
		t.Fatal("healthy origin did not recover over same USB device", e)
	}
	for i, v := range data {
		if v != pattern(retryLBA*sector+int64(i)) {
			t.Fatal("recovery payload differs")
		}
	}
	t.Log("Q1_MEDIA host read recovered on the same CD without reattaching")
}

// This case boots the separate reference BSP with libcomposite loaded and no
// FunctionFS module installed, as in the device software volume.
func TestQ1KernelWithoutRemoteMediaSupport(t *testing.T) {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	model, _ := os.ReadFile("/proc/device-tree/model")
	if !bytes.Contains(cmdline, []byte("q1media_without_ffs=1")) || strings.TrimRight(string(model), "\x00") != "linux,dummy-virt" {
		t.Skip("reserved for a dedicated no-FunctionFS QEMU guest")
	}
	if _, err := os.Stat("/sys/kernel/config/usb_gadget"); err != nil {
		t.Fatal("configfs gadget API must exist to exercise missing ffs function", err)
	}
	useQ1Pro(t)
	initOSServices()
	caps, err := rpcGetDeviceCapabilities()
	if err != nil || slices.Contains(caps, "os_services") || !slices.Contains(caps, "storage_download_only") {
		t.Fatal(caps, err)
	}
	if _, err := rpcSetOsServicesSettings(map[string]any{"enabled": true}); err == nil || (config.OSServices != nil && config.OSServices.Enabled) {
		t.Fatal("accepted unavailable USB service", err)
	}
	entries, err := os.ReadDir("/sys/kernel/config/usb_gadget")
	if err != nil || len(entries) != 0 {
		t.Fatal("probe leaked a gadget", entries, err)
	}
	t.Log("Q1_MEDIA stock-like kernel: no OS Services capability, RPC refused, storage remains download-only")
}
