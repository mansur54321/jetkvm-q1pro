# HTTP Range media and USB FunctionFS

The stable Q1 Pro build downloads an image to its local user volume before serving it as a USB disk. Its stock kernel lacks NBD and the shipped runtime lacks the compatible FunctionFS module. An independently built module failed the stock symbol-CRC checks. Keep the stable image for normal use.

The app tests FunctionFS with a temporary configfs instance that remains unbound from the USB controller. The result determines whether the OS Services control is available. HTTP Range backing requires an NBD device before the application enables the streaming path.

## ARM test

The laboratory kernel enables NBD and FunctionFS. A fake USB host issues actual SCSI reads through `dummy_hcd`. The test reads from a 5 GiB HTTPS image at its first sector, past 4 GiB and near the end. An HTTP 503 must surface as an I/O error and a later read must recover. The FunctionFS test checks the OS Services status protocol while HID and virtual media are enabled.

~~~sh
export CROSS_COMPILE="$BUILDKIT_PATH/bin/arm-rockchip830-linux-uclibcgnueabihf-"
export CC="${CROSS_COMPILE}gcc"
tools/q1pro-remote-media/build-kernel.sh \
  /path/to/sdk/sysdrv/source/kernel /path/to/stock.config /tmp/q1-media-bsp
tools/q1pro-remote-media/build-test.sh /tmp/q1-media.test
python3 -B tools/q1pro-remote-media/test-guest.py \
  --kernel /tmp/q1-media-bsp/arch/arm/boot/zImage \
  --rootfs bin/q1pro-image/q1pro-rootfs.squashfs \
  --test-binary /tmp/q1-media.test --log /tmp/q1-media.log
~~~

See [the experimental RV1106 RAM profile](board/README.md) for board-specific build inputs and limitations.
