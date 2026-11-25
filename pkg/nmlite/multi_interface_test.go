package nmlite

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jetkvm/kvm/internal/network/types"
	"github.com/jetkvm/kvm/pkg/nmlite/link"
	"github.com/rs/zerolog"
	"github.com/vishvananda/netlink"
)

func TestLeaseWithoutGateway(t *testing.T) {
	logger := zerolog.Nop()
	im := InterfaceManager{logger: &logger}
	lease := &types.DHCPLease{IPAddress: net.ParseIP("10.1.2.3"), Netmask: net.ParseIP("255.255.255.0")}
	addr := im.convertDHCPLeaseToIPv4Config(lease)
	if addr == nil || addr.Gateway != nil || addr.Address.String() != "10.1.2.3/24" {
		t.Fatalf("unexpected routerless lease: %+v", addr)
	}
	lease.Netmask = nil
	if addr := im.convertDHCPLeaseToIPv4Config(lease); addr != nil {
		t.Fatalf("unexpected mask: %+v", addr)
	}
}

func TestIsolatedDualInterface(t *testing.T) {
	if os.Getenv("JETKVM_ISOLATED_NETWORK") != "q1-test" {
		t.Skip("run via tools/q1pro-image/test-network.sh")
	}
	hostname, _ := os.Hostname()
	if hostname != "q1-test" {
		t.Fatal("private UTS namespace required")
	}
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v %s", args, err, out)
		}
	}
	waitFor := func(description string, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !f() {
			if time.Now().After(deadline) {
				t.Fatal(description)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	var events bytes.Buffer
	logger := zerolog.New(zerolog.SyncWriter(&events)).Level(zerolog.InfoLevel)
	defer func() {
		if t.Failed() {
			t.Log(events.String())
		}
	}()
	nm := NewNetworkManager(context.Background(), &logger)
	defer nm.Stop()
	// A callback exercises the same re-entrant reads as the RPC notification.
	nm.SetOnInterfaceStateChange(func(_ string, _ types.InterfaceState) { _ = nm.PrimaryState(); _ = nm.IPv4Ready() })
	wired := &types.NetworkConfig{DHCPClient: null.StringFrom("jetdhcpc"), IPv4Mode: null.StringFrom("static"), IPv4Static: &types.IPv4StaticConfig{
		Address: null.StringFrom("192.0.2.2"), Netmask: null.StringFrom("255.255.255.0"), Gateway: null.StringFrom("192.0.2.1")}}
	wireless := &types.NetworkConfig{DHCPClient: null.StringFrom("jetdhcpc")}
	if err := nm.WatchInterface("eth0", &types.NetworkConfig{DHCPClient: null.StringFrom("jetdhcpc")}, 100); err != nil {
		t.Fatal(err)
	}
	if err := nm.WatchInterface("wlan0", wireless, 600); err != nil {
		t.Fatal(err)
	}
	// Drivers appear only after the app has already registered both interfaces.
	ip("link", "add", "wlan0", "type", "veth", "peer", "name", "ap0")
	ip("addr", "add", "198.51.100.1/24", "dev", "ap0")
	ip("link", "set", "ap0", "up")
	server := exec.Command("dnsmasq", "--no-daemon", "--port=0", "--bind-interfaces", "--log-facility=-", "--interface=ap0", "--no-ping", "--user=root", "--group=root", "--pid-file=/tmp/dhcp.pid", "--dhcp-leasefile=/tmp/dhcp.leases", "--dhcp-option=42,198.51.100.1", "--dhcp-range=198.51.100.10,198.51.100.10,255.255.255.0,1h")
	server.Stdout, server.Stderr = os.Stderr, os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	primary := func(iface, address string) bool {
		state := nm.PrimaryState()
		return state.InterfaceName == iface && state.IPv4Address == address && nm.IPv4Ready()
	}
	routes := func() map[string]int {
		t.Helper()
		result := map[string]int{}
		entries, err := link.GetNetlinkManager().ListDefaultRoutes(link.AfInet)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range entries {
			result[r.Gw.String()] = r.Priority
		}
		return result
	}
	waitFor("wireless DHCP acquisition without Ethernet", func() bool { return primary("wlan0", "198.51.100.10") && routes()["198.51.100.1"] == 600 })
	if servers := nm.NTPServerStrings(); len(servers) != 1 || servers[0] != "198.51.100.1" {
		t.Fatalf("DHCP NTP servers: %v", servers)
	}
	if err := nm.SetInterfaceConfig("eth0", wired); err != nil {
		t.Fatalf("staging Ethernet config from Wi-Fi: %v", err)
	}
	ip("link", "add", "eth0", "type", "veth", "peer", "name", "wired0")
	ip("addr", "add", "192.0.2.1/24", "dev", "wired0")
	ip("link", "set", "wired0", "up")
	waitFor("connected Ethernet elected primary while wireless remains up", func() bool { return primary("eth0", "192.0.2.2") && len(nm.GetIPv4Addresses()) == 2 })
	got := routes()
	if got["192.0.2.1"] != 100 || got["198.51.100.1"] != 600 {
		t.Fatalf("incorrect metrics: %v", got)
	}
	selected, err := netlink.RouteGet(net.ParseIP("203.0.113.1"))
	if err != nil || len(selected) != 1 || !selected[0].Gw.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("kernel route: %v %v", selected, err)
	}
	target := net.ParseIP("203.0.113.1")
	for source, gateway := range map[string]string{"192.0.2.2": "192.0.2.1", "198.51.100.10": "198.51.100.1"} {
		reply, err := netlink.RouteGetWithOptions(target, &netlink.RouteGetOptions{SrcAddr: net.ParseIP(source)})
		if err != nil || len(reply) != 1 || !reply[0].Gw.Equal(net.ParseIP(gateway)) {
			t.Fatalf("reply from %s: %v %v", source, reply, err)
		}
	}
	ip("link", "set", "wired0", "down")
	waitFor("Wi-Fi remains online after Ethernet loses carrier", func() bool { return primary("wlan0", "198.51.100.10") && len(nm.GetIPv4Addresses()) == 1 })
	ip("link", "set", "wired0", "up")
	waitFor("wired static configuration restored on reconnect", func() bool { return primary("eth0", "192.0.2.2") && len(nm.GetIPv4Addresses()) == 2 })
	ip("link", "set", "ap0", "down")
	waitFor("Ethernet survives wireless disassociation", func() bool { return primary("eth0", "192.0.2.2") && len(nm.GetIPv4Addresses()) == 1 })
	ip("link", "set", "ap0", "up")
	waitFor("Wi-Fi reacquires DHCP while Ethernet stays primary", func() bool {
		return primary("eth0", "192.0.2.2") && routes()["198.51.100.1"] == 600 && len(nm.GetIPv4Addresses()) == 2
	})
	// Both NICs can share one home router. Wi-Fi came up first, but brand-new
	// LAN traffic now goes over Ethernet and Wi-Fi replies still use Wi-Fi.
	common := *wired
	ipv4 := *wired.IPv4Static
	ipv4.Address, ipv4.Gateway = null.StringFrom("198.51.100.23"), null.StringFrom("198.51.100.1")
	common.IPv4Static = &ipv4
	if err := nm.SetInterfaceConfig("eth0", &common); err != nil {
		t.Fatal(err)
	}
	lan := net.ParseIP("198.51.100.123")
	selected, err = netlink.RouteGet(lan)
	if err != nil || len(selected) != 1 || !selected[0].Src.Equal(net.ParseIP("198.51.100.23")) {
		t.Fatalf("same-LAN route: %v %v", selected, err)
	}
	reply, err := netlink.RouteGetWithOptions(lan, &netlink.RouteGetOptions{SrcAddr: net.ParseIP("198.51.100.10")})
	wifiLink, _ := netlink.LinkByName("wlan0")
	if err != nil || len(reply) != 1 || reply[0].LinkIndex != wifiLink.Attrs().Index {
		t.Fatalf("same-LAN Wi-Fi reply: %v %v", reply, err)
	}
}
