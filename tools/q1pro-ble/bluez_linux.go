//go:build linux

package q1ble

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	bluez        = "org.bluez"
	busAddress   = "unix:path=/run/q1-ble/bus"
	root         = dbus.ObjectPath("/jetkvm/improv")
	service      = dbus.ObjectPath("/jetkvm/improv/service0")
	advert       = dbus.ObjectPath("/jetkvm/advertisement")
	agentPath    = dbus.ObjectPath("/jetkvm/pairing")
	manager      = "org.freedesktop.DBus.ObjectManager"
	props        = "org.freedesktop.DBus.Properties"
	gattService  = "org.bluez.GattService1"
	gattChar     = "org.bluez.GattCharacteristic1"
	advertising  = "org.bluez.LEAdvertisingManager1"
	advertIface  = "org.bluez.LEAdvertisement1"
	agentManager = "org.bluez.AgentManager1"
)

type values = map[string]dbus.Variant
type objects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func uuid(id int) string { return fmt.Sprintf("00467768-6228-2272-4663-27747826800%d", id) }

func dbError(code string) *dbus.Error { return dbus.NewError(bluez+".Error."+code, []any{code}) }

type properties struct {
	iface string
	all   func() values
}

func (p properties) GetAll(iface string) (values, *dbus.Error) {
	if iface != p.iface {
		return nil, dbError("InvalidArguments")
	}
	return p.all(), nil
}
func (p properties) Get(iface, key string) (dbus.Variant, *dbus.Error) {
	all, err := p.GetAll(iface)
	if err != nil {
		return dbus.Variant{}, err
	}
	if v, ok := all[key]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbError("InvalidArguments")
}
func (p properties) Set(iface, key string, value dbus.Variant) *dbus.Error {
	return dbError("NotPermitted")
}

type application struct {
	tree map[dbus.ObjectPath]properties
}

func (a *application) GetManagedObjects() (objects, *dbus.Error) {
	out := objects{}
	for path, p := range a.tree {
		out[path] = map[string]values{p.iface: p.all()}
	}
	return out, nil
}

type characteristic struct {
	s      *session
	id     int
	mu     sync.Mutex
	notify bool
}

func (c *characteristic) value(v snapshot) []byte {
	switch c.id {
	case 1:
		return []byte{v.state}
	case 2:
		return []byte{v.code}
	case 4:
		return append([]byte{}, v.result...)
	default:
		return []byte{0}
	}
}
func (c *characteristic) properties() values {
	flags := []string{"read", "notify"}
	switch c.id {
	case 3:
		flags = []string{"write", "secure-write"}
	case 5:
		flags = []string{"read"}
	}
	all := values{"UUID": dbus.MakeVariant(uuid(c.id)), "Service": dbus.MakeVariant(service), "Flags": dbus.MakeVariant(flags)}
	if c.id != 3 {
		all["Value"] = dbus.MakeVariant(c.value(c.s.read()))
	}
	if c.id == 1 || c.id == 2 || c.id == 4 {
		c.mu.Lock()
		all["Notifying"] = dbus.MakeVariant(c.notify)
		c.mu.Unlock()
	}
	return all
}
func (c *characteristic) ReadValue(options values) ([]byte, *dbus.Error) {
	if c.id == 3 {
		return nil, dbError("NotPermitted")
	}
	value := c.value(c.s.read())
	offset, _ := options["offset"].Value().(uint16)
	if int(offset) > len(value) {
		return nil, dbError("InvalidOffset")
	}
	return value[offset:], nil
}
func (c *characteristic) WriteValue(value []byte, options values) *dbus.Error {
	if c.id != 3 {
		return dbError("NotPermitted")
	}
	device, ok := options["device"].Value().(dbus.ObjectPath)
	if !ok || !c.s.mayPair(string(device)) {
		return dbError("NotAuthorized")
	}
	if p, _ := options["prepare-authorize"].Value().(bool); p {
		return nil
	} // BlueZ will deliver the committed value separately.
	offset, _ := options["offset"].Value().(uint16)
	if err := c.s.write(string(device), value, offset); err != nil {
		return dbError("Failed")
	}
	return nil
}
func (c *characteristic) StartNotify() *dbus.Error {
	if c.id != 1 && c.id != 2 && c.id != 4 {
		return dbError("NotSupported")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify = true
	return nil
}
func (c *characteristic) StopNotify() *dbus.Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify = false
	return nil
}

type advertisement struct{}

func (advertisement) Release() *dbus.Error { return nil }

type pairingAgent struct{ s *session }

func (a pairingAgent) RequestAuthorization(peer dbus.ObjectPath) *dbus.Error {
	if !a.s.mayPair(string(peer)) {
		return dbError("Rejected")
	}
	return nil
}
func (a pairingAgent) RequestConfirmation(peer dbus.ObjectPath, passkey uint32) *dbus.Error {
	if !a.s.confirm(string(peer), passkey) {
		return dbError("Rejected")
	}
	return nil
}
func (a pairingAgent) AuthorizeService(peer dbus.ObjectPath, service string) *dbus.Error {
	return a.RequestAuthorization(peer)
}
func (pairingAgent) Release() *dbus.Error  { return nil }
func (a pairingAgent) Cancel() *dbus.Error { a.s.cancelPair(); return nil }

type gatt struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	s       *session
	chars   map[int]*characteristic
}

func (g *gatt) export() error {
	app := &application{tree: map[dbus.ObjectPath]properties{}}
	app.tree[service] = properties{gattService, func() values {
		return values{"UUID": dbus.MakeVariant(uuid(0)), "Primary": dbus.MakeVariant(true), "Includes": dbus.MakeVariant([]dbus.ObjectPath{})}
	}}
	for i := 1; i <= 5; i++ {
		c := &characteristic{s: g.s, id: i}
		g.chars[i] = c
		path := dbus.ObjectPath(fmt.Sprintf("%s/char%d", service, i))
		app.tree[path] = properties{gattChar, c.properties}
		if err := g.conn.Export(c, path, gattChar); err != nil {
			return err
		}
	}
	for path, p := range app.tree {
		if err := g.conn.Export(p, path, props); err != nil {
			return err
		}
	}
	if err := g.conn.Export(app, root, manager); err != nil {
		return err
	}
	if err := g.conn.Export(pairingAgent{g.s}, agentPath, "org.bluez.Agent1"); err != nil {
		return err
	}
	if err := g.conn.Export(advertisement{}, advert, advertIface); err != nil {
		return err
	}
	return g.conn.Export(properties{advertIface, func() values {
		// Flags (3), 128-bit service UUID (18), 16-bit service data (10): 31 bytes.
		data := map[string]dbus.Variant{"00004677-0000-1000-8000-00805f9b34fb": dbus.MakeVariant([]byte{g.s.read().state, 0, 0, 0, 0, 0})}
		return values{"Type": dbus.MakeVariant("peripheral"), "LocalName": dbus.MakeVariant("JetKVM Q1 Pro"),
			"ServiceUUIDs": dbus.MakeVariant([]string{uuid(0)}), "ServiceData": dbus.MakeVariant(data)}
	}}, advert, props)
}

func (g *gatt) invoke(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error {
	call, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return g.conn.Object(bluez, path).CallWithContext(call, method, 0, args...).Err
}

func (g *gatt) notify(before, after snapshot) {
	for _, i := range []int{4, 2, 1} {
		c := g.chars[i]
		c.mu.Lock()
		notify := c.notify
		c.mu.Unlock()
		if !notify || string(c.value(before)) == string(c.value(after)) {
			continue
		}
		path := dbus.ObjectPath(fmt.Sprintf("%s/char%d", service, i))
		_ = g.conn.Emit(path, props+".PropertiesChanged", gattChar, values{"Value": dbus.MakeVariant(c.value(after))}, []string{})
	}
}

func (g *gatt) connection(sig *dbus.Signal) {
	p := sig.Path
	changed := values{}
	switch sig.Name {
	case props + ".PropertiesChanged":
		if len(sig.Body) != 3 || sig.Body[0] != "org.bluez.Device1" {
			return
		}
		changed, _ = sig.Body[1].(map[string]dbus.Variant)
	case manager + ".InterfacesAdded":
		if len(sig.Body) != 2 {
			return
		}
		p, _ = sig.Body[0].(dbus.ObjectPath)
		ifaces, _ := sig.Body[1].(map[string]map[string]dbus.Variant)
		changed = ifaces["org.bluez.Device1"]
	case manager + ".InterfacesRemoved":
		if len(sig.Body) != 2 {
			return
		}
		p, _ = sig.Body[0].(dbus.ObjectPath)
		interfaces, _ := sig.Body[1].([]string)
		for _, iface := range interfaces {
			if iface == "org.bluez.Device1" {
				g.s.connected(string(p), false)
			}
		}
		return
	}
	if !strings.HasPrefix(string(p), string(g.adapter)+"/dev_") {
		return
	}
	if connected, ok := changed["Connected"].Value().(bool); ok {
		g.s.connected(string(p), connected)
	}
}

func (g *gatt) serve(ctx context.Context) error {
	if err := g.export(); err != nil {
		return err
	}
	sigs := make(chan *dbus.Signal, 32)
	g.conn.Signal(sigs)
	defer g.conn.RemoveSignal(sigs)
	for _, iface := range []string{props, manager} {
		if err := g.conn.AddMatchSignal(dbus.WithMatchSender(bluez), dbus.WithMatchInterface(iface)); err != nil {
			return err
		}
	}
	set := func(key string, value any) error {
		return g.invoke(ctx, g.adapter, props+".Set", "org.bluez.Adapter1", key, dbus.MakeVariant(value))
	}
	if err := set("Powered", true); err != nil {
		return err
	}
	if err := g.invoke(ctx, "/org/bluez", agentManager+".RegisterAgent", agentPath, "DisplayYesNo"); err != nil {
		return err
	}
	if err := g.invoke(ctx, "/org/bluez", agentManager+".RequestDefaultAgent", agentPath); err != nil {
		return err
	}
	if err := set("Pairable", true); err != nil {
		return err
	}
	if err := g.invoke(ctx, g.adapter, "org.bluez.GattManager1.RegisterApplication", root, values{}); err != nil {
		return err
	}
	reg := func() error { return g.invoke(ctx, g.adapter, advertising+".RegisterAdvertisement", advert, values{}) }
	if err := reg(); err != nil {
		return err
	}
	marker, err := os.OpenFile("/run/q1-ble-state", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer marker.Close()
	if err = unix.Flock(int(marker.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	if err = marker.Truncate(0); err != nil {
		return err
	}
	last := g.s.read()
	if _, err = marker.WriteAt([]byte{last.state + '0', '\n'}, 0); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-g.conn.Context().Done():
			return errors.New("Bluetooth bus closed")
		case sig, ok := <-sigs:
			if !ok {
				return errors.New("Bluetooth signals closed")
			}
			g.connection(sig)
		case <-g.s.updates:
			next := g.s.read()
			indicator := next.state + '0'
			if next.confirming {
				indicator = 'P'
			}
			if _, err := marker.WriteAt([]byte{indicator, '\n'}, 0); err != nil {
				return err
			}
			g.notify(last, next)
			if last.state != next.state && next.state != provisioned {
				// BlueZ snapshots the advertisement's data at registration.
				if err := g.invoke(ctx, g.adapter, advertising+".UnregisterAdvertisement", advert); err == nil {
					_ = reg()
				}
			}
			last = next
		case <-g.s.finished:
			g.notify(last, g.s.read())
			waitResultRead(ctx)
			return nil
		}
	}
}

func findAdapter(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, error) {
	var tree objects
	err := conn.Object(bluez, "/").CallWithContext(ctx, manager+".GetManagedObjects", 0).Store(&tree)
	if err != nil {
		return "", err
	}
	var found dbus.ObjectPath
	for p, ifaces := range tree {
		_, g := ifaces["org.bluez.GattManager1"]
		_, a := ifaces[advertising]
		if g && a {
			if found != "" {
				return "", errors.New("multiple Bluetooth controllers")
			}
			found = p
		}
	}
	if found == "" {
		return "", errors.New("Bluetooth controller unavailable")
	}
	return found, nil
}

func runPeripheral(ctx context.Context, s *session) error {
	cmd := exec.Command("/usr/sbin/q1-ble-radio")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()
	radio, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	for radio.Err() == nil {
		select {
		case <-done:
			return errors.New("Bluetooth stack unavailable")
		case <-time.After(300 * time.Millisecond):
		case <-radio.Done():
			return radio.Err()
		}
		conn, err := dbus.Connect(busAddress, dbus.WithContext(ctx))
		if err != nil {
			continue
		}
		adapter, err := findAdapter(radio, conn)
		if err != nil {
			conn.Close()
			continue
		}
		defer conn.Close()
		return (&gatt{conn: conn, adapter: adapter, s: s, chars: map[int]*characteristic{}}).serve(ctx)
	}
	return radio.Err()
}
