package nmlite

import (
	"github.com/jetkvm/kvm/internal/network/types"
	"net"
)

func (nm *NetworkManager) IsOnline() bool {
	for _, iface := range nm.orderedInterfaces() {
		if iface.IsOnline() {
			return true
		}
	}
	return false
}

func (nm *NetworkManager) IsUp() bool {
	for _, iface := range nm.orderedInterfaces() {
		if iface.IsUp() {
			return true
		}
	}
	return false
}

func (nm *NetworkManager) Hostname() string {
	return nm.resolvConf.Hostname()
}

func (nm *NetworkManager) FQDN() string {
	return nm.resolvConf.FQDN()
}

func (nm *NetworkManager) NTPServers() []net.IP {
	servers := []net.IP{}
	for _, iface := range nm.orderedInterfaces() {
		servers = append(servers, iface.NTPServers()...)
	}
	return servers
}

func (nm *NetworkManager) NTPServerStrings() []string {
	servers := []string{}
	for _, server := range nm.NTPServers() {
		servers = append(servers, server.String())
	}
	return servers
}

func (nm *NetworkManager) GetIPv4Addresses() []string {
	addresses := []string{}
	for _, iface := range nm.orderedInterfaces() {
		addresses = append(addresses, iface.GetIPv4Addresses()...)
	}
	return addresses
}

func (nm *NetworkManager) GetIPv4Address() string {
	for _, iface := range nm.orderedInterfaces() {
		if value := iface.GetIPv4Address(); value != "" {
			return value
		}
	}
	return ""
}

func (nm *NetworkManager) GetIPv6Address() string {
	for _, iface := range nm.orderedInterfaces() {
		if value := iface.GetIPv6Address(); value != "" {
			return value
		}
	}
	return ""
}

func (nm *NetworkManager) GetIPv6Addresses() []string {
	addresses := []string{}
	for _, iface := range nm.orderedInterfaces() {
		addresses = append(addresses, iface.GetIPv6Addresses()...)
	}
	return addresses
}

func (nm *NetworkManager) GetMACAddress() string {
	for _, iface := range nm.orderedInterfaces() {
		if mac := iface.GetMACAddress(); mac != "" {
			return mac
		}
	}
	return ""
}

func (nm *NetworkManager) IPv4Ready() bool {
	for _, iface := range nm.orderedInterfaces() {
		if iface.IPv4Ready() {
			return true
		}
	}
	return false
}

func (nm *NetworkManager) IPv6Ready() bool {
	for _, iface := range nm.orderedInterfaces() {
		if iface.IPv6Ready() {
			return true
		}
	}
	return false
}

func (nm *NetworkManager) IPv4String() string {
	return nm.GetIPv4Address()
}

func (nm *NetworkManager) IPv6String() string {
	return nm.GetIPv6Address()
}

func (nm *NetworkManager) MACString() string {
	return nm.GetMACAddress()
}

// PrimaryState prefers an addressed interface, then carrier, then configured order.
func (nm *NetworkManager) PrimaryState() *types.InterfaceState {
	var best *types.InterfaceState
	for _, im := range nm.orderedInterfaces() {
		state := im.GetState()
		state.Hostname = nm.Hostname()
		if state.Online {
			return state
		}
		if best == nil || (state.Up && !best.Up) {
			best = state
		}
	}
	if best == nil {
		return &types.InterfaceState{}
	}
	return best
}
