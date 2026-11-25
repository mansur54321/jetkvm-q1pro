package native

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestVideoInitFailureOnDefaultBoard(t *testing.T) {
	n := NewNative(NativeOptions{})
	want := errors.New("missing V4L2")
	if err := n.initializeVideo(func(float64) error { return want }, time.Second); !errors.Is(err, want) {
		t.Fatalf("initialization returned %v", err)
	}
	if n.videoInitialized() {
		t.Fatal("uninitialized device reported ready")
	}
}

func TestQ1DriverAppearsAfterControlPlaneStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var observed []VideoState
		n := NewNative(NativeOptions{RetryVideoInit: true, OnVideoStateChange: func(state VideoState) {
			observed = append(observed, state)
		}})
		var attempts int
		var factor float64
		err := n.initializeVideo(func(current float64) error {
			attempts++
			if attempts < 3 {
				return errors.New("missing V4L2")
			}
			factor = current
			return nil
		}, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n.videoInitialized() || len(observed) != 1 || observed[0].Error != "device_unavailable" {
			t.Fatalf("initial video state %+v", observed)
		}
		if err := n.VideoStart(); err != nil || !n.startVideoWhenReady {
			t.Fatal("session failed to request video during initialization")
		}
		if err := n.VideoStop(); err != nil || n.startVideoWhenReady {
			t.Fatal("stopped session kept its pending video request")
		}
		if err := n.VideoSetQualityFactor(.7); err != nil {
			t.Fatal(err)
		}
		<-n.ready
		synctest.Wait()
		if factor != .7 || attempts != 3 || len(observed) != 1 {
			t.Fatalf("factor %.2f, attempts %d, states %+v", factor, attempts, observed)
		}
	})
}

func TestQ1VideoRetryCrossesProcessBoundary(t *testing.T) {
	if !(&NativeOptions{RetryVideoInit: true}).toProxyOptions().toNativeOptions().RetryVideoInit {
		t.Fatal("Q1 retry flag lost by child option conversion")
	}
}
