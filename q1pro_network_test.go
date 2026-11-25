package kvm

import (
	"testing"

	"github.com/guregu/null/v6"
)

func TestQ1ProRequiresBuiltInDHCP(t *testing.T) {
	useQ1Pro(t)
	settings := rpcGetNetworkSettings()
	if !settings.FixedDHCPClient || settings.DHCPClient.String != "jetdhcpc" {
		t.Fatalf("unexpected DHCP settings: %+v", settings)
	}
	settings.DHCPClient = null.StringFrom("udhcpc")
	if _, err := rpcSetNetworkSettings(*settings); err == nil {
		t.Fatal("selected unsupported DHCP client")
	}
	if err := rpcToggleDHCPClient(); err == nil {
		t.Fatal("legacy toggle selected unsupported DHCP client")
	}
	if config.NetworkConfig.DHCPClient.String != "jetdhcpc" {
		t.Fatal("rejected RPC changed the active config")
	}
}
