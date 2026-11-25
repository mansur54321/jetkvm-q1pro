# Experimental RV1106 RAM profile

This profile combines the Q1 Pro application with `5.10.160-q1-media-ram` and packages a kernel and initramfs. It adds NBD and FunctionFS and builds board drivers with one matching module ABI. Boot on physical hardware remains unverified. Use the stable release for installation.

The packer checks the factory FIT DTB, the video path from LT7911D through CIF and Rockit, the USB controller, the SDIO radio wiring and the UART for Bluetooth. It installs the Rockchip MCU firmware in the external initramfs before Rockit initializes. The board SDK kernel needs the MCU hardware. QEMU's generic ARM machine tests the userspace with a separate control kernel.

The FIT's static DTB has no `memory` node containing a usable DRAM bank. The bootloader adds memory data at runtime. Establish the actual DRAM and reserved-memory map before selecting a temporary hardware load address. Verify the physical recovery procedure independently.

## Build

The reference dependencies are the JetKVM RV1106 BSP at `ccd1c03732055518f85d7ccb1d3200056aad21ff`, Radxa's AIC8800 SDK at `5562c254b0231be34588dc608ec28c4f1d613285`, and a signed wireless-regdb `2026.09.03` with its matching public certificate. Keep the reference DTB from your own factory backup. The packer checks hashes and matching module symbol CRCs.

~~~sh
export CROSS_COMPILE="$BUILDKIT_PATH/bin/arm-rockchip830-linux-uclibcgnueabihf-"
export CC="${CROSS_COMPILE}gcc"
tools/q1pro-remote-media/build-sdk-video.sh \
  /path/to/rv1106-system /path/to/stock.config /tmp/q1-sdk-video
tools/q1pro-remote-media/board/build-kernel.sh \
  /tmp/q1-sdk-video/sysdrv/source/kernel /tmp/q1-sdk-video/build/.config \
  /path/to/radxa/src/SDIO/driver_fw/driver/aic8800 /path/to/der-regdb-keys /tmp/q1-board-work
python3 -B tools/q1pro-remote-media/board/pack.py \
  --release bin/q1pro-image --kernel /tmp/q1-board-work/kernel --aic-objects /tmp/q1-board-work/aic \
  --radxa /path/to/radxa --regdb /path/to/wireless-regdb-2026.09.03 --regdb-cert /path/to/wens.x509.pem \
  --dtb /path/to/factory-fit-reference.dtb --cc "$CC" --out bin/q1pro-media-ram
python3 -B tools/q1pro-remote-media/board/test-profile.py bin/q1pro-media-ram \
  --control-kernel /path/to/q1-systest/arch/arm/boot/zImage
~~~

The report records the built kernel, initramfs and DTB hashes. The generic ARM test boots the exact initramfs and checks HTTP. Use the [media transport test](../README.md) to exercise the USB and NBD data path.
