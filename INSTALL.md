# Install JetKVM R4 on the OrayBox Q1 Pro

[R4 release downloads](https://github.com/mansur54321/jetkvm-q1pro/releases/tag/v0.1.0-q1pro.4)

## Before you start

This guide targets the unit marked **XRK-Q1Pro** with factory firmware `1.0.0_87160a`. The tested System Recovery Mode identifies the board as **OrayBox Q1Pro** and U-Boot as `2017.09-0e75b792`. The similar Q1 and other Oray models use different layouts. Use stable power and allow physical access throughout the installation.

> [!WARNING]
> The full installer writes the boot and system partitions and recreates `rootfs_data`. Back up the original firmware, settings, and any virtual media off-device before uploading an image. Prepare a recovery image from the original backup first. An interrupted flash can require further recovery.

The full first-install procedure was exercised on physical hardware with R3. R4 was installed and verified on that board using the settings-preserving maintenance path. The full R4 image uses the same package format and was accepted by the factory Linux validator. Factory rollback was exercised on virtual NAND. Treat the original backups and physical access to System Recovery Mode as prerequisites.

## 1. Save the factory inputs

Keep the complete **4 MiB boot partition** and **83 MiB rootfs MTD partition** from your own compatible unit. The release builder uses the factory boot FIT, a verified factory filesystem, and the few hardware-specific files the application needs. A backup of the original settings is separate from the generated installable image. Keep full NAND images and settings private.

If your factory maintenance access provides an authorized root shell with the MTD devices, check the partition labels first using `cat /proc/mtd`. On the tested layout, `mtd3` is `boot` and `mtd6` is `rootfs`. The following commands read those partitions to your Linux workstation. Substitute your established SSH endpoint.

```bash
umask 077
mkdir -p q1-backup
FACTORY_SSH='your-admin-user@your-device'
ssh "$FACTORY_SSH" 'cat /proc/mtd'
ssh "$FACTORY_SSH" 'cat /dev/mtd3ro' > q1-backup/mtd3-boot.bin
ssh "$FACTORY_SSH" 'cat /dev/mtd6ro' > q1-backup/mtd6-rootfs.bin
wc -c q1-backup/mtd3-boot.bin q1-backup/mtd6-rootfs.bin
ssh "$FACTORY_SSH" 'cat /dev/mtd3ro' | cmp q1-backup/mtd3-boot.bin -
ssh "$FACTORY_SSH" 'cat /dev/mtd6ro' | cmp q1-backup/mtd6-rootfs.bin -
(cd q1-backup && sha256sum mtd3-boot.bin mtd6-rootfs.bin > SHA256SUMS)
```

The expected byte counts are `4194304` and `87031808`, respectively. Read each partition twice and compare the copies before using them. Stop if the labels or sizes differ, or if your factory shell exposes network diagnostics only. Use your device's authorized maintenance or backup path to acquire the required files.

## 2. Assemble and verify the installer on Linux

Build on an x86-64 Linux workstation. On Ubuntu, install the host tools for the assembler and ARM build kit.

```bash
sudo apt-get update
sudo apt-get install build-essential bison flex pkg-config perl curl zstd \
  python3 qemu-user squashfs-tools device-tree-compiler ca-certificates
```

Download `jetkvm-q1pro-r4-install-kit.tar.gz` and `SHA256SUMS` from the release. Verify the archive's checksum before extracting it.

```bash
sha256sum --ignore-missing --check SHA256SUMS
tar -xzf jetkvm-q1pro-r4-install-kit.tar.gz
cd jetkvm-q1pro-r4-install-kit
```

Set `Q1_BACKUP` to the directory containing your two verified factory partition backups and their recorded checksums.

```bash
Q1_BACKUP=/absolute/path/to/q1-backup
(cd "$Q1_BACKUP" && sha256sum --check SHA256SUMS)
tools/q1pro-install/prepare-buildkit.sh ./q1-build-inputs
BOOT_SHA256=$(awk '$2 == "mtd3-boot.bin" {print $1}' "$Q1_BACKUP/SHA256SUMS")

python3 tools/q1pro-install/assemble.py \
  --boot "$Q1_BACKUP/mtd3-boot.bin" \
  --boot-sha256 "$BOOT_SHA256" \
  --stock-mtd6 "$Q1_BACKUP/mtd6-rootfs.bin" \
  --sdk q1-build-inputs/sdk \
  --app jetkvm_app-armv7 \
  --busybox q1-build-inputs/bin/busybox-arm \
  --dropbear q1-build-inputs/bin/dropbearmulti-arm \
  --wifi q1-build-inputs/wifi \
  --out q1-install
```

Use the SHA-256 you recorded when you checked the two boot partition reads. The assembler checks the exact factory firmware, boot data, board model, and image geometry. It creates the output directory with private permissions. Choose a new directory for a subsequent build.

| File in `q1-install/` | Purpose |
| --- | --- |
| `jetkvm-q1pro-0.1.0-q1pro.4-linux-fullboot-sysupgrade.bin` | Install JetKVM from System Recovery Mode |
| `fullboot-manifest.json` | Install image size and SHA-256 |
| `factory-from-backup-linux-fullboot-sysupgrade.bin` | Restore factory software from your verified backup |
| `factory-from-backup-fullboot-manifest.json` | Factory restoration image size and SHA-256 |
| `software-volume/` | The exact system volume and manifest needed for signed maintenance updates |

Run `sha256sum q1-install/*.bin` and compare each digest with the `sha256` field in its manifest. Keep both manifests, the factory restoration image, `software-volume/`, and the original backups off-device. The assembled images and software volume contain files from your board's backup. Keep them private.

## 3. Enter System Recovery Mode and install

1. Connect the Q1 Pro's RJ45 port **directly to a workstation's Ethernet port**, with no router or switch between them. Keep other routes to `192.168.1.1` off this connection. The bootloader has its own DHCP server.
2. Disconnect both USB-C cables to remove power, including any power from the controlled computer. Hold the enclosure button while plugging a power supply into USB-C **port 2**, the port near Ethernet. Leave USB-C **port 5** disconnected for now. The tested unit entered recovery with the button held from the start of power-up.
3. Renew the workstation's DHCP lease on the direct link and open **[http://192.168.1.1/](http://192.168.1.1/)**. Use the root path `/`. Check for **System Recovery Mode** with **OrayBox Q1Pro** and verify the loader version. Stop if the recovery page or model is different.
4. Choose the **system** upload, select your locally assembled full `sysupgrade.bin`, and upload it. Confirm the filename or reported size before accepting the write. A roughly 29 MiB size was displayed for the full tested package. Leave the separate bootloader update control alone.
5. Wait for the success page and automatic reboot. Keep power and Ethernet connected while the device writes the image and restarts.
6. Connect Q1 to your regular LAN with DHCP. Find its address in the router's client list or browse the local DNS-SD service `_jetkvm._tcp`. Open that address in a browser and set a local password. Connect the controlled computer's DisplayPort Alt Mode and USB data cable to **port 5**.

The Q1 Pro can advertise a `.local` hostname on networks with mDNS. The normal DHCP address may differ from the isolated recovery address. The USB-C port numbers refer to the [Oray Q1 Pro connector diagram](https://service.oray.com/question/61207.html).

## 4. Verify the first boot

Open `http://<device-ip>/device/status` to confirm that the device answers as `oraybox-q1pro`. Verify the installed version in **Settings → General**. Test a moving image and keyboard or mouse input from the console. For a laptop, configure the connected DisplayPort output as a mirror if you want to see the built-in screen.

Open **Settings → Network** to set up Wi-Fi or import a dedicated WireGuard client profile. A separate WireGuard peer lets the Q1 keep its own address and credentials. If WireGuard fails after power loss even though the LAN works, check the clock and configure a reachable time source under **Network → Time synchronization**. The device can start with the firmware build time when neither the hardware clock nor the network has supplied the current time.

Use **Virtual Media → Download from URL** to save a small ISO to the Q1 and then mount it as **CD/DVD**. For example, the tested [netboot.xyz ISO](https://boot.netboot.xyz/ipxe/netboot.xyz.iso) fits in the built-in storage. The computer being booted needs its own connection to the relevant boot server.

## Update an existing R3 or R4 without clearing settings

Use the maintenance path with the exact image already installed and a working administrative shell on the Q1. Back up the settings off-device first. The updater verifies the board, the current system volume and both signed packages before it accepts an activation request.

Generate an owner key on your workstation. Sign the new `q1-install/software-volume/` directory, then do the same with the matching directory for your **exact currently installed** system image. Both directories must contain `manifest.json` and `q1pro-rootfs.sfoo`.

```bash
umask 077
openssl genpkey -algorithm ED25519 -out owner-key.pem
openssl pkey -in owner-key.pem -pubout -out owner-key.pub
python3 tools/q1pro-image/package.py pack /path/to/new-release owner-key.pem new.tar
python3 tools/q1pro-image/package.py pack /path/to/current-release owner-key.pem previous.tar
python3 tools/q1pro-image/package.py verify new.tar owner-key.pub
python3 tools/q1pro-image/package.py verify previous.tar owner-key.pub
```

Build the ARM maintenance helper from the release source if you do not have it yet.

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o q1-handoff ./tools/q1pro-maintenance
```

Transfer the ARM `q1-handoff` executable, both packages, and **only the public key** to a private directory on the Q1's RAM filesystem, such as `/tmp/q1-maintenance`. Keep the private key on your workstation. Run these commands on the Q1 with administrative privileges.

```bash
cd /tmp/q1-maintenance
chmod 700 q1-handoff
./q1-handoff handoff-prepare new.tar previous.tar owner-key.pub
```

Review the **PREPARED** result and the new SFOO digest. Preparation leaves the running system in place. To cancel at this point, run `./q1-handoff handoff-discard`. When ready, activate using the exact digest the tool printed.

```bash
NEW_SFOO_SHA256='replace-with-the-64-digit-digest-from-PREPARED'
./q1-handoff handoff-activate "$NEW_SFOO_SHA256"
```

The SSH and KVM sessions close during activation. The update replaces the software volume and reboots. Check the version, local password, and VPN settings after it starts. If a write fails while the update is still running in RAM, the helper attempts to restore the verified previous volume. A power cut or a new image that does not boot requires recovery with the bootloader. If the tool rejects the previous image, leave the installed system in place until you have the exact matching release directory or a tested recovery route.

## Restore the factory system

Use the factory restoration image you made from your own verified backup. Enter **System Recovery Mode** using the direct Ethernet procedure above, upload the factory system image using the system update control, and wait for the reboot. A factory system restore recreates the settings volume. Restore configuration from your own backup using the maintenance method appropriate to the factory software.

If the Q1 cannot reach System Recovery Mode, contact the vendor or a board repair service for the correct recovery procedure. This release covers the software image and the tested web loader path.
