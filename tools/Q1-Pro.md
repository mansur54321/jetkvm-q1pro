# Q1 Pro port notes

The port targets the OrayBox XRK-Q1Pro with an RV1106 and factory system `1.0.0_87160a`. The stable build starts JetKVM under BusyBox and uses the board's stock `5.10.160-android` kernel, hardware firmware and compatible drivers. The device runs in headless mode, with the browser providing the control interface.

## Hardware paths

- **Video.** The LT7911D receives USB-C DisplayPort. The Rockchip CIF driver delivers NV16 buffers to Rockit VENC using DMA. Return a capture buffer only when the encoder has finished with it. Rockit maps each output packet at its payload. The reported offset is its position in the encoder ring.
- **Audio.** The `rockchipdpin` ALSA capture device supplies 48 kHz signed 16-bit stereo. The source computer must select its DisplayPort audio output. The application restarts a prepared capture stream after XRUN and reopens it if the signal clock stalls.
- **HID.** The Linux USB gadget exposes an eight-byte keyboard, absolute and relative mice, and a wake-up interface. The factory application has a separate 44-byte multi-touch function for phones. R4 has no multi-touch gadget.
- **Networking.** Ethernet and Wi-Fi run locally. WireGuard uses userspace networking with an HTTP and WebRTC path scoped to the configured tunnel. Tailscale is an optional userspace add-on. The LED driver and Bluetooth setup use the board GPIO and UART resources.

| Feature | Hardware coverage |
| --- | --- |
| Local login, USB keyboard and mouse, H.264 video, DisplayPort audio | Tested on a physical XRK-Q1Pro and a Linux host |
| WireGuard | Tested with an independent peer and browser WebRTC |
| ISO download and USB CD/DVD | A 3 MB HTTPS ISO was downloaded and read back byte-for-byte by the host |
| Wi-Fi and Bluetooth | The board scanned 2.4 and 5 GHz networks and advertised the Improv service after a physical button gesture. Complete provisioning against a new network was tested in an ARM fixture |
| Android as the controlled computer | DisplayPort video worked on a OnePlus 12, but that phone did not enumerate USB |
| HTTP Range ISO and FunctionFS OS Services | Tested with a separate kernel in ARM QEMU. See [remote media notes](q1pro-remote-media/README.md) |

## Test the media and network paths

~~~sh
JETKVM_HEADLESS=1 go test -count=1 . ./internal/native
tools/tests/test-video-lifecycle.sh
tools/tests/webrtc/run.sh
tools/tests/webrtc/run.sh --clock-recovery
tools/tests/test-q1-wifi.sh
~~~

See the [R4 feature guide](Q1-Pro-R4.md), [LED states](q1pro-image/leds.md), [USB development guide](q1pro-usb/README.md), and [headless boot tests](q1pro-system-test/README.md).
