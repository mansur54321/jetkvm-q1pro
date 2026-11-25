# Assemble a full Q1 Pro system installer

Build the installer locally from your own factory boot and software backups. It contains the complete 4 MiB boot member plus the release rootfs padded to the software-volume capacity. Compatibility metadata names only `oraybox,q1pro`. Follow the [installation guide](../../INSTALL.md) for the flashing procedure.

**A full install recreates the user volume.** Make a private settings backup first. To preserve it during an update from a compatible JetKVM release, use [the signed RAM handoff](../q1pro-maintenance/HANDOFF.md).

## Build and validate

`FACTORY_SQUASHFS` points to the locally normalized stock software volume. The builder uses the factory `fwtool` in ARM user-mode QEMU to append and read back the archive metadata.

~~~sh
python3 -B tools/q1pro-sysupgrade/build.py \
  --release bin/q1pro-image --boot /path/to/backup/mtd3-boot.bin \
  --factory-rootfs "$FACTORY_SQUASHFS" --out bin/q1pro-sysupgrade
python3 -B tools/q1pro-sysupgrade/test.py \
  bin/q1pro-sysupgrade/jetkvm-q1pro-0.1.0-q1pro.4-linux-fullboot-sysupgrade.bin
~~~

The validator exercises the stock Linux format and model checks in an ARM guest. Its task ends before flash writes. The [virtual NAND test](test-flash/README.md) exercises a full write and rollback using temporary devices.

## Build a private rollback

~~~sh
python3 -B tools/q1pro-sysupgrade/build.py \
  --stock-mtd6 /path/to/backup/mtd6-rootfs.bin --boot /path/to/backup/mtd3-boot.bin \
  --factory-rootfs "$FACTORY_SQUASHFS"
~~~

Keep the rollback package with your private backup. The package holds the original software volume and full boot partition. Restore user data separately. A FIT-only package is unsuitable because the stock write can erase the rest of the boot partition.
