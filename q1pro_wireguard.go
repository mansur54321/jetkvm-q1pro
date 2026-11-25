package kvm

import (
	"errors"

	"github.com/jetkvm/kvm/internal/q1vpn"
)

var q1WireGuard = q1vpn.New("/userdata/jetkvm/wireguard.json")

func rpcGetQ1WireGuard() (q1vpn.Status, error) {
	if !isQ1Pro() {
		return q1vpn.Status{}, errors.New("WireGuard profile is available on Q1 Pro")
	}
	return q1WireGuard.Status()
}

func rpcSetQ1WireGuard(settings q1vpn.Settings) error {
	if !isQ1Pro() {
		return errors.New("WireGuard profile is available on Q1 Pro")
	}
	if settings.Enabled && config.LocalLoopbackOnly {
		return errors.New("WireGuard access requires local network access")
	}
	return q1WireGuard.Update(settings)
}

func rpcImportQ1WireGuard(profile string) error {
	if !isQ1Pro() || config.LocalLoopbackOnly {
		return errors.New("WireGuard requires Q1 Pro with local network access")
	}
	return q1WireGuard.Import(profile)
}
