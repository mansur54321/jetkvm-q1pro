package native

import (
	"testing"

	"github.com/rs/zerolog"
)

func TestHeadlessDisplay(t *testing.T) {
	t.Setenv("JETKVM_HEADLESS", "1")
	logger := zerolog.Nop()
	n := &Native{nativeDisplay: newNativeDisplay(NativeOptions{}, &logger)}

	n.startDisplay()
	n.UISetVar("main_screen", "home_screen")
	n.SwitchToScreenIfDifferent("home_screen")
	n.UpdateLabelAndChangeVisibility("hdmi_status_label", "Connected")
	if value := n.UIGetVar("main_screen"); value != "" {
		t.Errorf("headless display stored a variable: %q", value)
	}
	if changed, err := n.DisplaySetRotation(90); changed || err != nil {
		t.Errorf("headless rotation returned (%t, %v)", changed, err)
	}
}

func TestDefaultFramebufferDisplay(t *testing.T) {
	t.Setenv("JETKVM_HEADLESS", "")
	logger := zerolog.Nop()
	display := newNativeDisplay(NativeOptions{DisplayRotation: 90}, &logger)
	lcd, ok := display.(*framebufferDisplay)
	if !ok || lcd.rotation != 90 {
		t.Fatalf("expected the framebuffer display with configured rotation, got %#v", display)
	}
}
