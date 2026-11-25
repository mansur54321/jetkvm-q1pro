package q1tailscale

import (
	"net"

	"github.com/pion/webrtc/v4"
)

// ConfigureICE binds the media socket to the IPv4 loopback address used by netstack.forwardUDP.
func ConfigureICE(engine *webrtc.SettingEngine, addr string) error {
	engine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	engine.SetIncludeLoopbackCandidate(true)
	engine.SetInterfaceFilter(func(name string) bool { return name == "lo" })
	engine.SetIPFilter(func(ip net.IP) bool { return ip.Equal(net.IPv4(127, 0, 0, 1)) })
	return engine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
		Local: "127.0.0.1", External: []string{addr}, Mode: webrtc.ICEAddressRewriteReplace,
		AsCandidateType: webrtc.ICECandidateTypeHost,
	})
}
