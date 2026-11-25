# Q1 Pro system tests in ARM QEMU

These tests boot the built JetKVM rootfs, check first-run setup and verify that ordinary settings survive a second start. The guest brings up DHCP and the web UI. It uses an isolated virtual network and binds the forwarded HTTP port to the workstation's loopback interface.

## Requirements

Use QEMU AArch32, ARM buildkit v0.2.8, SquashFS tools with XZ support, fakeroot, bsdtar, GNU cpio and gzip. The test downloads and checks the pinned Alpine ARMv7 package `linux-virt-6.12.117-r0.apk` and extracts the guest kernel and modules from it. The optional `--reserve-dma-budget` test models 216 MiB of memory after the board's DMA reservation. It cannot measure physical Rockchip encoder or DMA behavior.

~~~sh
python3 tools/q1pro-system-test/run.py --image bin/q1pro-image/q1pro-rootfs.squashfs
python3 tools/q1pro-system-test/run.py --serve --port 8717
python3 tools/q1pro-system-test/browser_offline.py
~~~

The browser test adds Chromium and Playwright. It sets a disposable password, performs local sign-in without Internet access, verifies authenticated WebRTC and checks that the frontend does not require a cloud login.

## Repeat on virtual UBI

Use the [QEMU BSP](../q1pro-image/test-devices/README.md) with block2mtd, UBI, UBIFS and SquashFS built in and devtmpfs disabled.

~~~sh
python3 -B tools/q1pro-system-test/ubi_boot.py \
  /path/to/qemu/bsp/arch/arm/boot/zImage bin/q1pro-image/q1pro-rootfs.squashfs
~~~

The virtual flash stores the real application and a synthetic user volume. The test checks the intended board layout and confirms that user files persist across boot.
