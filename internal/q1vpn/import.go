package q1vpn

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Import reads a single-peer client configuration. No wg-quick shell commands run.
func (m *Manager) Import(profile string) error {
	cfg, err := parseProfile(profile)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apply(cfg)
}

func parseProfile(profile string) (saved, error) {
	var cfg saved
	if len(profile) > 16384 {
		return cfg, errors.New("WireGuard profile is too large")
	}
	var section string
	sections := map[string]bool{}
	fields := map[string]bool{}
	for _, line := range strings.Split(profile, "\n") {
		before, _, _ := strings.Cut(line, "#")
		line = strings.TrimSpace(before)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch line {
			case "[Interface]", "[Peer]":
				section = line
			default:
				return cfg, errors.New("unknown WireGuard section")
			}
			if sections[section] {
				return cfg, errors.New("expected a single-interface, single-peer WireGuard profile")
			}
			sections[section] = true
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		val = strings.TrimSpace(val)
		if !ok || val == "" || section == "" {
			return cfg, errors.New("invalid WireGuard field")
		}
		field := section + name
		if fields[field] && field != "[Peer]AllowedIPs" {
			return cfg, errors.New("duplicate WireGuard field")
		}
		fields[field] = true
		switch field {
		case "[Interface]Address":
			p, err := netip.ParsePrefix(val)
			if err != nil {
				return cfg, errors.New("one tunnel address with CIDR prefix is required")
			}
			cfg.Address = p.Addr().String()
		case "[Interface]PrivateKey":
			cfg.PrivateKey = val
		case "[Peer]PublicKey":
			cfg.PeerKey = val
		case "[Peer]PresharedKey":
			cfg.PresharedKey = val
		case "[Peer]Endpoint":
			cfg.Endpoint = val
		case "[Peer]AllowedIPs":
			for _, v := range strings.Split(val, ",") {
				cfg.AllowedIPs = append(cfg.AllowedIPs, strings.TrimSpace(v))
			}
		case "[Peer]PersistentKeepalive", "[Interface]ListenPort":
			n, err := strconv.ParseUint(val, 10, 16)
			if err != nil {
				return cfg, errors.New("invalid WireGuard interval or port")
			}
			v := uint16(n)
			if field == "[Peer]PersistentKeepalive" {
				cfg.Keepalive = &v
			} else {
				cfg.ListenPort = v
			}
		case "[Interface]MTU":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1280 || n > 9000 {
				return cfg, errors.New("WireGuard MTU must be 1280 to 9000")
			}
			cfg.MTU = n
		case "[Interface]DNS":
			// Only inbound KVM services use this virtual stack. It never resolves external names.
		default:
			return cfg, fmt.Errorf("unsupported WireGuard directive %q", name)
		}
	}
	if !sections["[Interface]"] || !sections["[Peer]"] {
		return cfg, errors.New("missing WireGuard interface or peer")
	}
	cfg.Enabled = true
	return cfg, nil
}
