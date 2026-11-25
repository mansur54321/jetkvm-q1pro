package q1vpn

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func TestAuthenticatedHTTPAndWebRTCThroughWireGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hubIP := netip.MustParseAddr("10.77.0.1")
	clientIP := netip.MustParseAddr("10.77.0.5")
	tun, stack, err := netstack.CreateNetTUN([]netip.Addr{hubIP}, nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	hub := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer hub.Close()
	identity, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := New(filepath.Join(t.TempDir(), "wireguard.json"))
	st, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	q1key, err := keyBytes(st.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Running {
		t.Fatal("new profile is already enabled")
	}
	err = hub.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=%s/32\n", hex.EncodeToString(identity.Bytes()), hex.EncodeToString(q1key), clientIP))
	if err != nil {
		t.Fatal(err)
	}
	if err = hub.Up(); err != nil {
		t.Fatal(err)
	}
	peerState, err := hub.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	for _, line := range strings.Split(peerState, "\n") {
		if text, ok := strings.CutPrefix(line, "listen_port="); ok {
			port, _ = strconv.Atoi(text)
		}
	}
	if port == 0 {
		t.Fatal("missing dynamic UDP port")
	}
	q1mux := make(chan ice.UDPMux, 1)
	m.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux := MediaMux(r.Context())
		if mux == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		select {
		case q1mux <- mux:
		default:
		}
		io.WriteString(w, "q1")
	})
	settings := Settings{Enabled: true, Address: clientIP.String(), AllowedIPs: []string{hubIP.String() + "/32"}, Endpoint: fmt.Sprintf("127.0.0.1:%d", port), PeerKey: base64.StdEncoding.EncodeToString(identity.PublicKey().Bytes())}
	if err = m.Update(settings); err != nil {
		t.Fatal(err)
	}
	defer func() { settings.Enabled = false; _ = m.Update(settings) }()
	tr := &http.Transport{DialContext: stack.DialContext}
	defer tr.CloseIdleConnections()
	web := &http.Client{Transport: tr, Timeout: 7 * time.Second}
	for _, authenticated := range []bool{false, true} {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+clientIP.String()+"/device", nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer test")
		}
		resp, err := web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || (authenticated && (resp.StatusCode != 200 || string(b) != "q1")) || (!authenticated && resp.StatusCode != 401) {
			t.Fatal("unexpected HTTP status", resp.StatusCode)
		}
	}
	hubSock, err := stack.ListenUDPAddrPort(netip.AddrPortFrom(hubIP, 51821))
	if err != nil {
		t.Fatal(err)
	}
	hubMux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: hubSock})
	defer hubMux.Close()
	newPC := func(mux ice.UDPMux) *webrtc.PeerConnection {
		var se webrtc.SettingEngine
		se.SetICEUDPMux(mux)
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		return pc
	}
	remote := newPC(hubMux)
	defer remote.Close()
	local := newPC(<-q1mux)
	defer local.Close()
	received := make(chan string, 1)
	local.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(m webrtc.DataChannelMessage) { _ = dc.SendText("echo:" + string(m.Data)) })
	})
	channel, err := remote.CreateDataChannel("kvm", nil)
	if err != nil {
		t.Fatal(err)
	}
	channel.OnOpen(func() { _ = channel.SendText("rpc") })
	channel.OnMessage(func(msg webrtc.DataChannelMessage) { received <- string(msg.Data) })
	offer, err := remote.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	setAndWait := func(pc *webrtc.PeerConnection, d webrtc.SessionDescription) {
		done := webrtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(d); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("gathering timed out")
		}
	}
	setAndWait(remote, offer)
	if err = local.SetRemoteDescription(*remote.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := local.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	setAndWait(local, answer)
	if !strings.Contains(local.LocalDescription().SDP, clientIP.String()) {
		t.Fatal("Q1 offer lacks its VPN IP")
	}
	if err = remote.SetRemoteDescription(*local.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-received:
		if msg != "echo:rpc" {
			t.Fatal(msg)
		}
	case <-ctx.Done():
		t.Fatal("UDP/ICE over the tunnel timed out")
	}
	final, err := m.Status()
	if err != nil || !final.Running || final.LastHandshake <= 0 {
		t.Fatal("no authenticated handshake", err)
	}
	info, err := os.Stat(m.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unexpected config permissions")
	}
	wrong := settings
	wrong.PeerKey = "wrong"
	if err = m.Update(wrong); err == nil {
		t.Fatal("accepted invalid configuration")
	}
	after, err := m.Status()
	if err != nil || after.PeerKey != settings.PeerKey || !after.Running {
		t.Fatal("failed update changed live profile", err)
	}
	reopened, err := New(m.path).Status()
	if err != nil || reopened.PublicKey != st.PublicKey || !reopened.Enabled {
		t.Fatal("failed to restore stored identity", err)
	}
}
