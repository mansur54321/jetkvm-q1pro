# Q1 Pro LEDs

The stock DT uses `gpio-leds` with active-low outputs. The driver applies polarity and the firmware selects the LED class triggers.

| LED | Class name | GPIO |
| --- | --- | --- |
| Ethernet link | `eth:link` | GPIO1_A3 |
| Ethernet RX/TX | `eth:act` | GPIO1_A4 |
| Case blue | `status:blue` | GPIO2_A3 |
| Case red | `status:red` | GPIO2_A6 |
| Case green | `status:green` | GPIO2_A7 |

| Case light | Meaning |
| --- | --- |
| Solid red | Initial startup |
| Red, 300 ms on / 300 ms off | No link. Wi-Fi is idle. |
| Blue, 150 ms on / 350 ms off | Wi-Fi scan, association or key exchange |
| Solid yellow | A link is up and the device is waiting for an address |
| Blue, 150 ms on / 150 ms off | Bluetooth Wi-Fi setup is open. Select the device in the phone client |
| Cyan, 150 ms on / 150 ms off | Bluetooth pairing awaits physical confirmation |
| Solid blue during Bluetooth setup | The current phone can send a Wi-Fi profile |
| Solid green | Ethernet link and a usable address |
| Solid blue outside Bluetooth setup | Wi-Fi is ready |
| Green or blue, 1200 ms on / 150 ms off | An active WebRTC control session over the preferred network |

The status process prefers Ethernet when both interfaces have addresses. It treats tentative or failed IPv6 addresses as unusable. Local KVM requires a reachable IP address. The LED does not measure Internet connectivity.

Run `tools/q1pro-image/test-led.sh` to exercise carrier changes, Bluetooth setup states and the Ethernet activity triggers in a separate network namespace.
