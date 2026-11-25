package q1vpn

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestImportClientProfile(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	profile := "[Interface]\nPrivateKey=" + key + "\nAddress=192.0.2.10/32\nMTU=1420\n[Peer]\nPublicKey=" + key + "\nAllowedIPs=192.0.2.0/24\nEndpoint=vpn.example.com:51820\nPersistentKeepalive=0\nPresharedKey=" + key + "\n"
	cfg, err := parseProfile(profile)
	if err != nil || !cfg.Enabled || cfg.Address != "192.0.2.10" || cfg.PrivateKey != key || cfg.PresharedKey != key || cfg.MTU != 1420 || cfg.Keepalive == nil || *cfg.Keepalive != 0 {
		t.Fatal("client profile did not round-trip", err)
	}
	for _, bad := range []string{profile + "[Peer]\n", strings.Replace(profile, "[Peer]", "PostUp=anything\n[Peer]", 1), profile + "PublicKey=" + key} {
		if _, e := parseProfile(bad); e == nil {
			t.Fatal("accepted unsupported or duplicate directive")
		}
	}
}
