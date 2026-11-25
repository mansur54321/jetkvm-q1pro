# R4 features

## Network access and mobile setup

Open the Q1 Pro address in a desktop or mobile browser and sign in with the device's local password. The mobile browser can use the on-screen keyboard to send input to the attached computer. Third-party mobile JetKVM apps may use different protocol features. Compatibility with those apps has yet to be verified. [Upstream mobile FAQ](https://jetkvm.com/docs/getting-started/faq)

- **WireGuard.** Import a client profile or enter the tunnel address, endpoint and peer key in Network settings. Register the Q1 Pro's public key with your VPN server. The port supports one peer and one IPv4 tunnel address. Peers that open the interface need a route to that address and permission to reach TCP 80 and UDP 51821. [WireGuard documentation](https://www.wireguard.com/quickstart/)
- **Tailscale.** Install the matching ARM release from Network settings, start the service and authorize it in your tailnet. [Tailscale add-on](q1pro-tailscale/README.md)
- **Wi-Fi.** Configure a network locally or pass its credentials over Bluetooth LE with physical confirmation. [Phone setup](q1pro-ble/README.md)
- **Indicators.** The RJ45 lights indicate link and traffic. On the case, green indicates Ethernet and blue indicates Wi-Fi or Bluetooth setup. The blink pattern identifies the current Bluetooth state. [LED reference](q1pro-image/leds.md)

If the clock resets after a power cycle, add an NTP server in **Settings → Network → Time Synchronization**. Choose a server the Q1 Pro can reach as soon as its network comes up. WireGuard handshakes depend on timestamps.

## Video and audio

Use H.264 for broad browser support. H.265 requires a receiver that advertises H.265 in WebRTC. Mirror the host display if you want the KVM view to match the built-in laptop screen. The port can capture audio from USB-C DisplayPort. Select the display audio output on the host or configure the host to duplicate its audio there.

R4 fixes a capture-buffer ownership problem and reads the payload directly from the Rockit packet mapping. It restarts the ALSA capture stream after an underrun and reopens the device if its capture clock stalls.

## Virtual media

Select **Virtual Media** in the browser. Upload a small ISO or use **Download from URL** to load it into device storage, then mount it as CD/DVD. Available space depends on the user volume. A typical Linux or Windows installation ISO will exceed it. For network installs, a small iPXE ISO can boot the installer on a compatible machine with Ethernet. [netboot.xyz download](https://netboot.xyz/downloads/)

HTTP Range streaming and OS Services require kernel features that are absent from the stable factory kernel. A separate [experimental build](q1pro-remote-media/README.md) exercises both in QEMU.

## MCP

A third-party JetKVM MCP server can query status, take a screenshot and read screen text. Keyboard and mouse input require opt-in control. Taking over a session through MCP may disconnect the existing browser session. [JetKVM MCP source](https://github.com/LeeroyDing/jetkvm-mcp/tree/c066bbe9bc1bd0e6f399b02c45e956f815cd91cb)
