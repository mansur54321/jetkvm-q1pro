package kvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/jetkvm/kvm/internal/q1wifi"
	q1ble "github.com/jetkvm/kvm/tools/q1pro-ble"
)

func registerQ1Wifi(ctx context.Context) {
	station := q1wifi.New("/run/wpa_supplicant/wlan0", "/userdata/jetkvm/wifi.conf")
	ble := q1ble.New("/userdata/jetkvm/ble-wifi-handoff", func() bool { return q1LocalAuthMode() != "" }, func(ctx context.Context, ssid, password string) (string, error) {
		var address string
		err := station.Provision(ssid, password, func() error {
			var err error
			address, err = q1WifiAddress(ctx)
			return err
		})
		return address, err
	})
	go ble.Run(ctx)
	rpcHandlers["getBluetoothWifiStatus"] = RPCHandler{Func: func() (q1ble.Status, error) {
		out := ble.Status()
		if q1LocalAuthMode() != "password" {
			out.PairCode = ""
		}
		return out, nil
	}}
	rpcHandlers["setBluetoothWifiHandoff"] = RPCHandler{Func: func(enable bool) (q1ble.Status, error) {
		if enable && q1LocalAuthMode() != "password" {
			return q1ble.Status{}, errors.New("a local password is required for remote Wi-Fi handoff")
		}
		out, err := ble.Handoff(enable)
		if q1LocalAuthMode() != "password" {
			out.PairCode = ""
		}
		return out, err
	}, Params: []string{"enable"}}

	rpcHandlers["getWifiSettings"] = RPCHandler{Func: station.Settings}
	rpcHandlers["scanWifiNetworks"] = RPCHandler{Func: station.Scan, Params: []string{"refresh"}}
	rpcHandlers["saveWifiNetwork"] = RPCHandler{
		Func: func(ssid, password string, connect bool) (map[string]bool, error) {
			connecting, err := station.Save(ssid, password, connect)
			return map[string]bool{"connecting": connecting}, err
		},
		Params: []string{"ssid", "password", "connect"},
	}
	rpcHandlers["removeWifiNetwork"] = RPCHandler{Func: station.Remove, Params: []string{"ssid"}}
	rpcHandlers["connectWifiNetwork"] = RPCHandler{Func: station.Connect, Params: []string{"ssid"}}
	rpcHandlers["setWifiBand"] = RPCHandler{Func: station.SetBand, Params: []string{"band"}}
}

func q1LocalAuthMode() string {
	f, err := os.Open(configPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	var status struct {
		LocalAuthMode string `json:"localAuthMode"`
	}
	if json.NewDecoder(f).Decode(&status) != nil {
		return "unknown"
	}
	return status.LocalAuthMode
}

func q1WifiAddress(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if iface, err := net.InterfaceByName("wlan0"); err == nil {
			addresses, _ := iface.Addrs()
			var v6 string
			for _, a := range addresses {
				ip, _, err := net.ParseCIDR(a.String())
				if err != nil || !ip.IsGlobalUnicast() {
					continue
				}
				if ip.To4() != nil {
					return "http://" + ip.String() + "/", nil
				}
				v6 = "http://[" + ip.String() + "]/"
			}
			if v6 != "" {
				return v6, nil
			}
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	return "", errors.New("Wi-Fi did not acquire a local network address")
}
