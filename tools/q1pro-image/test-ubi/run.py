#!/usr/bin/env python3
"""Exercise real Linux UBI update and recovery on QEMU with a disposable block2mtd device."""

import argparse
import gzip
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from package import verify
from stock import read_stock, convert


def run(*args):
    subprocess.run(args, check=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in [
        "kernel-tree",
        "sdk",
        "busybox",
        "stock-mtd6",
        "package",
        "trusted-public-key",
    ]:
        p.add_argument("--" + name, required=True, type=Path)
    a = p.parse_args()
    _, candidate = verify(a.package, a.trusted_public_key)
    kernel = a.kernel_tree.resolve()
    with tempfile.TemporaryDirectory(prefix="q1pro-ubi-") as td:
        temp = Path(td)
        fs = temp / "initramfs"
        fs.mkdir()
        for n in ["bin", "dev", "proc", "sys", "mods"]:
            (fs / n).mkdir()
        (fs / "old").write_bytes(convert(read_stock(a.stock_mtd6), True))
        (fs / "new").write_bytes(candidate)
        (fs / "sentinel").write_bytes(os.urandom(4096))
        (fs / "q1-virtual-ubi-test").touch()
        shutil.copyfile(a.busybox, fs / "bin/busybox")
        (fs / "bin/busybox").chmod(0o755)
        for name in [
            "sh",
            "mount",
            "insmod",
            "cat",
            "echo",
            "sync",
            "poweroff",
            "sleep",
        ]:
            (fs / "bin" / name).symlink_to("busybox")
        cc = a.sdk / "bin/arm-rockchip830-linux-uclibcgnueabihf-gcc"
        run(
            cc,
            "-static",
            "-Os",
            "-Wall",
            "-Werror",
            HERE / "guest.c",
            "-o",
            fs / "proof",
        )
        modules = "virtio_ring virtio virtio_mmio virtio_blk mtd ubi block2mtd"
        for name in modules.split():
            matches = list((kernel / "lib/modules").rglob(name + ".ko.gz"))
            if len(matches) != 1:
                raise ValueError("kernel tree missing " + name)
            (fs / "mods" / (name + ".ko")).write_bytes(
                gzip.decompress(matches[0].read_bytes())
            )
        (fs / "init").write_text("""#!/bin/sh
export PATH=/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
for mod in virtio_ring virtio virtio_mmio virtio_blk mtd ubi; do insmod /mods/$mod.ko || exit 1; done
for attempt in 1 2 3 4 5; do [ -b /dev/vda ] && break; sleep 1; done
insmod /mods/block2mtd.ko block2mtd=/dev/vda,131072,q1-virtual-erase
cat /proc/mtd
case "$(cat /proc/cmdline)" in
    *proof=recover*) /proof recover ;;
    *) /proof update ;;
esac
echo "Q1TEST exit $?"
sync
poweroff -f
""")
        (fs / "init").chmod(0o755)
        archive = temp / "fixture.cpio.gz"
        with archive.open("wb") as dest:
            subprocess.run(
                [
                    "fakeroot",
                    "--",
                    "sh",
                    "-ec",
                    'cd "$1"; mknod dev/console c 5 1; mknod dev/null c 1 3; find . -print0 | cpio --null -o -H newc --owner=0:0 --quiet | gzip -1',
                    "test",
                    fs,
                ],
                check=True,
                stdout=dest,
            )
        backing = temp / "virtual-flash.img"
        with backing.open("wb") as f:
            for _ in range(83):
                f.write(b"\xff" * (1024 * 1024))
        for step, marker in [
            ("update", "Q1TEST interrupted"),
            ("recover", "Q1TEST recovery verified"),
        ]:
            cmd = [
                "qemu-system-arm",
                "-machine",
                "virt",
                "-cpu",
                "cortex-a15",
                "-m",
                "384M",
                "-smp",
                "2",
                "-nodefaults",
                "-display",
                "none",
                "-monitor",
                "none",
                "-serial",
                "stdio",
                "-no-reboot",
                "-nic",
                "none",
                "-kernel",
                kernel / "boot/vmlinuz-lts",
                "-initrd",
                archive,
                "-drive",
                f"if=none,id=flash,format=raw,file={backing}",
                "-device",
                "virtio-blk-device,drive=flash",
                "-append",
                "console=ttyAMA0 rdinit=/init panic=1 loglevel=7 proof=" + step,
            ]
            proc = subprocess.run(
                cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=140
            )
            log = proc.stdout.decode(errors="replace")
            if proc.returncode != 0 or marker not in log or "Q1TEST exit 0" not in log:
                raise RuntimeError(log[-6500:])
            for line in log.splitlines():
                if line.startswith(("Q1TEST", "mtd")):
                    print(line, flush=True)
        print(
            "Restored stock software SHA-256",
            hashlib.sha256((fs / "old").read_bytes()).hexdigest(),
        )
        print("Independent user volume matched its original sentinel after both boots.")


if __name__ == "__main__":
    main()
