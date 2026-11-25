package q1ble

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func wifiPacket(ssid, pass string) []byte {
	payload := append([]byte{byte(len(ssid))}, []byte(ssid)...)
	payload = append(payload, byte(len(pass)))
	payload = append(payload, []byte(pass)...)
	f := append([]byte{1, byte(len(payload))}, payload...)
	return append(f, checksum(f))
}
func TestPhysicalPeerAuthorizationAndFraming(t *testing.T) {
	var got [2]string
	s := newSession(context.Background(), func(_ context.Context, id, p string) (string, error) {
		got = [2]string{id, p}
		return "http://192.0.2.10/", nil
	})
	s.connected("phone", true)
	s.connected("other", true)
	if s.authorize() {
		t.Fatal("ambiguous peer authorized")
	}
	p := wifiPacket("lab network", "only-in-test")
	if err := s.write("phone", p, 0); err == nil {
		t.Fatal("accepted before physical approval")
	}
	s.connected("other", false)
	if !s.authorize() || s.mayPair("other") {
		t.Fatal("wrong peer authorized")
	}
	if err := s.write("other", p, 0); err == nil {
		t.Fatal("another client submitted credentials")
	}
	bad := append([]byte(nil), p...)
	bad[len(bad)-1]++
	if err := s.write("phone", bad, 0); err == nil || s.read().code != 1 {
		t.Fatal("malformed checksum accepted")
	}
	if err := s.write("phone", p[:6], 0); err != nil {
		t.Fatal(err)
	}
	s.connected("phone", false)
	s.connected("phone", true)
	if s.mayPair("phone") || !s.authorize() {
		t.Fatal("disconnect did not revoke peer grant")
	}
	for i := 0; i < len(p); i += 6 {
		if err := s.write("phone", p[i:min(i+6, len(p))], 0); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-s.finished:
	case <-time.After(time.Second):
		t.Fatal("did not finish")
	}
	v := s.read()
	if got != [2]string{"lab network", "only-in-test"} || v.state != 4 || v.code != 0 || checksum(v.result[:len(v.result)-1]) != v.result[len(v.result)-1] {
		t.Fatal("incorrect provisioning result")
	}
	if s.write("phone", p, 0) == nil {
		t.Fatal("reused completed authorization")
	}
}
func TestAssociationFailureKeepsOneAuthorizedPeer(t *testing.T) {
	s := newSession(context.Background(), func(context.Context, string, string) (string, error) { return "", errors.New("network rejected") })
	s.connected("one", true)
	s.authorize()
	if err := s.write("one", wifiPacket("lab", "not-a-real-key"), 0); err != nil {
		t.Fatal(err)
	}
	until := time.After(time.Second)
	for s.read().code != 3 {
		select {
		case <-s.updates:
		case <-until:
			t.Fatal("no failure notification")
		}
	}
	if s.read().state != 2 || s.mayPair("other") {
		t.Fatal("invalid state after failed attempt")
	}
}
func TestHandoffGrantExpiry(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "handoff"), func() bool { return true }, nil)
	if _, err := c.Handoff(true); err != nil {
		t.Fatal(err)
	}
	if !time.Unix(c.Status().HandoffUntil, 0).After(time.Now().Add(23 * time.Hour)) {
		t.Fatal("handoff not persisted")
	}
	if c.handoffUntil(time.Now().Add(-time.Hour)) != 0 || c.handoffUntil(time.Now().Add(25*time.Hour)) != 0 {
		t.Fatal("invalid clock retained grant")
	}
	if _, err := c.Handoff(false); err != nil || c.Status().HandoffUntil != 0 {
		t.Fatal("grant not revoked", err)
	}
}
