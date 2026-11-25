// Package q1ble implements physically authorized Wi-Fi provisioning on the Q1 Pro.
package q1ble

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	requireAuthorization byte = 1
	authorized           byte = 2
	provisioning         byte = 3
	provisioned          byte = 4
)

type pairing struct {
	peer   string
	code   uint32
	answer chan bool
}

type snapshot struct {
	state      byte
	code       byte
	result     []byte
	confirming bool
}

type session struct {
	pair      *pairing
	ctx       context.Context
	provision func(context.Context, string, string) (string, error)
	mu        sync.Mutex
	value     snapshot
	peers     map[string]bool
	granted   string
	pending   []byte
	updates   chan struct{}
	finished  chan struct{}
}

func newSession(ctx context.Context, provision func(context.Context, string, string) (string, error)) *session {
	return &session{ctx: ctx, provision: provision, value: snapshot{state: requireAuthorization}, peers: make(map[string]bool), updates: make(chan struct{}, 1), finished: make(chan struct{})}
}

func (s *session) signal() {
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

func (s *session) read() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return snapshot{state: s.value.state, code: s.value.code, result: append([]byte(nil), s.value.result...), confirming: s.pair != nil}
}

func (s *session) connected(peer string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.peers[peer] = true
		return
	}
	delete(s.peers, peer)
	if s.pair != nil && peer == s.pair.peer {
		select {
		case s.pair.answer <- false:
		default:
		}
	}
	if peer == s.granted && s.value.state == authorized {
		s.granted = ""
		s.value = snapshot{state: requireAuthorization}
		clear(s.pending)
		s.pending = nil
		s.signal()
	}
}

func (s *session) authorize() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pair != nil && len(s.peers) == 1 && s.peers[s.pair.peer] && s.ctx.Err() == nil {
		select {
		case s.pair.answer <- true:
		default:
		}
		return true
	}
	if s.value.state != requireAuthorization || len(s.peers) != 1 || s.ctx.Err() != nil {
		return false
	}
	for peer := range s.peers {
		s.granted = peer
	}
	s.value.state = authorized
	s.value.code = 0
	s.signal()
	return true
}

func (s *session) mayPair(peer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return peer != "" && peer == s.granted && s.peers[peer] && s.ctx.Err() == nil &&
		(s.value.state == authorized || s.value.state == provisioning)
}

func (s *session) passkey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pair == nil {
		return ""
	}
	return fmt.Sprintf("%06d", s.pair.code)
}

func (s *session) confirm(peer string, passkey uint32) bool {
	s.mu.Lock()
	if s.pair != nil || peer == "" || peer != s.granted || !s.peers[peer] || len(s.peers) != 1 || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	p := &pairing{peer: peer, code: passkey, answer: make(chan bool, 1)}
	s.pair = p
	s.signal()
	s.mu.Unlock()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ok := false
	select {
	case ok = <-p.answer:
	case <-timer.C:
	case <-s.ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pair = nil
	s.signal()
	return ok && s.ctx.Err() == nil && s.peers[peer] && peer == s.granted && len(s.peers) == 1
}

func (s *session) cancelPair() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pair != nil {
		select {
		case s.pair.answer <- false:
		default:
		}
	}
}

func checksum(frame []byte) byte {
	var sum byte
	for _, v := range frame {
		sum += v
	}
	return sum
}

func (s *session) write(peer string, data []byte, offset uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.value.state != authorized || peer == "" || peer != s.granted || !s.peers[peer] {
		// Keep a second central from disturbing a valid transaction or its UI.
		if s.value.state == requireAuthorization {
			s.value.code = 4
			s.signal()
		}
		return errors.New("not authorized")
	}
	bad := func(code byte) error {
		clear(s.pending)
		s.pending = nil
		s.value.code = code
		s.signal()
		return errors.New("invalid Improv command")
	}
	if len(data) == 0 || (offset != 0 && int(offset) != len(s.pending)) || len(s.pending)+len(data) > 258 {
		return bad(1)
	}
	s.pending = append(s.pending, data...)
	if len(s.pending) < 2 {
		return nil
	}
	n := int(s.pending[1]) + 3
	if len(s.pending) < n {
		return nil
	}
	if len(s.pending) != n || checksum(s.pending[:n-1]) != s.pending[n-1] {
		return bad(1)
	}
	if s.pending[0] != 1 {
		return bad(2)
	}
	p := s.pending[2 : n-1]
	if len(p) < 2 {
		return bad(1)
	}
	ns := int(p[0])
	if ns == 0 || ns > 32 || 1+ns >= len(p) || int(p[1+ns]) != len(p)-ns-2 || !utf8.Valid(p[1:1+ns]) {
		return bad(1)
	}
	ssid, pass := string(p[1:1+ns]), string(p[2+ns:])
	clear(s.pending)
	s.pending = nil
	s.value = snapshot{state: provisioning}
	s.signal()
	go s.apply(ssid, pass)
	return nil
}

func (s *session) apply(ssid, password string) {
	url, err := s.provision(s.ctx, ssid, password)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.value = snapshot{state: requireAuthorization, code: 3}
		if s.peers[s.granted] {
			s.value.state = authorized
		} else {
			s.granted = ""
		}
		s.signal()
		return
	}
	payload := []byte{}
	if url != "" && len(url) <= 254 {
		payload = append([]byte{byte(len(url))}, []byte(url)...)
	}
	packet := append([]byte{1, byte(len(payload))}, payload...)
	packet = append(packet, checksum(packet))
	s.value = snapshot{state: provisioned, result: packet}
	s.signal()
	close(s.finished)
}

func waitResultRead(ctx context.Context) {
	// Clients may read the URL after their final state notification.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
