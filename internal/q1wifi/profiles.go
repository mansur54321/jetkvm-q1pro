package q1wifi

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/pbkdf2"
)

var errBusy = errors.New("a Wi-Fi change is still in progress")

func profileKey(ssid, pass string) (string, error) {
	if len(ssid) == 0 || len(ssid) > 32 || !utf8.ValidString(ssid) {
		return "", errors.New("SSID must contain 1 to 32 bytes of UTF-8 text")
	}
	if pass == "" {
		return "", nil
	}
	if len(pass) == 64 {
		if _, err := hex.DecodeString(pass); err == nil {
			return pass, nil
		}
	}
	if len(pass) < 8 || len(pass) > 63 || strings.IndexFunc(pass, func(r rune) bool { return r < 32 || r > 126 }) >= 0 {
		return "", errors.New("Wi-Fi password must be 8 to 63 printable ASCII characters or 64 hex digits")
	}
	return hex.EncodeToString(pbkdf2.Key([]byte(pass), []byte(ssid), 4096, 32, sha1.New)), nil
}

func (s *Station) restore(cause error) error {
	if err := s.ok("RECONFIGURE"); err != nil {
		return errors.Join(cause, fmt.Errorf("reload saved Wi-Fi settings: %w", err))
	}
	return cause
}

type provisionRequest struct {
	verify func() error
	done   chan error
}

// Provision keeps the old disk profile until association and address assignment succeed.
func (s *Station) Provision(ssid, pass string, verify func() error) error {
	request := &provisionRequest{verify: verify, done: make(chan error, 1)}
	if _, err := s.save(ssid, pass, true, request); err != nil {
		return err
	}
	return <-request.done
}

func (s *Station) Save(ssid, pass string, connect bool) (bool, error) {
	return s.save(ssid, pass, connect, nil)
}

func (s *Station) save(ssid, pass string, connect bool, request *provisionRequest) (bool, error) {
	psk, err := profileKey(ssid, pass)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return false, errBusy
	}
	profiles, err := s.profiles()
	if err != nil {
		return false, err
	}
	old := -1
	for _, p := range profiles {
		if p.SSID == ssid {
			old = p.id
			break
		}
	}
	if old < 0 && len(profiles) >= maxProfiles {
		return false, errors.New("saved network limit reached")
	}
	text, err := s.cmd("ADD_NETWORK")
	if err != nil {
		return false, err
	}
	id, err := strconv.Atoi(text)
	if err != nil || id < 0 {
		return false, s.restore(errors.New("invalid network ID from wpa_supplicant"))
	}
	prefix := fmt.Sprintf("SET_NETWORK %d ", id)
	commands := []string{prefix + "ssid " + hex.EncodeToString([]byte(ssid)), prefix + "scan_ssid 1"}
	if psk == "" {
		results, err := s.cmd("SCAN_RESULTS")
		if err != nil {
			return false, s.restore(err)
		}
		visible, found := scanEntries(results)[ssid]
		useOWE := visible.enhancedOpen
		if !found && old >= 0 {
			key, err := s.cmd(fmt.Sprintf("GET_NETWORK %d key_mgmt", old))
			if err != nil {
				return false, s.restore(err)
			}
			useOWE = key == "OWE"
		}
		if useOWE {
			commands = append(commands, prefix+"key_mgmt OWE", prefix+"ieee80211w 2")
		} else {
			commands = append(commands, prefix+"key_mgmt NONE")
		}
	} else if len(pass) != 64 {
		commands = append(commands, prefix+"key_mgmt WPA-PSK SAE", prefix+"ieee80211w 1",
			prefix+"psk "+psk, prefix+"sae_password "+hex.EncodeToString([]byte(pass)))
	} else {
		commands = append(commands, prefix+"key_mgmt WPA-PSK", prefix+"psk "+psk)
	}
	for _, c := range commands {
		if err := s.ok(c); err != nil {
			return false, s.restore(err)
		}
	}
	if old >= 0 {
		status, err := s.cmd("STATUS")
		if err != nil {
			return false, s.restore(err)
		}
		active := fields(status)
		connect = connect || (active["wpa_state"] == "COMPLETED" && active["id"] == strconv.Itoa(old))
	}
	if connect {
		if err := s.startConnect(id, old, request); err != nil {
			return false, s.restore(err)
		}
		return true, nil
	}
	if err := s.ok(fmt.Sprintf("ENABLE_NETWORK %d no-connect", id)); err != nil {
		return false, s.restore(err)
	}
	if old >= 0 {
		if err := s.ok(fmt.Sprintf("REMOVE_NETWORK %d", old)); err != nil {
			return false, s.restore(err)
		}
	}
	if err := s.ok("SAVE_CONFIG"); err != nil {
		return false, s.restore(err)
	}
	s.lastError = ""
	return false, nil
}

func (s *Station) find(ssid string) (int, error) {
	profiles, err := s.profiles()
	if err != nil {
		return 0, err
	}
	for _, p := range profiles {
		if p.SSID == ssid {
			return p.id, nil
		}
	}
	return 0, errors.New("Wi-Fi network is not saved")
}

func (s *Station) Remove(ssid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return errBusy
	}
	id, err := s.find(ssid)
	if err != nil {
		return err
	}
	if err := s.ok(fmt.Sprintf("REMOVE_NETWORK %d", id)); err != nil {
		return err
	}
	if err := s.ok("SAVE_CONFIG"); err != nil {
		return s.restore(err)
	}
	s.lastError = ""
	return nil
}

func (s *Station) Connect(ssid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return errBusy
	}
	id, err := s.find(ssid)
	if err != nil {
		return err
	}
	return s.startConnect(id, -1, nil)
}

// The prior profiles remain on disk while the candidate associates. This
// also restores the last working profile if power is lost during the change.
func (s *Station) startConnect(id, old int, request *provisionRequest) error {
	if err := s.ok(fmt.Sprintf("SELECT_NETWORK %d", id)); err != nil {
		return err
	}
	s.completeAssociation(id, "auto", request, func() error { return s.commitConnection(id, old) })
	return nil
}

func (s *Station) completeAssociation(id int, band string, request *provisionRequest, commit func() error) {
	s.applying = true
	s.lastError = ""
	go func() {
		err := s.waitAssociation(id, band, 30*time.Second)
		if err == nil && request != nil {
			err = request.verify()
			if err == nil {
				err = s.waitAssociation(id, band, time.Second)
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err == nil {
			err = commit()
		}
		if err != nil {
			err = s.restore(err)
			s.lastError = err.Error()
		}
		s.applying = false
		if request != nil {
			request.done <- err
		}
	}()
}

func (s *Station) waitAssociation(id int, band string, wait time.Duration) error {
	end := time.Now().Add(wait)
	for time.Now().Before(end) {
		text, err := s.cmd("STATUS")
		if err != nil {
			return err
		}
		state := fields(text)
		freq, _ := strconv.Atoi(state["freq"])
		validBand := band == "auto" || (band == "2.4ghz" && freq >= 2412 && freq <= 2484) ||
			(band == "5ghz" && freq >= 5000 && freq <= 5900)
		if state["wpa_state"] == "COMPLETED" && state["id"] == strconv.Itoa(id) && validBand {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("Wi-Fi association timed out")
}

func (s *Station) commitConnection(id, old int) error {
	if old >= 0 {
		if err := s.ok(fmt.Sprintf("REMOVE_NETWORK %d", old)); err != nil {
			return err
		}
	}
	profiles, err := s.profiles()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		priority := 0
		if p.id == id {
			priority = 1
		}
		if err := s.ok(fmt.Sprintf("SET_NETWORK %d priority %d", p.id, priority)); err != nil {
			return err
		}
		if p.id != id {
			if err := s.ok(fmt.Sprintf("ENABLE_NETWORK %d no-connect", p.id)); err != nil {
				return err
			}
		}
	}
	return s.ok("SAVE_CONFIG")
}

func (s *Station) SetBand(band string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return errBusy
	}
	var frequencies []string
	switch band {
	case "auto":
		frequencies = []string{"0"}
	case "2.4ghz":
		for mhz := 2412; mhz <= 2472; mhz += 5 {
			frequencies = append(frequencies, strconv.Itoa(mhz))
		}
		frequencies = append(frequencies, "2484")
	case "5ghz":
		// The regulatory domain and driver still restrict the allowed channels.
		for mhz := 5000; mhz <= 5900; mhz += 5 {
			frequencies = append(frequencies, strconv.Itoa(mhz))
		}
	default:
		return errors.New("unsupported Wi-Fi band")
	}
	text, err := s.cmd("STATUS")
	if err != nil {
		return err
	}
	status := fields(text)
	if err := s.ok("SET freq_list " + strings.Join(frequencies, " ")); err != nil {
		return err
	}
	if err := s.ok("REASSOCIATE"); err != nil {
		return s.restore(err)
	}
	if status["wpa_state"] == "COMPLETED" {
		id, err := strconv.Atoi(status["id"])
		if err != nil {
			return s.restore(errors.New("unknown connected Wi-Fi profile"))
		}
		s.completeAssociation(id, band, nil, func() error { return s.ok("SAVE_CONFIG") })
		return nil
	}
	if err := s.ok("SAVE_CONFIG"); err != nil {
		return s.restore(err)
	}
	s.lastError = ""
	return nil
}
