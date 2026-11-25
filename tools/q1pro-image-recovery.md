# Q1 Pro recovery and temporary boot

The documented target is the XRK-Q1Pro. Port 2 powers the Q1 Pro. Port 5 connects to the controlled computer and carries DisplayPort Alt Mode and USB. Use a direct Ethernet link for the bootloader recovery page so it cannot conflict with an existing router address. The Q1 Pro recovery flow has been exercised on the target board. [Factory hardware guide](https://service.oray.com/question/61207.html)

## Keep a private recovery copy

Before a first installation, save the original flash partitions and user data from your own board to separate storage. The Linux system installer rewrites the full boot area and recreates the UBI user volume. Use [the local package builder](q1pro-sysupgrade/README.md) to prepare a factory rollback from that backup.

| Area | Installation relevance |
| --- | --- |
| `mtd3`, `boot` | The whole 4 MiB partition is preserved in a full-boot installer. FIT-only packages can erase the final bytes during a stock rewrite. |
| `mtd6`, `rootfs` | Contains `rootfs` (UBI volume 0) and `rootfs_data` (UBI volume 1). |
| Bootloader, factory identity and calibration | Preserve these partitions. They remain outside the full-boot package and the signed rootfs updater. |

A software-only update on an existing compatible JetKVM installation is a different operation. See [the RAM handoff procedure](q1pro-maintenance/HANDOFF.md).

## Experimental loading paths

[The RAM profile](q1pro-ram-system/README.md) starts a read-only system image from a loop device. Its data mount is temporary. Use a separate boot environment that controls the full kernel command line if you need to guarantee the stock UBI partition remains untouched. A stock command line with `ubi.mtd=rootfs` can attach UBI before the profile's init runs.

[The media kernel](q1pro-remote-media/board/README.md) adds NBD and FunctionFS and has QEMU coverage. Its kernel and initramfs require physical boot validation before anyone can use them as a general Q1 Pro recovery image.
