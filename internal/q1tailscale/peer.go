package q1tailscale

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"strings"

	ts "github.com/jetkvm/kvm/internal/tailscale"
)

type mediaKey struct{}

func MediaIPv4(ctx context.Context) string {
	addr, _ := ctx.Value(mediaKey{}).(string)
	return addr
}

// Userspace tailscaled registers the loopback TCP endpoint of each forwarded peer with WhoIs.
func (s *Service) SignalingContext(ctx context.Context, remote, host string) context.Context {
	peer, err := netip.ParseAddrPort(remote)
	if err != nil || !peer.Addr().Unmap().IsLoopback() || !s.Enabled() {
		return ctx
	}
	if _, err := s.call(ctx, "GET", "whois?addr="+url.QueryEscape(peer.String())+"&proto=tcp", nil); err != nil {
		return ctx
	}
	body, err := s.call(ctx, "GET", "status?peers=false", nil)
	if err != nil {
		return ctx
	}
	var status struct {
		BackendState string
		Self         *ts.Peer
	}
	if err := json.Unmarshal(body, &status); err != nil || status.BackendState != "Running" || status.Self == nil {
		return ctx
	}
	if hp, _, err := net.SplitHostPort(host); err == nil {
		host = hp
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	dns := strings.ToLower(strings.TrimSuffix(status.Self.DNSName, "."))
	match := host != "" && (host == dns || strings.EqualFold(host, status.Self.HostName))
	var ipv4 netip.Addr
	for _, ip := range status.Self.TailscaleIPs {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		if host == addr.String() {
			match = true
		}
		if addr.Is4() {
			ipv4 = addr
		}
	}
	if !match || !ipv4.IsValid() {
		return ctx
	}
	return context.WithValue(ctx, mediaKey{}, ipv4.String())
}
