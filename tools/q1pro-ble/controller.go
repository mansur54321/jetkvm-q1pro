package q1ble

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Controller struct {
	mu        sync.Mutex
	grantPath string
	radio     func(context.Context, *session) error
	claimed   func() bool
	provision func(context.Context, string, string) (string, error)
	window    *session
	cancel    context.CancelFunc
	lastTap   time.Time
	taps      int
	failure   bool
}

type Status struct {
	PairCode     string `json:"pairCode,omitempty"`
	HandoffUntil int64  `json:"handoffUntil"`
	Active       bool   `json:"active"`
	State        byte   `json:"state"`
	RadioFailed  bool   `json:"radioFailed"`
}

func New(grantPath string, claimed func() bool, provision func(context.Context, string, string) (string, error)) *Controller {
	return &Controller{radio: runPeripheral, grantPath: grantPath, claimed: claimed, provision: provision}
}

func syncGrantDirectory(file string) error {
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (c *Controller) handoffUntil(now time.Time) int64 {
	b, err := os.ReadFile(c.grantPath)
	if err != nil {
		return 0
	}
	exp, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || exp <= now.Unix() || exp > now.Add(24*time.Hour).Unix() {
		return 0
	}
	return exp
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{HandoffUntil: c.handoffUntil(time.Now()), RadioFailed: c.failure}
	if c.window != nil {
		s.Active = true
		s.State = c.window.read().state
		s.PairCode = c.window.passkey()
	}
	return s
}

// Handoff is called by the authenticated local control session. On a clock
// rollback the grant expires; the owner at the device can still use the physical gesture.
func (c *Controller) Handoff(enable bool) (Status, error) {
	c.mu.Lock()
	if !enable {
		err := os.Remove(c.grantPath)
		if err == nil {
			err = syncGrantDirectory(c.grantPath)
		}
		if c.cancel != nil {
			c.cancel()
		}
		c.mu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Status{}, err
		}
		return c.Status(), nil
	}
	f, err := os.CreateTemp(filepath.Dir(c.grantPath), ".wifi-handoff-")
	if err == nil {
		defer os.Remove(f.Name())
		exp := time.Now().Add(24 * time.Hour).Unix()
		_, err = f.WriteString(strconv.FormatInt(exp, 10) + "\n")
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(f.Name(), c.grantPath)
			if err == nil {
				err = syncGrantDirectory(c.grantPath)
			}
		}
	}
	c.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return c.Status(), nil
}

func (c *Controller) tap(ctx context.Context, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window != nil {
		c.window.authorize()
		return
	}
	if gap := now.Sub(c.lastTap); gap < 0 || gap > 750*time.Millisecond {
		c.taps = 0
	}
	c.lastTap = now
	c.taps++
	armed := c.handoffUntil(time.Now()) != 0
	target := 2
	if c.claimed() && !armed {
		target = 5
	}
	if c.taps != target {
		return
	}
	c.taps = 0
	if armed {
		if err := os.Remove(c.grantPath); err != nil || syncGrantDirectory(c.grantPath) != nil {
			return
		}
	}
	window, cancel := context.WithTimeout(ctx, 120*time.Second)
	s := newSession(window, c.provision)
	c.window, c.cancel, c.failure = s, cancel, false
	go func() {
		err := c.radio(window, s)
		c.mu.Lock()
		c.failure = err != nil && window.Err() == nil
		c.window, c.cancel = nil, nil
		c.mu.Unlock()
		cancel()
	}()
}
