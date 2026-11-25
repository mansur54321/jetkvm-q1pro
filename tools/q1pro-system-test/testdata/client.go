package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"
)

type signal struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func sessionCookie(ctx context.Context, password string) (*http.Cookie, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	for ctx.Err() == nil {
		resp, err := client.Get("http://127.0.0.1/device/status")
		if err == nil {
			var status struct{ IsSetup bool }
			err = json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if err == nil && status.IsSetup {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post("http://127.0.0.1/auth/login-local", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	for _, cookie := range resp.Cookies() {
		if resp.StatusCode == http.StatusOK && cookie.Name == "authToken" && cookie.Value != "" {
			return cookie, nil
		}
	}
	return nil, fmt.Errorf("login returned %s without a session", resp.Status)
}

func acceptSignals(ctx context.Context, ws *websocket.Conn, pc *webrtc.PeerConnection) error {
	var pending []webrtc.ICECandidateInit
	for {
		var msg signal
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			return err
		}
		switch msg.Type {
		case "answer":
			var encoded string
			if err := json.Unmarshal(msg.Data, &encoded); err != nil {
				return err
			}
			packet, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return err
			}
			var answer webrtc.SessionDescription
			if err := json.Unmarshal(packet, &answer); err != nil {
				return err
			}
			if err := pc.SetRemoteDescription(answer); err != nil {
				return err
			}
			for _, c := range pending {
				if err := pc.AddICECandidate(c); err != nil {
					return err
				}
			}
			pending = nil
		case "new-ice-candidate":
			var cand webrtc.ICECandidateInit
			if err := json.Unmarshal(msg.Data, &cand); err != nil {
				return err
			}
			if pc.RemoteDescription() == nil {
				pending = append(pending, cand)
			} else if err := pc.AddICECandidate(cand); err != nil {
				return err
			}
		}
	}
}

func waitForDHCP(ctx context.Context) error {
	for ctx.Err() == nil {
		iface, err := net.InterfaceByName("eth0")
		if err == nil {
			addrs, err := iface.Addrs()
			if err != nil {
				return err
			}
			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
					return nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ctx.Err()
}

type rpcReply struct {
	ID     int
	Result json.RawMessage
	Error  json.RawMessage
}

// VmHWM comes from the kernel and covers each live process's lifetime.
func memoryKiB(data []byte, key string) (int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == key+":" && fields[2] == "kB" {
			return strconv.Atoi(fields[1])
		}
	}
	return 0, fmt.Errorf("missing %s", key)
}

func reportMemory() error {
	info, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return err
	}
	total, err := memoryKiB(info, "MemTotal")
	if err != nil {
		return err
	}
	available, err := memoryKiB(info, "MemAvailable")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Q1 guest memory CPUs=%d MemTotal_KiB=%d MemAvailable_KiB=%d\n", runtime.NumCPU(), total, available)
	pids, err := filepath.Glob("/proc/[0-9]*/status")
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, statusPath := range pids {
		dir := filepath.Dir(statusPath)
		exe, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil || filepath.Base(exe) != "jetkvm_app" {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			return err
		}
		title := string(bytes.SplitN(cmdline, []byte{0}, 2)[0])
		role := ""
		for _, r := range []string{"supervisor", "app", "native"} {
			if strings.HasPrefix(title, "jetkvm: ["+r+"]") || strings.HasPrefix(title, "jetkvm: ["+r+"+") {
				role = r
				break
			}
		}
		if role == "" {
			continue
		}
		info, err = os.ReadFile(statusPath)
		if err != nil {
			return err
		}
		rss, err := memoryKiB(info, "VmRSS")
		if err != nil {
			return err
		}
		peak, err := memoryKiB(info, "VmHWM")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Q1 process %s VmRSS_KiB=%d VmHWM_KiB=%d\n", role, rss, peak)
		found[role] = true
	}
	if len(found) != 3 {
		return fmt.Errorf("could not identify all three JetKVM roles: %v", found)
	}
	return nil
}

func verifyRPC(ctx context.Context, rpc *webrtc.DataChannel, replies <-chan rpcReply) error {
	call := func(id int, method string, params any) (json.RawMessage, error) {
		req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if err := rpc.SendText(string(req)); err != nil {
			return nil, err
		}
		select {
		case reply := <-replies:
			if reply.ID != id || (len(reply.Error) > 0 && string(reply.Error) != "null") {
				return nil, fmt.Errorf("%s: id %d, error %s", method, reply.ID, reply.Error)
			}
			return reply.Result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	state, err := call(37, "getVideoState", map[string]any{})
	if err != nil {
		return err
	}
	var video struct {
		Ready         *bool
		Error         string
		Width, Height int
	}
	if err := json.Unmarshal(state, &video); err != nil {
		return err
	}
	if video.Ready == nil || *video.Ready || video.Error != "device_unavailable" || video.Width != 0 || video.Height != 0 {
		return fmt.Errorf("unexpected video state: %+v", video)
	}
	fmt.Fprintln(os.Stdout, "Q1 WebRTC verified getVideoState ready=false error=device_unavailable width=0 height=0")
	raw, err := call(40, "getDeviceCapabilities", map[string]any{})
	if err != nil {
		return err
	}
	var capabilities []string
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		return err
	}
	if !slices.Contains(capabilities, "fixed_edid") || slices.Contains(capabilities, "custom_edid") {
		return fmt.Errorf("unexpected Q1 EDID capabilities: %v", capabilities)
	}
	fmt.Fprintln(os.Stdout, "Q1 WebRTC verified fixed_edid and no custom_edid")
	if !slices.Contains(capabilities, "storage_download") || !slices.Contains(capabilities, "storage_download_only") {
		return fmt.Errorf("unexpected storage capabilities without NBD: %v", capabilities)
	}
	raw, err = call(41, "getStorageSpace", map[string]any{})
	if err != nil {
		return err
	}
	var space struct{ BytesFree int64 }
	if err := json.Unmarshal(raw, &space); err != nil {
		return err
	}
	if space.BytesFree <= 0 || space.BytesFree > 64*1024*1024 {
		return fmt.Errorf("invalid free space on 64 MiB guest disk: %d", space.BytesFree)
	}
	fmt.Fprintf(os.Stdout, "Q1 storage download-only free_bytes=%d\n", space.BytesFree)
	dial := func() (net.Conn, error) { return net.DialTimeout("tcp4", "127.0.0.1:22", time.Second) }
	conn, err := dial()
	if err == nil {
		conn.Close()
		return fmt.Errorf("SSH enabled before developer mode")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	if _, err = call(38, "setDevModeState", map[string]bool{"enabled": true}); err != nil {
		return err
	}
	for ctx.Err() == nil {
		conn, err = dial()
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	banner, err := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(banner, "SSH-2.0-dropbear") {
		return fmt.Errorf("unexpected SSH banner %q", banner)
	}
	if _, err = call(39, "setDevModeState", map[string]bool{"enabled": false}); err != nil {
		return err
	}
	for ctx.Err() == nil {
		conn, err = dial()
		if errors.Is(err, syscall.ECONNREFUSED) {
			return reportMemory()
		}
		if err != nil {
			return err
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return ctx.Err()
}

func test(ctx context.Context, password string) error {
	if err := waitForDHCP(ctx); err != nil {
		return err
	}
	cookie, err := sessionCookie(ctx, password)
	if err != nil {
		return err
	}
	ws, _, err := websocket.Dial(ctx, "ws://127.0.0.1/webrtc/signaling/client", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	defer pc.Close()
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return err
		}
	}
	rpc, err := pc.CreateDataChannel("rpc", nil)
	if err != nil {
		return err
	}
	result := make(chan error, 2)
	replies := make(chan rpcReply, 4)
	rpc.OnMessage(func(m webrtc.DataChannelMessage) {
		var msg rpcReply
		if err := json.Unmarshal(m.Data, &msg); err == nil && msg.ID != 0 {
			replies <- msg
		}
	})
	rpc.OnOpen(func() { go func() { result <- verifyRPC(ctx, rpc, replies) }() })
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return ctx.Err()
	}
	sdp, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, ws, map[string]any{"type": "offer", "data": map[string]string{"sd": base64.StdEncoding.EncodeToString(sdp)}}); err != nil {
		return err
	}
	go func() { result <- acceptSignals(ctx, ws, pc) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("wait for WebRTC in state %s: %w", pc.ConnectionState(), ctx.Err())
	}
}

func main() {
	password, err := os.ReadFile("/test-password")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		err = test(ctx, string(bytes.TrimSpace(password)))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Q1 WebRTC failure:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Q1 verified developer SSH closed, Dropbear SSH-2.0 banner when enabled, closed after disable")
}
