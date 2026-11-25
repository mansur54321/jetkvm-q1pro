# Set up Wi-Fi from a phone

The Q1 Pro receives the SSID and password over Bluetooth LE with physical confirmation.

1. On Android use Chrome. On iPhone use a browser with Web Bluetooth support, such as Bluefy. Open an HTTPS Improv client or serve the downloadable `q1pro-wifi.html` from a trusted HTTPS origin. [Improv client](https://toolbox.openhomefoundation.org/improv/) · [Bluefy](https://bluefy.app/)
2. On a fresh Q1 Pro, press the case button twice. To use two presses on a configured Q1 Pro, first select **Settings → Network → Allow one transfer** in the local admin session. The approval expires after 24 hours or one handoff. A configured board also accepts five quick presses for physical setup.
3. While the case light flashes blue, select **JetKVM Q1 Pro** on the phone. Press the case button once to authorize the connected phone. The setup window lasts up to two minutes.
4. Enter the SSID and password. If the phone asks to pair, confirm the prompt. When the case light flashes cyan, confirm with another short press. An open admin session can display the numeric comparison code.
5. Open the address returned after the device joins the network. If the new connection fails, the Q1 Pro reloads the previously saved network. The JetKVM administrator password remains in use.

## Transport

The AIC8800D80 Bluetooth device uses H4 UART at 1.5 Mbaud, with hardware flow control on `/dev/ttyS5`. Its BSP brings up the radio and loads the board firmware. The provisioning service starts BlueZ and a private D-Bus instance during the physical setup window. It scopes approval to the currently connected client and stores temporary pairing data in RAM.

The GATT service implements Improv Wi-Fi and requires BlueZ `secure-write` for credential updates. Wi-Fi profiles use a file mode of `0600`. [Improv Wi-Fi specification](https://www.improv-wifi.com/ble/)

## Testing

~~~sh
go test -race ./tools/q1pro-ble ./internal/q1wifi
tools/q1pro-image/test-led.sh
python3 tools/q1pro-ble/test-virtual.py --btvirt /path/to/bluez/build/emulator/btvirt
~~~

Build `btvirt` from BlueZ 5.66 with `--enable-testing` for ARM hard-float Linux and the SDK toolchain. The test runs a virtual BLE pair and `mac80211_hwsim` WPA2 station with ARM BlueZ, D-Bus and wpa_supplicant. It checks authorization, fragmented writes, rollback after a wrong password and a successful join. The board advertising check uses a scanner. A real phone and access point remain necessary to confirm physical provisioning.

For a test that leaves the current network active, use steps 1 through 3, then close the phone client before sending credentials. The Q1 Pro returns to its ordinary LED state when the setup window ends. [LED states](../q1pro-image/leds.md)
