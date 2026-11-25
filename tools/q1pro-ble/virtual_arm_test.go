//go:build linux && arm

package q1ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jetkvm/kvm/internal/q1wifi"
	"golang.org/x/sys/unix"
)

func TestVirtualBluetoothToWPA2(t *testing.T) {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	if !strings.Contains(" "+strings.TrimSpace(string(cmdline))+" ", " q1-ble-guest=1 ") {
		t.Skip("disposable ARM VM only")
	}
	compatible, _ := os.ReadFile("/proc/device-tree/compatible")
	if !bytes.Contains(compatible, []byte("linux,dummy-virt")) {
		t.Fatal("expected QEMU virt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 105*time.Second)
	defer cancel()
	wait := func(label string, until time.Duration, predicate func() bool) {
		t.Helper()
		start := time.Now()
		for time.Since(start) < until && ctx.Err() == nil {
			if predicate() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", label)
	}
	connect := func() *dbus.Conn {
		t.Helper()
		var conn *dbus.Conn
		wait("private bus", 10*time.Second, func() bool { var err error; conn, err = dbus.Connect(busAddress); return err == nil })
		return conn
	}
	peripheral, central := connect(), connect()
	defer peripheral.Close()
	defer central.Close()
	getTree := func() objects {
		var tree objects
		if err := central.Object(bluez, "/").CallWithContext(ctx, manager+".GetManagedObjects", 0).Store(&tree); err != nil {
			return nil
		}
		return tree
	}
	// Verify the public Secure Connections exchange in this VM. Never record
	// payloads, key material, addresses or pairing codes.
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.BTPROTO_HCI)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrHCI{Dev: 0xffff, Channel: 2}); err != nil {
		t.Fatal(err)
	}
	var dhCheck atomic.Bool
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	defer func() { close(stopMonitor); <-monitorDone }()
	go func() {
		defer close(monitorDone)
		buf := make([]byte, 2048)
		for {
			select {
			case <-stopMonitor:
				return
			default:
			}
			n, e := unix.Read(fd, buf)
			if e != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if n >= 15 && binary.LittleEndian.Uint16(buf) == 4 && binary.LittleEndian.Uint16(buf[12:]) == 6 && buf[14] == 0x0d {
				dhCheck.Store(true)
			}
		}
	}()
	const a0 = dbus.ObjectPath("/org/bluez/hci0")
	const a1 = dbus.ObjectPath("/org/bluez/hci1")
	wait("two virtual BLE controllers", 12*time.Second, func() bool { tr := getTree(); _, p := tr[a0][advertising]; _, c := tr[a1][advertising]; return p && c })
	call := func(p dbus.ObjectPath, name string, args ...any) error {
		return central.Object(bluez, p).CallWithContext(ctx, name, 0, args...).Err
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	conf := "/run/wifi.conf"
	station := q1wifi.New("/run/wifictrl/wlan0", conf)
	s := newSession(ctx, func(ctx context.Context, ssid, password string) (string, error) {
		err := station.Provision(ssid, password, func() error {
			iface, err := net.InterfaceByName("wlan0")
			if err != nil {
				return err
			}
			addrs, err := iface.Addrs()
			if err != nil {
				return err
			}
			for _, v := range addrs {
				if v.String() == "192.0.2.2/24" {
					return nil
				}
			}
			return errors.New("simulated station has no local address")
		})
		return "http://192.0.2.2/", err
	})
	server := &gatt{conn: peripheral, adapter: a0, s: s, chars: map[int]*characteristic{}}
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx) }()
	check(call(a1, props+".Set", "org.bluez.Adapter1", "Powered", dbus.MakeVariant(true)))
	check(call(a1, "org.bluez.Adapter1.SetDiscoveryFilter", values{"Transport": dbus.MakeVariant("le"), "UUIDs": dbus.MakeVariant([]string{uuid(0)})}))
	check(call(a1, "org.bluez.Adapter1.StartDiscovery"))
	var device dbus.ObjectPath
	wait("Improv advertisement", 15*time.Second, func() bool {
		select {
		case err := <-done:
			t.Fatalf("server stopped before advertising: %v", err)
		default:
		}
		for p, ifs := range getTree() {
			if !strings.HasPrefix(string(p), string(a1)+"/dev_") {
				continue
			}
			d := ifs["org.bluez.Device1"]
			uuids, _ := d["UUIDs"].Value().([]string)
			for _, u := range uuids {
				if u == uuid(0) {
					serviceData, _ := d["ServiceData"].Value().(map[string]dbus.Variant)
					payload, _ := serviceData["00004677-0000-1000-8000-00805f9b34fb"].Value().([]byte)
					name, _ := d["Name"].Value().(string)
					if name != "JetKVM Q1 Pro" || !bytes.Equal(payload, []byte{1, 0, 0, 0, 0, 0}) {
						continue
					}
					device = p
					return true
				}
			}
		}
		return false
	})
	check(call(a1, "org.bluez.Adapter1.StopDiscovery"))
	check(call(device, "org.bluez.Device1.Connect"))
	chars := map[int]dbus.ObjectPath{}
	wait("all five ATT characteristics at remote central", 20*time.Second, func() bool {
		for p, ifs := range getTree() {
			if !strings.HasPrefix(string(p), string(device)+"/") {
				continue
			}
			v, _ := ifs[gattChar]["UUID"].Value().(string)
			for i := 1; i <= 5; i++ {
				if v == uuid(i) {
					chars[i] = p
				}
			}
		}
		return len(chars) == 5
	})
	remoteRead := func(id int) []byte {
		t.Helper()
		var b []byte
		check(central.Object(bluez, chars[id]).CallWithContext(ctx, gattChar+".ReadValue", 0, values{}).Store(&b))
		return b
	}
	if !bytes.Equal(remoteRead(1), []byte{1}) || !bytes.Equal(remoteRead(5), []byte{0}) {
		t.Fatal("incorrect remote initial state/capabilities")
	}
	for _, id := range []int{1, 2, 4} {
		check(call(chars[id], gattChar+".StartNotify"))
	}
	command := chars[3]
	// The service gets the central's identity from BlueZ; no identity is injected into the server.
	wait("single physical-authorization candidate", 5*time.Second, s.authorize)
	// The central's outgoing Pair has its own agent. The peripheral retains the production agent.
	check(central.Export(virtualAgent{device}, "/phone", "org.bluez.Agent1"))
	check(call("/org/bluez", agentManager+".RegisterAgent", dbus.ObjectPath("/phone"), "DisplayYesNo"))
	pairResult := make(chan error, 1)
	go func() { pairResult <- call(device, "org.bluez.Device1.Pair") }()
	wait("phone and Q1 pairing confirmation", 7*time.Second, func() bool { return s.read().confirming })
	if !s.authorize() {
		t.Fatal("physical confirmation did not approve the selected phone")
	}
	check(<-pairResult)
	wait("LE Secure Connections DHKey check", time.Second, dhCheck.Load)
	if !bytes.Equal(remoteRead(1), []byte{2}) {
		t.Fatal("authorized state not readable over ATT")
	}
	t.Log("virtual phone discovered service, physical confirmation authorized its BLE peer and pairing completed")
	write := func(data []byte) {
		for len(data) > 0 {
			n := min(20, len(data))
			check(call(command, gattChar+".WriteValue", data[:n], values{"type": dbus.MakeVariant("request")}))
			data = data[n:]
		}
	}
	previousConnected := func() bool {
		settings, err := station.Settings()
		if err != nil || !settings.Connected {
			return false
		}
		for _, p := range settings.Networks {
			if p.Connected && p.SSID == "q1-virtual-network" {
				return true
			}
		}
		return false
	}
	wait("previous saved WPA2 profile connected", 12*time.Second, previousConnected)
	original, err := os.ReadFile(conf)
	check(err)
	write(wifiPacket("q1-virtual-network", "incorrect-simulation-password"))
	wait("rejected WPA2 and rollback", 38*time.Second, func() bool { return s.read().code == 3 })
	if !bytes.Equal(remoteRead(2), []byte{3}) {
		t.Fatal("WPA2 failure not readable at central")
	}
	after, err := os.ReadFile(conf)
	check(err)
	if !bytes.Equal(after, original) {
		t.Fatal("failed credentials changed saved profiles")
	}
	wait("previous working WPA2 profile re-associated after rollback", 10*time.Second, previousConnected)
	t.Log("fragmented secure GATT write reached real supplicant; wrong WPA2 password rolled back and the previous Wi-Fi reconnected")
	write(wifiPacket("q1-virtual-network", "only-for-this-radio-fixture"))
	wait("associated virtual Wi-Fi station", 37*time.Second, func() bool { return s.read().state == 4 })
	if !bytes.Equal(remoteRead(1), []byte{4}) {
		t.Fatal("success state not readable at central")
	}
	result := remoteRead(4)
	if !bytes.Contains(result, []byte("http://192.0.2.2/")) || bytes.Contains(result, []byte("fixture")) {
		t.Fatal("invalid provisioning response")
	}
	state, err := station.Settings()
	check(err)
	if !state.Connected {
		t.Fatal("WPA2 station never connected")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Log("actual encrypted BLE writes and successful WPA2 association on ARM, address returned, window closed")
}

type virtualAgent struct{ peer dbus.ObjectPath }

func (v virtualAgent) RequestAuthorization(p dbus.ObjectPath) *dbus.Error {
	if p != v.peer {
		return dbError("Rejected")
	}
	return nil
}
func (v virtualAgent) RequestConfirmation(p dbus.ObjectPath, n uint32) *dbus.Error {
	return v.RequestAuthorization(p)
}
func (v virtualAgent) AuthorizeService(p dbus.ObjectPath, u string) *dbus.Error {
	return v.RequestAuthorization(p)
}
func (virtualAgent) Cancel() *dbus.Error  { return nil }
func (virtualAgent) Release() *dbus.Error { return nil }
