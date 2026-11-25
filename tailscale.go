package kvm

import (
	"errors"
	"fmt"

	"github.com/jetkvm/kvm/internal/tailscale"
)

func rpcGetTailscaleStatus() (*tailscale.Status, error) {
	ensureConfigLoaded()
	if isQ1Pro() {
		return q1Tailscale.Status(config.TailscaleControlURL)
	}
	return tailscale.GetStatus(config.TailscaleControlURL, func(err error) {
		tailscaleLogger.Warn().Err(err).Msg("failed to get tailscale status")
	})
}

func rpcGetTailscaleControlURL() (string, error) {
	ensureConfigLoaded()
	return tailscale.EffectiveControlURL(config.TailscaleControlURL), nil
}

func rpcSetTailscaleControlURL(controlURL string) error {
	ensureConfigLoaded()

	previousURL := config.TailscaleControlURL

	apply := tailscale.SetControlURL
	if isQ1Pro() {
		apply = tailscale.NormalizeControlURL
	}
	normalized, err := apply(controlURL)
	if err != nil {
		return err
	}

	config.TailscaleControlURL = normalized
	if err := SaveConfig(); err != nil {
		config.TailscaleControlURL = previousURL
		return fmt.Errorf("failed to save tailscale control URL: %w", err)
	}

	if isQ1Pro() && q1Tailscale.Enabled() {
		if err := q1Tailscale.SetEnabled(true, normalized); err != nil {
			config.TailscaleControlURL = previousURL
			return errors.Join(err, SaveConfig(), q1Tailscale.SetEnabled(true, previousURL))
		}
	}
	return nil
}
