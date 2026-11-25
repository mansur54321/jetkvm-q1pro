# Q1 Pro USB and virtual media development

Run `python3 tools/q1pro-usb/check-host.py jetkvm` on the connected Linux host after it enumerates the Q1 Pro. Use `stock` against the original system. The checker reads descriptors from sysfs. It checks the HID report sizes and identifies the emulated CD/DVD.

Stable R4 exposes a keyboard, absolute and relative mouse interfaces and virtual media. A OnePlus 12 displayed video on the test board but did not enumerate USB. Video Alt Mode and USB enumeration are independent. See [the port notes](../Q1-Pro.md#hardware-paths).

## NBD compatibility study

The factory kernel disables NBD. The tool below builds a candidate against the open RV1106 BSP and compares its module symbol CRCs against the stock kernel backup. The candidate failed this check. Kernel release alone is insufficient to load a compatible module.

~~~sh
CROSS_COMPILE="$BUILDKIT_PATH/bin/arm-rockchip830-linux-uclibcgnueabihf-" \
  tools/q1pro-usb/build-nbd.sh "$SDK/sysdrv/source/kernel" stock.config mtd3-boot.bin build-q1-nbd
~~~

The [separate media test](../q1pro-remote-media/README.md) builds its kernel and modules together, then checks network-backed SCSI reads through `dummy_hcd`.
