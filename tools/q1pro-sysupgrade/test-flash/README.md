# Q1 Pro installer test on virtual NAND

Three boots of an ARM QEMU guest exercise the factory Linux sysupgrade script. The guest creates two block2mtd partitions and uses a temporary UBI user volume with a synthetic witness file. The fixture validates the archive, runs the factory writer, reboots and reads back the boot and software partitions. A separate rollback pass exercises the same path with a factory package.

Build the [QEMU BSP](../../q1pro-image/test-devices/README.md) first. Install QEMU AArch32, `fdtput`, `mkfs.ext4`, `mkfs.ubifs` and `ubinize`. Use locally assembled full-boot packages.

~~~sh
python3 -B tools/q1pro-sysupgrade/test-flash/run.py \
  --r3 /path/to/jetkvm-fullboot-sysupgrade.bin \
  --r3-sfoo /path/to/q1pro-rootfs.sfoo \
  --kernel-build /path/to/qemu/bsp-build \
  --stock-squashfs /path/to/stock-rootfs.squashfs \
  --rollback /path/to/factory-from-backup-linux-fullboot-sysupgrade.bin \
  --stock-boot /path/to/backup/mtd3-boot.bin \
  --stock-boot-sha256 "$BOOT_SHA256" --log /tmp/q1-virtual-flash.log
~~~

The tester keeps disk images and serial logs under a new directory when you pass `--work`. Its MTD differs from SPI NAND in page size and bad-block behavior. Physical boot, ECC, OOB and recovery require a board.
