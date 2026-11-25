package kvm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jetkvm/kvm/internal/confparser"
	"github.com/jetkvm/kvm/internal/network/types"
	"github.com/jetkvm/kvm/internal/ota"
	"github.com/jetkvm/kvm/pkg/myip"
	"github.com/jetkvm/kvm/pkg/nmlite"
	"github.com/jetkvm/kvm/pkg/nmlite/link"
)

const (
	NetIfName = "eth0"
)

var (
	networkManager *nmlite.NetworkManager
	publicIPState  *myip.PublicIPState
)

type RpcNetworkSettings struct {
	types.NetworkConfig
	FixedDHCPClient bool `json:"fixed_dhcp_client,omitempty"`
}

func (s *RpcNetworkSettings) ToNetworkConfig() *types.NetworkConfig {
	return &s.NetworkConfig
}

type PostRebootAction struct {
	HealthCheck string `json:"healthCheck"`
	RedirectTo  string `json:"redirectTo"`
}

func toRpcNetworkSettings(config *types.NetworkConfig) *RpcNetworkSettings {
	return &RpcNetworkSettings{
		NetworkConfig:   *config,
		FixedDHCPClient: isQ1Pro(),
	}
}

func restartMdns() {
	if mDNS == nil {
		return
	}

	options := getMdnsOptions()
	if options == nil {
		return
	}

	if err := mDNS.SetOptions(options); err != nil {
		networkLogger.Error().Err(err).Msg("failed to restart mDNS")
	}
}

func triggerTimeSyncOnNetworkStateChange() {
	if timeSync == nil {
		return
	}

	// set the NTP servers from the network manager
	if networkManager != nil {
		ntpServers := networkManager.NTPServerStrings()
		networkLogger.Info().Strs("ntpServers", ntpServers).Msg("setting NTP servers from network manager")
		timeSync.SetDhcpNtpAddresses(ntpServers)
	}

	// sync time
	go func() {
		if err := timeSync.Sync(); err != nil {
			networkLogger.Error().Err(err).Msg("failed to sync time after network state change")
		}
	}()
}

func setPublicIPReadyState(ipv4Ready, ipv6Ready bool) {
	if publicIPState == nil {
		return
	}
	publicIPState.SetIPv4AndIPv6(ipv4Ready, ipv6Ready)
}

func networkStateChanged(_ string, state types.InterfaceState) {
	// do not block the main thread
	go waitCtrlAndRequestDisplayUpdate(false, "network_state_changed")

	if currentSession != nil {
		writeJSONRPCEvent("networkState", rpcGetNetworkState(), currentSession)
	}

	if state.Online {
		networkLogger.Info().Msg("network state changed to online, triggering time sync")
		triggerTimeSyncOnNetworkStateChange()
	}

	setPublicIPReadyState(networkManager.IPv4Ready(), networkManager.IPv6Ready())

	// always restart mDNS when the network state changes
	if mDNS != nil {
		restartMdns()
	}
}

func validateNetworkConfig() {
	err := confparser.SetDefaultsAndValidate(config.NetworkConfig)
	if err == nil {
		return
	}

	networkLogger.Error().Err(err).Msg("failed to validate config, reverting to default config")
	if err := SaveBackupConfig(); err != nil {
		networkLogger.Error().Err(err).Msg("failed to save backup config")
	}

	// do not use a pointer to the default config
	// it has been already changed during LoadConfig
	config.NetworkConfig = &(types.NetworkConfig{})
	if err := SaveConfig(); err != nil {
		networkLogger.Error().Err(err).Msg("failed to save config")
	}
}

func initNetwork() error {
	ensureConfigLoaded()

	// validate the config, if it's invalid, revert to the default config and save the backup
	validateNetworkConfig()

	nc := config.NetworkConfig
	if isQ1Pro() {
		nc.DHCPClient = null.StringFrom("jetdhcpc")
	}

	nm := nmlite.NewNetworkManager(context.Background(), networkLogger)
	networkLogger.Info().Interface("networkConfig", nc).Str("hostname", nc.Hostname.String).Str("domain", nc.Domain.String).Msg("initializing network manager")
	_ = setHostname(nm, nc.Hostname.String, nc.Domain.String)
	networkManager = nm
	nm.SetOnInterfaceStateChange(networkStateChanged)
	if isQ1Pro() {
		if err := nm.WatchInterface(NetIfName, nc, 100); err != nil {
			return err
		}
		wifi := &types.NetworkConfig{DHCPClient: null.StringFrom("jetdhcpc")}
		if err := nm.WatchInterface("wlan0", wifi, 600); err != nil {
			_ = nm.Stop()
			return err
		}
		return nil
	}
	if err := nm.AddInterface(NetIfName, nc); err != nil {
		return fmt.Errorf("failed to add interface: %w", err)
	}
	_ = nm.CleanUpLegacyDHCPClients()

	return nil
}

func initPublicIPState() {
	// the feature will be only enabled if the cloud has been adopted
	// due to privacy reasons

	// but it will be initialized anyway to avoid nil pointer dereferences
	ps := myip.NewPublicIPState(&myip.PublicIPStateConfig{
		Logger:             networkLogger,
		CloudflareEndpoint: config.CloudURL,
		APIEndpoint:        "",
		IPv4:               false,
		IPv6:               false,
		HttpClientGetter: func(family int) *http.Client {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = config.NetworkConfig.GetTransportProxyFunc()
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				netType := network
				switch family {
				case link.AfInet:
					netType = "tcp4"
				case link.AfInet6:
					netType = "tcp6"
				}
				return (&net.Dialer{}).DialContext(ctx, netType, addr)
			}

			return &http.Client{
				Transport: transport,
				Timeout:   30 * time.Second,
			}
		},
	})
	publicIPState = ps
}

func setHostname(nm *nmlite.NetworkManager, hostname, domain string) error {
	if nm == nil {
		return nil
	}

	if hostname == "" {
		hostname = GetDefaultHostname()
	}

	return nm.SetHostname(hostname, domain)
}

func shouldRebootForNetworkChange(oldConfig, newConfig *types.NetworkConfig) (rebootRequired bool, postRebootAction *ota.PostRebootAction) {
	oldDhcpClient := oldConfig.DHCPClient.String

	l := networkLogger.With().
		Interface("old", oldConfig).
		Interface("new", newConfig).
		Logger()

	// DHCP client change always requires reboot
	if newConfig.DHCPClient.String != oldDhcpClient {
		rebootRequired = true
		l.Info().Msg("DHCP client changed, reboot required")
		return rebootRequired, postRebootAction
	}

	oldIPv4Mode := oldConfig.IPv4Mode.String
	newIPv4Mode := newConfig.IPv4Mode.String

	// IPv4 mode change requires reboot
	if newIPv4Mode != oldIPv4Mode {
		rebootRequired = true
		l.Info().Msg("IPv4 mode changed with udhcpc, reboot required")

		if newIPv4Mode == "static" && oldIPv4Mode != "static" {
			postRebootAction = &ota.PostRebootAction{
				HealthCheck: fmt.Sprintf("//%s/device/status", newConfig.IPv4Static.Address.String),
				RedirectTo:  fmt.Sprintf("//%s", newConfig.IPv4Static.Address.String),
			}
			l.Info().Interface("postRebootAction", postRebootAction).Msg("IPv4 mode changed to static, reboot required")
		}

		return rebootRequired, postRebootAction
	}

	// IPv4 static config changes require reboot
	if !reflect.DeepEqual(oldConfig.IPv4Static, newConfig.IPv4Static) {
		rebootRequired = true

		// Handle IP change for redirect (only if both are not nil and IP changed)
		if newConfig.IPv4Static != nil && oldConfig.IPv4Static != nil &&
			newConfig.IPv4Static.Address.String != oldConfig.IPv4Static.Address.String {
			postRebootAction = &ota.PostRebootAction{
				HealthCheck: fmt.Sprintf("//%s/device/status", newConfig.IPv4Static.Address.String),
				RedirectTo:  fmt.Sprintf("//%s", newConfig.IPv4Static.Address.String),
			}

			l.Info().Interface("postRebootAction", postRebootAction).Msg("IPv4 static config changed, reboot required")
		}

		return rebootRequired, postRebootAction
	}

	// IPv6 mode change requires reboot when using udhcpc
	if newConfig.IPv6Mode.String != oldConfig.IPv6Mode.String && oldDhcpClient == "udhcpc" {
		rebootRequired = true
		l.Info().Msg("IPv6 mode changed with udhcpc, reboot required")
	}

	if newConfig.Hostname.String != oldConfig.Hostname.String {
		rebootRequired = true
		l.Info().Msg("Hostname changed, reboot required")
	}

	return rebootRequired, postRebootAction
}

func rpcGetNetworkState() *types.RpcInterfaceState {
	return networkManager.PrimaryState().ToRpcInterfaceState()
}

func rpcGetNetworkSettings() *RpcNetworkSettings {
	return toRpcNetworkSettings(config.NetworkConfig)
}

func rpcSetNetworkSettings(settings RpcNetworkSettings) (*RpcNetworkSettings, error) {
	netConfig := settings.ToNetworkConfig()
	if isQ1Pro() {
		if netConfig.DHCPClient.Valid && netConfig.DHCPClient.String != "jetdhcpc" {
			return nil, fmt.Errorf("Q1 Pro requires the built-in jetdhcpc client")
		}
		netConfig.DHCPClient = null.StringFrom("jetdhcpc")
	}

	l := networkLogger.With().
		Str("interface", NetIfName).
		Interface("newConfig", netConfig).
		Logger()

	l.Debug().Msg("setting new config")

	// Check if reboot is needed
	if err := confparser.SetDefaultsAndValidate(netConfig); err != nil {
		return nil, err
	}
	rebootRequired, postRebootAction := shouldRebootForNetworkChange(config.NetworkConfig, netConfig)
	if isQ1Pro() && rebootRequired && config.NetworkConfig.Hostname == netConfig.Hostname {
		ethernet, err := networkManager.GetInterfaceState(NetIfName)
		if err == nil && !ethernet.Up {
			// The Ethernet configuration is staged until carrier. Keep the Wi-Fi session alive.
			rebootRequired, postRebootAction = false, nil
		}
	}

	// If reboot required, send willReboot event before applying network config
	if rebootRequired {
		l.Info().Msg("Sending willReboot event before applying network config")
		writeJSONRPCEvent("willReboot", postRebootAction, currentSession)
	}

	_ = setHostname(networkManager, netConfig.Hostname.String, netConfig.Domain.String)

	s := networkManager.SetInterfaceConfig(NetIfName, netConfig)
	if s != nil {
		return nil, s
	}
	l.Debug().Msg("new config applied")

	newConfig, err := networkManager.GetInterfaceConfig(NetIfName)
	if err != nil {
		return nil, err
	}
	config.NetworkConfig = newConfig

	if timeSync != nil {
		timeSync.SetNetworkConfig(newConfig)
		go func() {
			if err := timeSync.Sync(); err != nil {
				l.Error().Err(err).Msg("failed to sync time after network settings change")
			}
		}()
	}

	l.Debug().Msg("saving new config")
	if err := SaveConfig(); err != nil {
		return nil, err
	}

	if rebootRequired {
		l.Info().Msg("Rebooting due to network changes")
		if err := hwReboot(true, postRebootAction, 0); err != nil {
			return nil, err
		}
	}

	return toRpcNetworkSettings(newConfig), nil
}

func rpcRenewDHCPLease() error {
	iface := NetIfName
	if isQ1Pro() {
		iface = networkManager.PrimaryState().InterfaceName
	}
	return networkManager.RenewDHCPLease(iface)
}

func rpcToggleDHCPClient() error {
	if isQ1Pro() {
		return fmt.Errorf("Q1 Pro requires the built-in jetdhcpc client")
	}
	switch config.NetworkConfig.DHCPClient.String {
	case "jetdhcpc":
		config.NetworkConfig.DHCPClient.String = "udhcpc"
	case "udhcpc":
		config.NetworkConfig.DHCPClient.String = "jetdhcpc"
	}

	if err := SaveConfig(); err != nil {
		return err
	}

	return rpcReboot(true)
}

func rpcGetPublicIPAddresses(refresh bool) ([]myip.PublicIP, error) {
	if publicIPState == nil {
		return nil, fmt.Errorf("public IP state not initialized")
	}

	// Only allow refresh when cloud is adopted, to avoid sending requests
	// to api.jetkvm.com when the user has not opted into cloud features.
	if refresh && config.CloudToken != "" && config.CloudURL != "" {
		if err := publicIPState.ForceUpdate(); err != nil {
			return nil, err
		}
	}

	return publicIPState.GetAddresses(), nil
}

func rpcCheckPublicIPAddresses() error {
	if publicIPState == nil {
		return fmt.Errorf("public IP state not initialized")
	}

	// Only check public IPs when cloud is adopted.
	if config.CloudToken == "" || config.CloudURL == "" {
		return fmt.Errorf("cloud is not enabled")
	}

	return publicIPState.ForceUpdate()
}
