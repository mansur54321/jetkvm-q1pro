package q1tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyForwardedPeerAndConfigure(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, dir)
	if err := os.WriteFile(filepath.Join(dir, "enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", s.socket())
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	allowPeer := true
	tsStatus := "NeedsLogin"
	hasKey := false
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" {
			t.Errorf("unexpected LocalAPI host %q", r.Host)
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/localapi/v0/whois":
			if !allowPeer || r.URL.Query().Get("addr") != "127.0.0.1:34855" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, "{}")
		case "/localapi/v0/status":
			fmt.Fprintf(w, `{"HaveNodeKey":%t,"BackendState":%q,"Self":{"HostName":"q1","DNSName":"q1.tail.test.","TailscaleIPs":["100.64.0.8","fd7a:115c:a1e0::8"]}}`, hasKey, tsStatus)
		case "/localapi/v0/prefs":
			if r.Method == "GET" {
				fmt.Fprint(w, `{"ControlURL":"http://control.test"}`)
				return
			}
			var prefs map[string]any
			if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
				t.Error(err)
			}
			if prefs["WantRunningSet"] == true {
				if prefs["WantRunning"] != true || len(prefs) != 2 {
					t.Error("unexpected enable preferences")
				}
			} else if prefs["ControlURL"] != "http://control.test" || prefs["CorpDNS"] != false || prefs["RouteAll"] != false || prefs["NetfilterMode"] != float64(0) {
				t.Errorf("unexpected managed preferences")
			}
			fmt.Fprint(w, "{}")
		case "/localapi/v0/start", "/localapi/v0/login-interactive":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	ctx := context.Background()
	if err := s.configure(ctx, "http://control.test"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /localapi/v0/status", "GET /localapi/v0/prefs", "PATCH /localapi/v0/prefs", "POST /localapi/v0/start", "PATCH /localapi/v0/prefs", "POST /localapi/v0/login-interactive"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
	hasKey = true
	calls = nil
	if err := s.configure(ctx, "http://control.test"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"GET /localapi/v0/status", "GET /localapi/v0/prefs", "PATCH /localapi/v0/prefs"}) {
		t.Fatalf("resume requested a new identity: %v", calls)
	}
	tsStatus = "Running"
	for _, host := range []string{"100.64.0.8", "q1", "q1.tail.test.:80", "[fd7a:115c:a1e0::8]:80"} {
		if got := MediaIPv4(s.SignalingContext(ctx, "127.0.0.1:34855", host)); got != "100.64.0.8" {
			t.Errorf("missing tailnet candidate for %s", host)
		}
	}
	if got := MediaIPv4(s.SignalingContext(ctx, "127.0.0.1:34855", "unrelated.test")); got != "" {
		t.Fatal(got)
	}
	allowPeer = false
	if got := MediaIPv4(s.SignalingContext(ctx, "127.0.0.1:34855", "100.64.0.8")); got != "" {
		t.Fatal(got)
	}
	if got := MediaIPv4(s.SignalingContext(ctx, "192.0.2.8:34855", "100.64.0.8")); got != "" {
		t.Fatal(got)
	}
}

func TestAddonFailureKeepsUserState(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, filepath.Join(t.TempDir(), "run"))
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "owner-sentinel")
	if err := os.WriteFile(marker, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(strings.NewReader("not an archive")); err == nil {
		t.Fatal("accepted invalid archive")
	}
	if err := s.SetEnabled(true, ""); err == nil {
		t.Fatal("enabled absent daemon")
	}
	if err := s.SetEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(payload, []byte("sentinel")) || s.Enabled() {
		t.Fatalf("disabled state damaged: %v", err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("left partial files: %v", files)
	}
}
