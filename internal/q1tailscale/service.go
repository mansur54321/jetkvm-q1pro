package q1tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	ts "github.com/jetkvm/kvm/internal/tailscale"
)

type Service struct {
	dir, runDir string
	client      *http.Client
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	controlURL  string
}

func New(dir, runDir string) *Service {
	s := &Service{dir: dir, runDir: runDir}
	s.client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.socket())
		},
	}}
	return s
}

func (s *Service) socket() string { return filepath.Join(s.runDir, "tailscaled.sock") }

func (s *Service) Enabled() bool {
	info, err := os.Lstat(filepath.Join(s.dir, "enabled"))
	return err == nil && info.Mode().IsRegular()
}

func (s *Service) call(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tailscaled %s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (s *Service) Status(controlURL string) (*ts.Status, error) {
	info := &ts.AddonInfo{Enabled: s.Enabled(), Version: Version, ArchiveBytes: ArchiveBytes, RequiredBytes: DaemonBytes}
	info.FreeBytes, _ = availableBytes(filepath.Dir(s.dir))
	status := &ts.Status{Installed: s.installed(), ControlURL: ts.EffectiveControlURL(controlURL), Addon: info}
	if !info.Enabled || !status.Installed {
		return status, nil
	}
	data, err := s.call(context.Background(), "GET", "status?peers=false", nil)
	if err != nil {
		status.BackendState = "Starting"
		return status, nil
	}
	status, err = ts.ParseStatus(data, controlURL)
	if err != nil {
		return nil, err
	}
	status.Addon = info
	return status, nil
}

func (s *Service) Login() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Enabled() || s.cancel == nil {
		return errors.New("enable Tailscale before signing in")
	}
	return s.interactiveLogin(context.Background())
}

func (s *Service) interactiveLogin(ctx context.Context) error {
	_, err := s.call(ctx, "POST", "login-interactive", nil)
	return err
}

func (s *Service) configure(ctx context.Context, controlURL string) error {
	status, err := s.call(ctx, "GET", "status?peers=false", nil)
	if err != nil {
		return err
	}
	var st struct{ HaveNodeKey bool }
	if err := json.Unmarshal(status, &st); err != nil {
		return err
	}
	prefs, err := s.call(ctx, "GET", "prefs", nil)
	if err != nil {
		return err
	}
	var current struct{ ControlURL string }
	if err := json.Unmarshal(prefs, &current); err != nil {
		return err
	}
	want := ts.EffectiveControlURL(controlURL)
	run := []byte(`{"WantRunning":true,"WantRunningSet":true}`)
	// Like a bare "tailscale up", resume an existing identity without restarting its control client.
	if st.HaveNodeKey && current.ControlURL == want {
		_, err = s.call(ctx, "PATCH", "prefs", run)
		return err
	}
	data, err := json.Marshal(map[string]any{"ControlURL": want, "ControlURLSet": true, "CorpDNS": false, "CorpDNSSet": true,
		"RouteAll": false, "RouteAllSet": true, "NetfilterMode": 0, "NetfilterModeSet": true})
	if err != nil {
		return err
	}
	if _, err = s.call(ctx, "PATCH", "prefs", data); err != nil {
		return err
	}
	// Start reloads the newly stored control server before the first login.
	if _, err = s.call(ctx, "POST", "start", []byte("{}")); err != nil {
		return err
	}
	if _, err = s.call(ctx, "PATCH", "prefs", run); err != nil {
		return err
	}
	if !st.HaveNodeKey || current.ControlURL != want {
		return s.interactiveLogin(ctx)
	}
	return nil
}

func (s *Service) runProcess(ctx context.Context, controlURL string, ready func(error)) error {
	// Linux sends Pdeathsig when the thread that started the child exits.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	stateDir := filepath.Join(s.dir, "state")
	cmd := exec.CommandContext(ctx, s.binary(), "--tun=userspace-networking", "--socket="+s.socket(),
		"--statedir="+stateDir, "--state="+filepath.Join(stateDir, "tailscaled.state"), "--no-logs-no-support", "--port=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(150 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case err := <-exit:
			if err == nil {
				err = errors.New("tailscaled exited")
			}
			return err
		case <-ctx.Done():
			<-exit
			return ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-exit
			return errors.New("timed out waiting for the Tailscale local API")
		case <-poll.C:
			if _, err := s.call(ctx, "GET", "status?peers=false", nil); err != nil {
				continue
			}
			if err := s.configure(ctx, controlURL); err != nil {
				_ = cmd.Process.Kill()
				<-exit
				return err
			}
			ready(nil)
			return <-exit
		}
	}
}

func (s *Service) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
}

func (s *Service) Resume(controlURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Enabled() {
		return nil
	}
	return s.setEnabledLocked(true, controlURL)
}

func (s *Service) SetEnabled(enabled bool, controlURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setEnabledLocked(enabled, controlURL)
}

func (s *Service) setEnabledLocked(enabled bool, controlURL string) error {
	marker := filepath.Join(s.dir, "enabled")
	if st, err := os.Lstat(marker); err == nil && !st.Mode().IsRegular() {
		return errors.New("unexpected file at Tailscale enable marker")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !enabled {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.stopLocked()
		return nil
	}
	if s.cancel != nil && s.controlURL == controlURL {
		return nil
	}
	if err := s.verifyBinary(); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if err := privateDir(filepath.Join(s.dir, "state")); err != nil {
		return err
	}
	if err := privateDir(s.runDir); err != nil {
		return err
	}
	s.stopLocked()
	ctx, cancel := context.WithCancel(context.Background())
	bootstrap := make(chan error, 1)
	done := make(chan struct{})
	s.cancel, s.done, s.controlURL = cancel, done, controlURL
	go func() {
		defer close(done)
		var once sync.Once
		ready := func(err error) { once.Do(func() { bootstrap <- err }) }
		for ctx.Err() == nil {
			err := s.runProcess(ctx, controlURL, ready)
			ready(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	if err := <-bootstrap; err != nil {
		s.stopLocked()
		return err
	}
	if !s.Enabled() {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			err = f.Close()
		}
		if err != nil {
			s.stopLocked()
			return err
		}
	}
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}
