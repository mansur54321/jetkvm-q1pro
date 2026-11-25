// Package q1vpn serves the Q1 web interface and ICE transport on a WireGuard
// address without requiring a kernel WireGuard or TUN driver.
package q1vpn

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type Settings struct {
	Enabled    bool     `json:"enabled"`
	Address    string   `json:"address"`
	PeerKey    string   `json:"peerKey"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowedIPs"`
}

type Status struct {
	Settings
	PublicKey     string `json:"publicKey"`
	Running       bool   `json:"running"`
	LastHandshake int64  `json:"lastHandshake"`
	Error         string `json:"error,omitempty"`
}

type saved struct {
	Settings
	PrivateKey   string  `json:"privateKey"`
	PresharedKey string  `json:"presharedKey,omitempty"`
	Keepalive    *uint16 `json:"keepalive,omitempty"`
	ListenPort   uint16  `json:"listenPort,omitempty"`
	MTU          int     `json:"mtu,omitempty"`
}

type Manager struct {
	mu        sync.Mutex
	path      string
	handler   http.Handler
	config    *saved
	active    *tunnel
	lastError string
}

type tunnel struct {
	device   *device.Device
	mux      ice.UDPMux
	server   *http.Server
	listener net.Listener
}

func New(path string) *Manager { return &Manager{path: path} }

func keyBytes(v string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(v)
	if err != nil || len(b) != 32 || bytes.Equal(b, make([]byte, 32)) || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n") {
		return nil, errors.New("expected a 32-byte base64 WireGuard key")
	}
	return b, nil
}

// Save in the same directory so a restart during configuration sees one complete profile.
func (m *Manager) write(config *saved) error {
	d := filepath.Dir(m.path)
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(d, ".wireguard-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(config)
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), m.path)
}

func (m *Manager) load(create bool) error {
	if m.config != nil {
		return nil
	}
	var cfg saved
	data, err := os.ReadFile(m.path)
	if err == nil {
		if err = json.Unmarshal(data, &cfg); err != nil {
			return errors.New("invalid saved WireGuard profile")
		}
		if _, err = keyBytes(cfg.PrivateKey); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil
		}
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		cfg.PrivateKey = base64.StdEncoding.EncodeToString(priv.Bytes())
		cfg.AllowedIPs = []string{}
		if err = m.write(&cfg); err != nil {
			return err
		}
	} else {
		return err
	}
	m.config = &cfg
	return nil
}

func (m *Manager) Status() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(true); err != nil {
		return Status{}, err
	}
	key, err := keyBytes(m.config.PrivateKey)
	if err != nil {
		return Status{}, err
	}
	priv, err := ecdh.X25519().NewPrivateKey(key)
	if err != nil {
		return Status{}, err
	}
	st := Status{Settings: m.config.Settings, PublicKey: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), Running: m.active != nil, Error: m.lastError}
	if m.active != nil {
		state, err := m.active.device.IpcGet()
		if err == nil {
			for _, line := range strings.Split(state, "\n") {
				if stamp, found := strings.CutPrefix(line, "last_handshake_time_sec="); found {
					st.LastHandshake, _ = strconv.ParseInt(stamp, 10, 64)
					break
				}
			}
		}
	}
	return st, nil
}

func start(ctx context.Context, cfg saved, handler http.Handler) (*tunnel, error) {
	address, err := netip.ParseAddr(cfg.Address)
	if err != nil || !address.IsGlobalUnicast() || address.Is4In6() {
		return nil, errors.New("enter the assigned WireGuard host IP without a subnet mask")
	}
	key, err := keyBytes(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	pub, err := keyBytes(cfg.PeerKey)
	if err != nil {
		return nil, err
	}
	if len(cfg.AllowedIPs) == 0 {
		return nil, errors.New("at least one allowed network is required")
	}
	if len(cfg.AllowedIPs) > 128 {
		return nil, errors.New("too many allowed networks")
	}
	var routes strings.Builder
	for _, r := range cfg.AllowedIPs {
		p, err := netip.ParsePrefix(strings.TrimSpace(r))
		if err != nil {
			return nil, fmt.Errorf("invalid allowed network %q", r)
		}
		fmt.Fprintf(&routes, "allowed_ip=%s\n", p.Masked())
	}
	host, portText, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil || host == "" {
		return nil, errors.New("expected endpoint host:port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid WireGuard endpoint port")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve WireGuard endpoint %q", host)
	}
	endpoint := netip.AddrPortFrom(ips[0].Unmap(), uint16(port))
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	tun, stack, err := netstack.CreateNetTUN([]netip.Addr{address}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("create WireGuard network: %w", err)
	}
	wg := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t := &tunnel{device: wg}
	ok := false
	defer func() {
		if !ok {
			t.close()
		}
	}()
	keepalive := uint16(25)
	if cfg.Keepalive != nil {
		keepalive = *cfg.Keepalive
	}
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nendpoint=%s\npersistent_keepalive_interval=%d\n%s", hex.EncodeToString(key), cfg.ListenPort, hex.EncodeToString(pub), endpoint, keepalive, routes.String())
	if cfg.PresharedKey != "" {
		psk, err := keyBytes(cfg.PresharedKey)
		if err != nil {
			return nil, errors.New("invalid WireGuard preshared key")
		}
		ipc += "preshared_key=" + hex.EncodeToString(psk) + "\n"
	}
	if err = wg.IpcSet(ipc); err != nil {
		return nil, errors.New("WireGuard rejected the peer configuration")
	}
	if err = wg.Up(); err != nil {
		return nil, fmt.Errorf("start WireGuard: %w", err)
	}
	udp, err := stack.ListenUDPAddrPort(netip.AddrPortFrom(address, 51821))
	if err != nil {
		return nil, err
	}
	t.mux = ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udp})
	t.listener, err = stack.ListenTCPAddrPort(netip.AddrPortFrom(address, 80))
	if err != nil {
		return nil, err
	}
	t.server = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, muxContextKey{}, t.mux)
	}}
	go func() { _ = t.server.Serve(t.listener) }()
	ok = true
	return t, nil
}

type muxContextKey struct{}

func MediaMux(ctx context.Context) ice.UDPMux {
	mux, _ := ctx.Value(muxContextKey{}).(ice.UDPMux)
	return mux
}

func (t *tunnel) close() {
	if t.server != nil {
		_ = t.server.Close()
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
	if t.mux != nil {
		_ = t.mux.Close()
	}
	t.device.Close()
}

// Run restores an enabled profile. DNS may be unavailable before DHCP completes.
func (m *Manager) Run(ctx context.Context, handler http.Handler) {
	m.mu.Lock()
	m.handler = handler
	m.mu.Unlock()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		err := m.load(false)
		if err == nil && m.config != nil && m.config.Enabled && m.active == nil {
			m.active, err = m.startWithTimeout(ctx, *m.config)
		}
		if err != nil {
			m.lastError = err.Error()
		} else if m.active != nil {
			m.lastError = ""
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if m.active != nil {
				m.active.close()
				m.active = nil
			}
			m.handler = nil
			m.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) startWithTimeout(ctx context.Context, cfg saved) (*tunnel, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return start(ctx, cfg, m.handler)
}
func (m *Manager) Update(settings Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(true); err != nil {
		return err
	}
	next := *m.config
	next.Settings = settings
	return m.apply(next)
}

func (m *Manager) apply(next saved) error {
	if m.handler == nil {
		return errors.New("WireGuard web listener is not available")
	}
	if m.config != nil && reflect.DeepEqual(next, *m.config) && next.Enabled == (m.active != nil) {
		return nil
	}
	// A specifically configured listen port cannot be held by both generations.
	rebind := m.active != nil && next.Enabled && next.ListenPort != 0 && next.ListenPort == m.config.ListenPort
	if rebind {
		m.active.close()
		m.active = nil
	}
	recoverOld := func(cause error) error {
		if rebind {
			var err error
			m.active, err = m.startWithTimeout(context.Background(), *m.config)
			if err != nil {
				m.lastError = err.Error()
				return errors.Join(cause, fmt.Errorf("restore previous WireGuard profile: %w", err))
			}
		}
		return cause
	}
	var replacement *tunnel
	var err error
	if next.Enabled {
		replacement, err = m.startWithTimeout(context.Background(), next)
		if err != nil {
			return recoverOld(err)
		}
	}
	if err = m.write(&next); err != nil {
		if replacement != nil {
			replacement.close()
		}
		return recoverOld(err)
	}
	old := m.active
	m.active = replacement
	m.config = &next
	m.lastError = ""
	if old != nil {
		old.close()
	}
	return nil
}
