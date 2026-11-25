# JetKVM for OrayBox Q1 Pro

Local, browser-based KVM for the **OrayBox XRK-Q1Pro**. See the connected computer's display, hear its audio, and control its keyboard and mouse over Ethernet, Wi-Fi, or WireGuard. R4 runs JetKVM on the board's factory Linux kernel and supports local sign-in without an Oray account.

**[Download R4](https://github.com/mansur54321/jetkvm-q1pro/releases/tag/v0.1.0-q1pro.4)** · **[Installation and recovery](INSTALL.md)** · **[Report an issue](https://github.com/mansur54321/jetkvm-q1pro/issues/new)**

## Choose your installation path

| What your Q1 Pro runs today | Path | What happens to settings |
| --- | --- | --- |
| Oray factory firmware `1.0.0_87160a` | Assemble the release locally with your board's factory backup, then upload it in **System Recovery Mode** | The full installer recreates the settings volume |
| JetKVM R3 or R4 from this port | Use the signed maintenance update when you have the exact image currently installed | The updater replaces the system volume and keeps the settings volume |
| Anything else | Check the model and firmware build before proceeding | Compatibility is unverified |

The release kit includes the built ARM application and installer tooling. The assembler uses factory components from your own backup to make the installable `.bin`. Keep your backup private and keep a copy off the device.

## What works on R4

- **Video and audio.** The hardware captured 1080p at about 60 frames per second and sent DisplayPort audio to a browser, including over WireGuard. Select H.265 manually when the browser advertises it as a WebRTC receiving codec.
- **Local control.** A computer connected to USB-C port 5 receives USB keyboard and mouse input. The browser console works over the LAN with a local password.
- **Networking.** Ethernet and dual-band Wi-Fi support local access. WireGuard uses one profile and one peer. Tailscale is an optional ARM add-on that uses storage on the device. Bluetooth Wi-Fi provisioning was exercised in a test environment, and its advertisement was detected on hardware.
- **Virtual media.** Upload an ISO or IMG, or let Q1 download it from a URL. The device stores the whole image before presenting it as a USB drive. A 3 MB netboot image was read back and checksum-verified over USB on hardware.
- **Status LEDs.** The case shows boot, network and pairing states. The two Ethernet LEDs indicate link and traffic.
- **MCP.** The third-party JetKVM MCP server can open a session over WireGuard. Keyboard input was tested on a connected Linux laptop. [JetKVM MCP](https://github.com/LeeroyDing/jetkvm-mcp)

On the tested board, a clean installation had about **44 MiB of free storage** for settings and images. Multi-gigabyte URL streaming and JetKVM OS Services need kernel features absent from this release. For a network install, use a small boot image and a wired Internet connection on the computer being booted. The Q1's own network connection is separate.

An Android phone used as the controlled USB-C device can supply video while USB input remains disconnected. This was observed on a OnePlus 12. The phone's USB-C data connection must come up before keyboard or mouse input can work. Use the **Relative** mouse mode once Android reports the USB device. The stock firmware's multitouch interface is still absent from R4.

## Source and development

The application source, web UI, Q1 board support, image builder, and maintenance source are in this repository.

See [Build from source](BUILD.md) for the toolchain, application build, and image verification.

| Component | Location |
| --- | --- |
| Browser UI | [`ui/`](ui/) |
| Native display capture and encoding | [`internal/native/`](internal/native/) |
| Board startup and root filesystem builder | [`tools/q1pro-image/`](tools/q1pro-image/) |
| Full installer and factory rollback builder | [`tools/q1pro-sysupgrade/`](tools/q1pro-sysupgrade/) |
| Signed maintenance updater | [`tools/q1pro-maintenance/`](tools/q1pro-maintenance/) |
| Virtual board and installer tests | [`tools/q1pro-system-test/`](tools/q1pro-system-test/) |

The [installation guide](INSTALL.md) also covers building the installer on Linux, verifying the download, and preparing a factory rollback image. Report Q1 issues here with the case model, factory version, JetKVM version, and a description of how the device is connected. Keep backups, Wi-Fi credentials, and VPN private keys out of issue attachments.

This is an independent port of [JetKVM](https://github.com/jetkvm/kvm). JetKVM's software in this repository is distributed under [GPL-2.0](LICENSE). Refer to the [Oray Q1 Pro manual](https://service.oray.com/question/61207.html) for the manufacturer's connector diagram.
