package kvm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestQ1LocalSyntheticWebRTCInChromium(t *testing.T) {
	dir := os.Getenv("Q1_WEB_TEST")
	if dir == "" {
		t.Skip("use tools/tests/webrtc/run.sh for the isolated Chromium test")
	}
	h264, err := os.ReadFile(filepath.Join(dir, "test.h264"))
	if err != nil {
		t.Fatal(err)
	}
	units := bytes.Split(h264, []byte{0, 0, 0, 1, 9})[1:]
	if len(units) != 45 {
		t.Fatalf("fixture contains %d AUD-delimited access units", len(units))
	}
	for i := range units {
		units[i] = append([]byte{0, 0, 0, 1, 9}, units[i]...)
	}
	originalConfig := config
	defer func() { config = originalConfig }()
	originalSKU := GetDeviceSKU()
	defer func() { deviceSKU = originalSKU }()
	deviceSKU = q1ProSKU
	cfg := getDefaultConfig()
	cfg.VideoCodecPreference = "h264"
	cfg.AudioEnabled = true
	config = &cfg
	session, err := newSession(SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pc := session.peerConnection
	defer close(session.done)
	defer pc.Close()
	defer stopAudio()
	// The test exchanges all ICE candidates via HTTP and supplies its own video source.
	pc.OnICECandidate(func(*webrtc.ICECandidate) {})
	pc.OnICEConnectionStateChange(func(webrtc.ICEConnectionState) {})
	ctx, cancel := context.WithTimeout(context.Background(), 33*time.Second)
	defer cancel()
	done := make(chan string, 1)
	recovery := os.Getenv("Q1_AUDIO_RECOVERY") == "1"
	mux := http.NewServeMux()
	if recovery {
		mux.HandleFunc("POST /pause-clock", func(w http.ResponseWriter, _ *http.Request) {
			f, err := os.OpenFile(filepath.Join(dir, "pause-clock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				http.Error(w, "clock already paused", 409)
				return
			}
			f.Close()
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dir, "receiver.html"))
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 3000))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case done <- string(b):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /offer", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		gather := webrtc.GatheringCompletePromise(pc)
		_, err = session.ExchangeOffer(base64.StdEncoding.EncodeToString(b))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		select {
		case <-ctx.Done():
			http.Error(w, "gather timeout", 504)
			return
		case <-gather:
		}
		ans := pc.LocalDescription()
		if ans == nil || !strings.Contains(ans.SDP, "a=candidate:") {
			http.Error(w, "no local ICE candidates", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(ans)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	query := ""
	if recovery {
		query = "?clock-recovery"
	}
	cmd := exec.CommandContext(ctx, "chromium",
		"--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--disable-default-apps", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--autoplay-policy=no-user-gesture-required", "--disable-features=WebRtcHideLocalIpsWithMdns",
		"--user-data-dir="+t.TempDir(), server.URL+query)
	if recovery {
		// ALSA's null playback sink consumes samples without a clock. Chromium's
		// clocked silent output keeps Web Audio's render timing representative.
		cmd.Args = append(cmd.Args[:len(cmd.Args)-1], "--disable-audio-output", cmd.Args[len(cmd.Args)-1])
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var browserLog bytes.Buffer
	cmd.Stderr = &browserLog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(browserLog.String())
		}
	}()
	ticker := time.NewTicker(time.Second / 15)
	defer ticker.Stop()
	n := 0
	startedAudio := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout, %d frames submitted, state %s", n, pc.ConnectionState())
		case result := <-done:
			t.Logf("Chromium stats: %s", result)
			var stats struct {
				Error, Codec, AudioCodec                                                string
				AudioPackets, ToneHz, FirstToneHz, QuietMs                              int
				Decoded, Keyframes, Packets, Width, Height, Presentations, UniqueCanvas int
			}
			if err := json.Unmarshal([]byte(result), &stats); err != nil {
				t.Fatal(err)
			}
			low, high := 960, 1035
			if recovery {
				low, high = 1470, 1550
				if stats.FirstToneHz < 960 || stats.FirstToneHz > 1035 || stats.QuietMs < 4500 {
					t.Fatalf("audio clock gap not demonstrated: %s", result)
				}
				for _, name := range []string{"reopened-while-clock-idle", "recovered-prepared", "restarted-running"} {
					if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
						t.Fatalf("missing ALSA transition %s: %v", name, err)
					}
				}
				t.Log("real ALSA capture reopened during the six-second clock loss; injected XRUN -> snd_pcm_recover PREPARED -> snd_pcm_start RUNNING")
			}
			if stats.Error != "" || stats.Codec != "video/H264" || stats.Decoded < 30 || stats.Keyframes == 0 || stats.Packets == 0 ||
				stats.AudioCodec != "audio/G722" || stats.AudioPackets < 30 || stats.ToneHz < low || stats.ToneHz > high ||
				stats.Width != 320 || stats.Height != 240 || stats.Presentations < 10 || stats.UniqueCanvas < 10 {
				t.Fatalf("decode failed: %s", result)
			}
			t.Logf("JetKVM submitted %d Annex B access units", n)
			return
		case <-ticker.C:
			if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
				continue
			}
			if !startedAudio {
				if session.AudioTrack == nil {
					t.Fatal("missing negotiated displayport audio track")
				}
				startAudio(session.AudioTrack)
				startedAudio = true
			}
			err = session.writeVideoFrame(videoClockHardware, units[n%len(units)], time.Duration(n)*time.Second/15)
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
}
