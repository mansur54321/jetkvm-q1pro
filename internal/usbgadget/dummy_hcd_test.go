//go:build linux && amd64

package usbgadget

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/imagedownload"

	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"
)

func TestDisposableDummyHCD(t *testing.T) {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	if !strings.Contains(" "+strings.TrimSpace(string(cmdline))+" ", " jetkvm.test=dummy_hcd ") {
		t.Skip("requires a disposable QEMU kernel")
	}
	const controller = "dummy_udc.0"
	logger := zerolog.New(os.Stderr).Level(zerolog.WarnLevel)
	g := NewUsbGadgetForUDC("q1-test", "/sys/bus/platform/devices/dummy_udc.0", nil, nil, &logger)
	poll := func(want bool) {
		t.Helper()
		got, err := g.AttachRegisteredUDC()
		if err != nil || got != want {
			t.Fatalf("attach registered UDC = %t, %v, want %t", got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(udcClassPath, controller)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected controller initially absent: %v", err)
	}
	poll(false)
	f, err := os.Open("/modules/dummy_hcd.ko")
	if err != nil {
		t.Fatal(err)
	}
	err = unix.FinitModule(int(f.Fd()), "", 0)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Hold the controller with an unrelated gadget while JetKVM polls.
	foreign := filepath.Join(gadgetPath, "foreign-test")
	for _, dir := range []string{foreign, foreign + "/configs/c.1", foreign + "/functions/mass_storage.foreign"} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(foreign+"/functions/mass_storage.foreign", foreign+"/configs/c.1/disk"); err != nil {
		t.Fatal(err)
	}
	bind := func(name string) {
		t.Helper()
		if err := os.WriteFile(foreign+"/UDC", []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	bind(controller)
	poll(false)
	if owner, err := os.ReadFile(filepath.Join(udcClassPath, controller, "function")); err != nil || strings.TrimSpace(string(owner)) != "foreign-test" {
		t.Fatalf("foreign gadget lost its UDC: %q, %v", owner, err)
	}
	if err := g.SetEmulationEnabled(false); err != nil {
		t.Fatal(err)
	}
	bind("\n")
	poll(false)
	if err := g.SetEmulationEnabled(true); err != nil {
		t.Fatal(err)
	}
	concurrent := make(chan error, 3)
	for range cap(concurrent) {
		go func() {
			ready, err := g.AttachRegisteredUDC()
			if err == nil && !ready {
				err = errors.New("free UDC did not attach")
			}
			concurrent <- err
		}()
	}
	for range cap(concurrent) {
		if err := <-concurrent; err != nil {
			t.Fatal(err)
		}
	}
	poll(true)
	t.Log("late UDC registration, foreign owner and disabled emulation passed")
	defer g.Close()
	if !g.IsGadgetAttachedToUDC() {
		t.Fatal("virtual UDC not bound")
	}

	wait := func(description string, fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for !fn() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", description)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	expected := map[string][]byte{"keyboard": keyboardReportDesc, "absolute": absoluteMouseCombinedReportDesc, "relative": relativeMouseCombinedReportDesc, "wake": wakeReportDesc}
	nodes := map[string]string{}
	wait("all four host-side HID devices", func() bool {
		paths, _ := filepath.Glob("/sys/class/hidraw/*/device")
		for _, path := range paths {
			d, err := os.ReadFile(filepath.Join(path, "report_descriptor"))
			if err != nil {
				continue
			}
			for name, desc := range expected {
				if bytes.Equal(d, desc) {
					nodes[name] = path
				}
			}
		}
		return len(nodes) == len(expected)
	})
	read := func(path string) string {
		t.Helper()
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(value))
	}
	host, err := filepath.EvalSymlinks(nodes["keyboard"])
	if err != nil {
		t.Fatal(err)
	}
	hostDevnum := filepath.Join(filepath.Dir(filepath.Dir(host)), "devnum")
	before := read(hostDevnum)
	restarted := NewUsbGadgetForUDC("q1-test", "/sys/bus/platform/devices/dummy_udc.0", nil, nil, &logger)
	if ready, err := restarted.AttachRegisteredUDC(); !ready || err != nil {
		t.Fatalf("adopt already active gadget: %t, %v", ready, err)
	}
	// Give the host time to process a disconnect and assign a new address.
	time.Sleep(300 * time.Millisecond)
	if after := read(hostDevnum); after != before {
		t.Fatalf("host re-enumerated on adoption: %s -> %s", before, after)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("matching gadget adopted with the same host USB device number")
	for name, path := range nodes {
		hid, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		iface := filepath.Dir(hid)
		expectedSize := map[string]int{"keyboard": 8, "relative": 5, "absolute": 6, "wake": 1}[name]
		protocols := map[string]string{"keyboard": "01/01", "relative": "01/02", "absolute": "00/02", "wake": "00/00"}
		if got := read(filepath.Join(iface, "bInterfaceSubClass")) + "/" + read(filepath.Join(iface, "bInterfaceProtocol")); got != protocols[name] {
			t.Fatalf("%s protocol: %s", name, got)
		}
		eps, _ := filepath.Glob(filepath.Join(iface, "ep_*"))
		var directions []string
		for _, ep := range eps {
			size, err := strconv.ParseInt(read(filepath.Join(ep, "wMaxPacketSize")), 16, 32)
			if err != nil || int(size) != expectedSize || read(filepath.Join(ep, "type")) != "Interrupt" {
				t.Fatalf("%s endpoint: %s, %d", name, ep, size)
			}
			directions = append(directions, read(filepath.Join(ep, "direction")))
		}
		sort.Strings(directions)
		want := "in"
		if name == "keyboard" {
			want = "in,out"
		}
		if got := strings.Join(directions, ","); got != want {
			t.Fatalf("%s directions: %s", name, got)
		}
	}
	event := func(name string) int {
		t.Helper()
		paths, _ := filepath.Glob(filepath.Join(nodes[name], "input/input*/event*"))
		if len(paths) != 1 {
			t.Fatalf("%s events: %v", name, paths)
		}
		fd, err := unix.Open("/dev/input/"+filepath.Base(paths[0]), unix.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Close(fd) })
		return fd
	}
	assertEvent := func(fd int, typ, code uint16, value int32) {
		t.Helper()
		buf := make([]byte, 24)
		wait(fmt.Sprintf("host input event %d/%d=%d", typ, code, value), func() bool {
			for {
				n, e := unix.Read(fd, buf)
				if e == unix.EAGAIN {
					return false
				}
				if e != nil || n != len(buf) {
					t.Fatalf("input_event %d: %v", n, e)
				}
				if binary.NativeEndian.Uint16(buf[16:]) == typ && binary.NativeEndian.Uint16(buf[18:]) == code && int32(binary.NativeEndian.Uint32(buf[20:])) == value {
					return true
				}
			}
		})
	}
	const leftShift, axisX, axisY = 42, 0, 1
	keyboard, relative, absolute := event("keyboard"), event("relative"), event("absolute")
	if err := g.OpenKeyboardHidFile(); err != nil {
		t.Fatal(err)
	}
	if err := g.KeyboardReport(2, nil); err != nil {
		t.Fatal(err)
	}
	assertEvent(keyboard, unix.EV_KEY, leftShift, 1)
	if err := g.KeyboardReport(0, nil); err != nil {
		t.Fatal(err)
	}
	assertEvent(keyboard, unix.EV_KEY, leftShift, 0)
	if err := g.RelMouseReport(7, -3, 0); err != nil {
		t.Fatal(err)
	}
	assertEvent(relative, unix.EV_REL, axisX, 7)
	assertEvent(relative, unix.EV_REL, axisY, -3)
	if err := g.AbsMouseReport(1111, 2222, 0); err != nil {
		t.Fatal(err)
	}
	assertEvent(absolute, unix.EV_ABS, axisX, 1111)
	assertEvent(absolute, unix.EV_ABS, axisY, 2222)
	t.Log("four host HID descriptors, Shift press/release, relative and absolute pointer events passed")

	contents := make([]byte, 2<<20)
	for sector := 0; sector < len(contents)/2048; sector++ {
		for i := 0; i < 2048; i += 4 {
			binary.LittleEndian.PutUint32(contents[sector*2048+i:], uint32(sector*2048+i))
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "media.iso", time.Time{}, bytes.NewReader(contents))
	}))
	defer server.Close()
	dir := t.TempDir()
	d := imagedownload.Downloader{Dir: dir, Client: func() *http.Client { return server.Client() }, FreeSpace: func() (int64, error) { return int64(len(contents)), nil }}
	if _, err := d.Start(server.URL+"/media.iso", ""); err != nil {
		t.Fatal(err)
	}
	d.Wait()
	if state := d.State(); state.Status != imagedownload.StatusDone || state.Received != int64(len(contents)) {
		t.Fatalf("download: %+v", state)
	}
	image := filepath.Join(dir, "media.iso")
	if err := g.SetMassStorageImage(image); err != nil {
		t.Fatal(err)
	}
	wait("SCSI removable drive with the attached image", func() bool {
		paths, _ := filepath.Glob("/sys/class/block/*/device")
		for _, path := range paths {
			phys, _ := filepath.EvalSymlinks(path)
			if !strings.Contains(phys, "dummy_hcd") {
				continue
			}
			block := filepath.Base(filepath.Dir(path))
			fd, err := os.Open("/dev/" + block)
			if err != nil {
				continue
			}
			actual := make([]byte, len(contents))
			n, err := fd.ReadAt(actual, 0)
			fd.Close()
			if err == nil && n == len(contents) && bytes.Equal(actual, contents) {
				return true
			}
		}
		return false
	})
	t.Logf("HTTP download to local storage; host SCSI read all %d bytes of the attached image", len(contents))
}
