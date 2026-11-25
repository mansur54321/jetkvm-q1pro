package native

import (
	"os"
	"time"

	"github.com/jetkvm/kvm/internal/sync"

	"github.com/Masterminds/semver/v3"
	"github.com/jetkvm/kvm/internal/diagnostics"
	"github.com/rs/zerolog"
)

type Native struct {
	nativeDisplay
	ready                chan struct{}
	l                    *zerolog.Logger
	defaultQualityFactor float64
	retryVideoInit       bool
	startVideoWhenReady  bool
	onVideoStateChange   func(state VideoState)
	onVideoFrameReceived func(frame []byte, pts time.Duration)
	onIndevEvent         func(event string)
	onRpcEvent           func(event string)
	sleepModeSupported   bool
	videoLock            sync.Mutex
	screenLock           sync.Mutex
	extraLock            sync.Mutex
}

type NativeOptions struct {
	SystemVersion        *semver.Version
	AppVersion           *semver.Version
	DisplayRotation      uint16
	DefaultQualityFactor float64
	RetryVideoInit       bool
	MaxRestartAttempts   uint
	OnVideoStateChange   func(state VideoState)
	OnVideoFrameReceived func(frame []byte, pts time.Duration)
	OnIndevEvent         func(event string)
	OnRpcEvent           func(event string)
	OnNativeRestart      func()
	// GetSessionInfo returns session diagnostics for crash logging.
	GetSessionInfo func() diagnostics.SessionInfo
}

type VideoStreamingStatus uint8

const (
	VideoStreamingStatusActive   VideoStreamingStatus = 1
	VideoStreamingStatusStopping VideoStreamingStatus = 2 // video is stopping, but not yet stopped
	VideoStreamingStatusInactive VideoStreamingStatus = 0
)

func (s VideoStreamingStatus) String() string {
	switch s {
	case VideoStreamingStatusActive:
		return "active"
	case VideoStreamingStatusStopping:
		return "stopping"
	case VideoStreamingStatusInactive:
		return "inactive"
	}
	return "unknown"
}

func NewNative(opts NativeOptions) *Native {
	pid := os.Getpid()
	nativeSubLogger := nativeLogger.With().Int("pid", pid).Str("scope", "native").Logger()
	displaySubLogger := displayLogger.With().Int("pid", pid).Str("scope", "native").Logger()

	onVideoStateChange := opts.OnVideoStateChange
	if onVideoStateChange == nil {
		onVideoStateChange = func(state VideoState) {
			nativeLogger.Info().Interface("state", state).Msg("video state changed")
		}
	}

	onVideoFrameReceived := opts.OnVideoFrameReceived
	if onVideoFrameReceived == nil {
		onVideoFrameReceived = func(frame []byte, pts time.Duration) {
			nativeLogger.Trace().Interface("frame", frame).Dur("pts", pts).Msg("video frame received")
		}
	}

	onIndevEvent := opts.OnIndevEvent
	if onIndevEvent == nil {
		onIndevEvent = func(event string) {
			nativeLogger.Info().Str("event", event).Msg("indev event")
		}
	}

	onRpcEvent := opts.OnRpcEvent
	if onRpcEvent == nil {
		onRpcEvent = func(event string) {
			nativeLogger.Info().Str("event", event).Msg("rpc event")
		}
	}

	sleepModeSupported := isSleepModeSupported()

	defaultQualityFactor := opts.DefaultQualityFactor
	if defaultQualityFactor <= 0 || defaultQualityFactor > 1 {
		defaultQualityFactor = 1.0
	}

	return &Native{
		ready:                make(chan struct{}),
		l:                    &nativeSubLogger,
		nativeDisplay:        newNativeDisplay(opts, &displaySubLogger),
		defaultQualityFactor: defaultQualityFactor,
		retryVideoInit:       opts.RetryVideoInit,
		onVideoStateChange:   onVideoStateChange,
		onVideoFrameReceived: onVideoFrameReceived,
		onIndevEvent:         onIndevEvent,
		onRpcEvent:           onRpcEvent,
		sleepModeSupported:   sleepModeSupported,
		videoLock:            sync.Mutex{},
		screenLock:           sync.Mutex{},
	}
}

func (n *Native) Start() error {
	// set up singleton
	setInstance(n)
	if err := setUpNativeHandlers(); err != nil {
		return err
	}

	// start the native video
	go n.handleLogChan()
	go n.handleVideoStateChan()
	go n.handleVideoFrameChan()
	go n.handleIndevEventChan()
	go n.handleRpcEventChan()

	n.startDisplay()

	initialize := videoInit
	if n.retryVideoInit {
		initialize = func(factor float64) error {
			for _, device := range []string{"/dev/v4l-subdev2", "/dev/video0"} {
				if _, err := os.Stat(device); err != nil {
					return err
				}
			}
			return videoInit(factor)
		}
	}
	return n.initializeVideo(initialize, 3*time.Second)
}

func (n *Native) initializeVideo(init func(float64) error, retryDelay time.Duration) error {
	try := func() error {
		n.videoLock.Lock()
		defer n.videoLock.Unlock()
		if err := init(n.defaultQualityFactor); err != nil {
			return err
		}
		close(n.ready)
		if n.startVideoWhenReady {
			n.startVideoWhenReady = false
			n.startVideoLocked()
		}
		return nil
	}
	if !n.retryVideoInit {
		return try()
	}
	// Bring up the control plane even when the capture devices have not probed yet.
	go func() {
		if err := try(); err != nil {
			n.l.Warn().Err(err).Msg("video capture unavailable, waiting for the driver")
			n.onVideoStateChange(VideoState{Error: "device_unavailable"})
			for {
				time.Sleep(retryDelay)
				if try() == nil {
					n.l.Info().Msg("video capture available")
					return
				}
			}
		}
	}()
	return nil
}

func (n *Native) videoInitialized() bool {
	select {
	case <-n.ready:
		return true
	default:
		return false
	}
}

// DoNotUseThisIsForCrashTestingOnly
// will crash the program in cgo code
func (n *Native) DoNotUseThisIsForCrashTestingOnly() {
	defer func() {
		if r := recover(); r != nil {
			n.l.Error().Msg("recovered from crash")
		}
	}()

	crash()
}

// GetLVGLVersion returns the LVGL version
func GetLVGLVersion() string {
	return uiGetLVGLVersion()
}
