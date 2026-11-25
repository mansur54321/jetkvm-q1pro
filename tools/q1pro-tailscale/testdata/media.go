// A test-only RTP source in the disposable guest; the KVM HTTP and RPC stay in the released application.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jetkvm/kvm/internal/q1tailscale"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func memory() {
	fields := func(name, field string) string {
		raw, _ := os.ReadFile(name)
		for line := range strings.SplitSeq(string(raw), "\n") {
			items := strings.Fields(line)
			if len(items) >= 2 && items[0] == field+":" {
				if _, err := strconv.Atoi(items[1]); err == nil {
					return items[1]
				}
			}
		}
		return "unknown"
	}
	paths, _ := filepath.Glob("/proc/[0-9]*/exe")
	for _, path := range paths {
		name, _ := os.Readlink(path)
		if filepath.Base(name) == "tailscaled" {
			status := filepath.Join(filepath.Dir(path), "status")
			fmt.Fprintf(os.Stdout, "Q1 tailnet test: tailscaled RSS=%s KiB HWM=%s KiB; guest MemTotal=%s KiB MemAvailable=%s KiB\n",
				fields(status, "VmRSS"), fields(status, "VmHWM"), fields("/proc/meminfo", "MemTotal"), fields("/proc/meminfo", "MemAvailable"))
			return
		}
	}
}

func main() {
	data, err := os.ReadFile("/sample.ivf")
	if err != nil {
		panic(err)
	}
	if len(data) < 44 || string(data[:4]) != "DKIF" {
		panic("expected IVF video")
	}
	length := binary.LittleEndian.Uint32(data[32:36])
	if length == 0 || uint64(length) > uint64(len(data)-44) {
		panic("invalid IVF payload")
	}
	frame := data[44 : 44+length]
	// Hold the target root before PID 1 moves its mount away from /image.
	ready, err := os.Create("/media-root-ready")
	if err != nil {
		panic(err)
	}
	if err := syscall.Chroot("/image"); err != nil {
		panic(err)
	}
	if err := os.Chdir("/"); err != nil {
		panic(err)
	}
	if _, err := ready.WriteString("1"); err != nil {
		panic(err)
	}
	if err := ready.Close(); err != nil {
		panic(err)
	}
	for i := 0; i < 200; i++ {
		if model, _ := os.ReadFile("/sys/firmware/devicetree/base/model"); bytes.Equal(bytes.TrimRight(model, "\000"), []byte("linux,dummy-virt")) {
			break
		}
		if i == 199 {
			panic("private QEMU device tree not available")
		}
		time.Sleep(100 * time.Millisecond)
	}
	s := q1tailscale.New("/userdata/jetkvm/tailscale", "/run/q1-tailscale")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "Q1 test video")
	})
	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		ctx := s.SignalingContext(r.Context(), r.RemoteAddr, r.Host)
		peerIP := q1tailscale.MediaIPv4(ctx)
		if peerIP == "" {
			http.Error(w, "unverified Tailscale peer", 403)
			return
		}
		var offer webrtc.SessionDescription
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 131072)).Decode(&offer); err != nil {
			http.Error(w, "invalid offer", 400)
			return
		}
		engine := webrtc.SettingEngine{}
		engine.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		if err := q1tailscale.ConfigureICE(&engine, peerIP); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(engine)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		started := false
		defer func() {
			if !started {
				pc.Close()
			}
		}()
		track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "fixture", "q1")
		if err == nil {
			_, err = pc.AddTrack(track)
		}
		if err == nil {
			err = pc.SetRemoteDescription(offer)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		gather := webrtc.GatheringCompletePromise(pc)
		if err = pc.SetLocalDescription(answer); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		select {
		case <-gather:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
		if err = json.NewEncoder(w).Encode(pc.LocalDescription()); err != nil {
			return
		}
		started = true
		memory()
		go func() {
			defer pc.Close()
			for i := 0; i < 100; i++ {
				if err := track.WriteSample(media.Sample{Data: frame, Duration: 150 * time.Millisecond}); err != nil {
					return
				}
				time.Sleep(150 * time.Millisecond)
			}
		}()
	})
	if err := http.ListenAndServe("127.0.0.1:8078", nil); err != nil {
		panic(err)
	}
}
