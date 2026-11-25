package link

import (
	"fmt"
	"net"

	"github.com/jetkvm/kvm/internal/network/types"
	"github.com/vishvananda/netlink"
)

// ReconcileSourceRoutes keeps replies on the interface whose local IPv4 address
// the peer connected to, even when a different default route has a lower metric.
// The caller owns the entire table and its source rules.
func ReconcileSourceRoutes(index, table int, addresses []types.IPAddress) error {
	routes := make(map[string]netlink.Route)
	rules := make(map[string]*netlink.Rule)
	for _, address := range addresses {
		ip := address.Address.IP.To4()
		if ip == nil {
			continue
		}
		prefix := &net.IPNet{IP: ip.Mask(address.Address.Mask), Mask: address.Address.Mask}
		routes[prefix.String()] = netlink.Route{LinkIndex: index, Scope: netlink.SCOPE_LINK, Table: table, Dst: prefix, Protocol: StaticProtocol}
		if gateway := address.Gateway.To4(); gateway != nil {
			routes["default"] = netlink.Route{LinkIndex: index, Scope: netlink.SCOPE_UNIVERSE, Table: table, Gw: gateway, Protocol: StaticProtocol}
		}
		rule := netlink.NewRule()
		rule.Family, rule.Priority, rule.Table = AfInet, 10000+table, table
		rule.Src = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		rules["from "+rule.Src.String()] = rule
		// Connected routes auto-created for two links on the same LAN both
		// have metric zero. Prefer the wired table for new on-LAN traffic.
		lan := netlink.NewRule()
		lan.Family, lan.Priority, lan.Table, lan.Dst = AfInet, 20000+table, table, prefix
		rules["to "+prefix.String()] = lan
	}
	active, err := netlink.RuleList(AfInet)
	if err != nil {
		return err
	}
	for _, rule := range active {
		if rule.Table != table {
			continue
		}
		var key string
		switch {
		case rule.Priority == 10000+table && rule.Src != nil:
			key = "from " + rule.Src.String()
		case rule.Priority == 20000+table && rule.Dst != nil:
			key = "to " + rule.Dst.String()
		default:
			continue
		}
		if _, ok := rules[key]; ok {
			delete(rules, key)
			continue
		}
		if err := netlink.RuleDel(&rule); err != nil {
			return fmt.Errorf("remove source rule: %w", err)
		}
	}

	existing, err := netlink.RouteListFiltered(AfInet, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return err
	}
	for _, route := range existing {
		key := "default"
		if route.Dst != nil {
			ones, _ := route.Dst.Mask.Size()
			if ones != 0 {
				key = route.Dst.String()
			}
		}
		if _, ok := routes[key]; !ok {
			if err := netlink.RouteDel(&route); err != nil {
				return fmt.Errorf("remove stale source route: %w", err)
			}
		}
	}
	// Install connected networks before their gateways.
	for key, route := range routes {
		if key == "default" {
			continue
		}
		if err := netlink.RouteReplace(&route); err != nil {
			return err
		}
	}
	if route, ok := routes["default"]; ok {
		if err := netlink.RouteReplace(&route); err != nil {
			return err
		}
	}
	for _, rule := range rules {
		if err := netlink.RuleAdd(rule); err != nil {
			return fmt.Errorf("add source rule: %w", err)
		}
	}
	return nil
}
