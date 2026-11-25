package kvm

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/unix"
)

type controlLEDMarker struct {
	path string
	file *os.File
}

var q1ControlLED = controlLEDMarker{path: "/run/jetkvm-control-active"}

func (m *controlLEDMarker) set(active bool) error {
	if !active {
		if m.file != nil {
			defer m.file.Close()
			m.file = nil
		}
		if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if m.file != nil {
		return nil
	}
	// Publish an already locked file. The kernel releases it even if the app crashes.
	f, err := os.CreateTemp(filepath.Dir(m.path), ".jetkvm-control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		err = os.Rename(f.Name(), m.path)
	}
	if err != nil {
		f.Close()
		return err
	}
	m.file = f
	return nil
}

// Called during startup or with sessionSwitchMu held across ownership changes.
func syncQ1ControlLED(s *Session) {
	if !isQ1Pro() {
		return
	}
	active := s != nil && s.q1ControlChannel != nil && s.q1ControlChannel.ReadyState() == webrtc.DataChannelStateOpen
	if err := q1ControlLED.set(active); err != nil {
		webrtcLogger.Warn().Err(err).Msg("Unable to update Q1 control session LED")
	}
}

// A replacement RPC channel supersedes the previous channel of this session.
func (s *Session) refreshQ1ControlLED(newChannel *webrtc.DataChannel) {
	sessionSwitchMu.Lock()
	defer sessionSwitchMu.Unlock()
	if newChannel != nil {
		s.q1ControlChannel = newChannel
	}
	if currentSession == s {
		syncQ1ControlLED(s)
	}
}
