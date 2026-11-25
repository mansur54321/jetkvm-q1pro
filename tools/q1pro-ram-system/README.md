# Start the Q1 Pro userspace from RAM

The bootstrap mounts the existing root filesystem read-only on `/dev/loop0` and transfers control to its init. A copy of `rcS` mounts the user volume as a 16 MiB tmpfs. The headless application boots with volatile settings.

~~~sh
python3 -B tools/q1pro-ram-system/build.py --release bin/q1pro-image
python3 -B tools/q1pro-ram-system/test.py --release bin/q1pro-image --compare-extracted
~~~

The builder requires the ARM buildkit, `unsquashfs`, GNU cpio, fakeroot, gzip and standard Linux utilities. Use `--cc` to specify a toolchain and `--out` for a separate output directory.

| Archive | Embedded root |
| --- | --- |
| `board-sfoo.cpio.gz` | Board format. Requires the stock SFOO-aware SquashFS implementation |
| `emulated-hsqs.cpio.gz` | Standard SquashFS XZ for a compatible test kernel |

The kernel must provide initramfs, loop and SquashFS before the application loads its modules. The board modules depend on the stock kernel ABI. The stock command line can attach NAND through `ubi.mtd=rootfs` before the initramfs runs. Control the complete command line if testing a RAM-only boot on hardware. Physical support for this loading path remains unverified.

The QEMU test uses 216 MiB and compares local login, fresh settings on a second cold boot, and memory use. Pass `--bsp-build /path/to/qemu/bsp-build` to repeat the userspace check with a compatible RV1106 test kernel. [QEMU dependencies](../q1pro-system-test/README.md)
