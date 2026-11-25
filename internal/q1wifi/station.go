package q1wifi

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxProfiles = 32

type Station struct {
	mu        sync.Mutex
	socket    string
	config    string
	applying  bool
	lastError string
	scanning  bool
}

func New(socket, config string) *Station { return &Station{socket: socket, config: config} }

type Network struct {
	SSID      string `json:"ssid"`
	Secure    bool   `json:"secure"`
	Connected bool   `json:"connected"`
	id        int
}

type Settings struct {
	Available   bool      `json:"available"`
	Connected   bool      `json:"connected"`
	Applying    bool      `json:"applying"`
	Switching   bool      `json:"switching"`
	ApplyFailed bool      `json:"apply_failed"`
	Error       string    `json:"connect_error"`
	Band        string    `json:"band"`
	Channel     int       `json:"channel"`
	RSSI        int       `json:"rssi"`
	Max         int       `json:"max_networks"`
	Networks    []Network `json:"networks"`
}

func (s *Station) cmd(command string) (string, error) { return control(s.socket, command) }

func (s *Station) ok(command string) error {
	reply, err := s.cmd(command)
	if err != nil {
		return err
	}
	if reply != "OK" {
		return fmt.Errorf("unexpected wpa_supplicant response to %s", strings.Fields(command)[0])
	}
	return nil
}

func (s *Station) profiles() ([]Network, error) {
	text, err := s.cmd("LIST_NETWORKS")
	if err != nil {
		return nil, err
	}
	networks := []Network{}
	for line := range strings.SplitSeq(text, "\n") {
		values := strings.SplitN(line, "\t", 4)
		if len(values) != 4 {
			continue
		}
		id, err := strconv.Atoi(values[0])
		ssid, ok := decodeSSID(values[1])
		if err != nil || id < 0 || !ok || !utf8.ValidString(ssid) {
			continue
		}
		key, err := s.cmd(fmt.Sprintf("GET_NETWORK %d key_mgmt", id))
		if err != nil {
			return nil, err
		}
		networks = append(networks, Network{SSID: ssid, Secure: key != "NONE" && key != "OWE", id: id})
	}
	return networks, nil
}

func (s *Station) band() (string, error) {
	f, err := os.Open(s.config)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "network={" {
			break
		}
		if v, ok := strings.CutPrefix(line, "freq_list="); ok {
			freqs := strings.Fields(v)
			if len(freqs) == 0 {
				continue
			}
			freq, _ := strconv.Atoi(freqs[0])
			if freq >= 4900 {
				return "5ghz", nil
			}
			if freq >= 2400 {
				return "2.4ghz", nil
			}
		}
	}
	return "auto", sc.Err()
}

func (s *Station) Settings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := Settings{Max: maxProfiles, Applying: s.applying, Switching: s.applying, Error: s.lastError, ApplyFailed: s.lastError != ""}
	text, err := s.cmd("STATUS")
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	status := fields(text)
	state.Available = true
	state.Connected = status["wpa_state"] == "COMPLETED"
	state.Networks, err = s.profiles()
	if err != nil {
		return state, err
	}
	if state.Connected {
		for i := range state.Networks {
			state.Networks[i].Connected = strconv.Itoa(state.Networks[i].id) == status["id"]
		}
		freq, _ := strconv.Atoi(status["freq"])
		switch {
		case freq == 2484:
			state.Channel = 14
		case freq >= 2412 && freq <= 2472:
			state.Channel = (freq - 2407) / 5
		case freq >= 5000 && freq < 5925:
			state.Channel = (freq - 5000) / 5
		}
		if poll, err := s.cmd("SIGNAL_POLL"); err == nil {
			state.RSSI, _ = strconv.Atoi(fields(poll)["RSSI"])
		}
	}
	state.Band, err = s.band()
	return state, err
}

type Scanned struct {
	enhancedOpen bool
	SSID         string `json:"ssid"`
	RSSI         int    `json:"rssi"`
	Secure       bool   `json:"secure"`
	Saved        bool   `json:"saved"`
}
type ScanResult struct {
	Scanning bool      `json:"scanning"`
	Networks []Scanned `json:"networks"`
}

func (s *Station) startScan() error {
	events, err := openControl(s.socket)
	if err != nil {
		return err
	}
	if reply, err := exchange(events, "ATTACH"); err != nil || reply != "OK" {
		events.Close()
		return errors.New("cannot subscribe to Wi-Fi scan results")
	}
	if err := s.ok("SCAN"); err != nil && !errors.Is(err, errScanBusy) {
		events.Close()
		return err
	}
	s.scanning = true
	go func() {
		defer events.Close()
		_ = events.SetReadDeadline(time.Now().Add(20 * time.Second))
		var buf [4096]byte
		for {
			n, err := events.Read(buf[:])
			event := string(buf[:n])
			if err != nil || strings.Contains(event, "CTRL-EVENT-SCAN-RESULTS") || strings.Contains(event, "CTRL-EVENT-SCAN-FAILED") {
				break
			}
		}
		_, _ = exchange(events, "DETACH")
		s.mu.Lock()
		s.scanning = false
		s.mu.Unlock()
	}()
	return nil
}

func (s *Station) Scan(refresh bool) (ScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ScanResult{Networks: []Scanned{}}
	if refresh && !s.scanning {
		if err := s.startScan(); err != nil {
			return result, err
		}
	}
	result.Scanning = s.scanning
	text, err := s.cmd("SCAN_RESULTS")
	if err != nil {
		return result, err
	}
	networks, err := s.profiles()
	if err != nil {
		return result, err
	}
	saved := make(map[string]bool)
	for _, n := range networks {
		saved[n.SSID] = true
	}
	strongest := scanEntries(text)
	for _, n := range strongest {
		n.Saved = saved[n.SSID]
		result.Networks = append(result.Networks, n)
	}
	sort.Slice(result.Networks, func(i, j int) bool {
		if result.Networks[i].RSSI == result.Networks[j].RSSI {
			return result.Networks[i].SSID < result.Networks[j].SSID
		}
		return result.Networks[i].RSSI > result.Networks[j].RSSI
	})
	return result, nil
}

func scanEntries(text string) map[string]Scanned {
	strongest := make(map[string]Scanned)
	for line := range strings.SplitSeq(text, "\n") {
		row := strings.SplitN(line, "\t", 5)
		if len(row) != 5 {
			continue
		}
		rssi, err := strconv.Atoi(row[2])
		ssid, ok := decodeSSID(row[4])
		if err != nil || !ok || ssid == "" || !utf8.ValidString(ssid) {
			continue
		}
		n, exists := strongest[ssid]
		if exists && n.RSSI >= rssi {
			continue
		}
		owe := strings.Contains(row[3], "-OWE-")
		secure := strings.Contains(row[3], "WPA") || strings.Contains(row[3], "WEP") || strings.Contains(row[3], "RSN") || strings.Contains(row[3], "OWE")
		strongest[ssid] = Scanned{SSID: ssid, RSSI: rssi, Secure: secure && !owe, enhancedOpen: owe}
	}
	return strongest
}
