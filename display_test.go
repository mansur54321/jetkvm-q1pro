package kvm

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestHeadlessSkipsLocalDisplay(t *testing.T) {
	t.Setenv("JETKVM_HEADLESS", "1")
	synctest.Test(t, func(t *testing.T) {
		initDisplay()
		configureDisplayOnNativeRestart()
		requestDisplayUpdate(true, "test")
		startBacklightTickers()
		wakeDisplay(true, "test")

		// Let the normal delayed display initialization run if it was scheduled.
		time.Sleep(time.Second)
		if displayInited || dimTicker != nil || offTicker != nil {
			t.Fatal("headless process initialized local display or backlight timers")
		}
	})
}
