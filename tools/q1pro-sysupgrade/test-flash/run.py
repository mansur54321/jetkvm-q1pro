#!/usr/bin/env python3
"""Exercise the stock Q1 sysupgrade validator, MTD writer and NAND upgrade scripts in three isolated ARM boots."""

import argparse
import gzip
import hashlib
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
CACHE = Path.home() / ".cache/jetkvm-q1pro"
sys.path.insert(0, str(REPO / "tools/q1pro-image"))
sys.path.insert(0, str(HERE.parent))
from build import boot_partition  # noqa: E402
from stock import LEB, VOLUME_SIZE, STOCK_SQUASH_SHA256, convert  # noqa: E402

BOOT_SIZE = 4 << 20
ROOT_SIZE = 83 << 20


def digest(data):
    return hashlib.sha256(data).hexdigest()


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def make_images(work, args):
    fixtures = work / "fixtures"
    fixtures.mkdir()
    boot, _, _ = boot_partition(args.stock_boot, work, args.stock_boot_sha256)
    stock = args.stock_squashfs.read_bytes()
    if digest(stock) != STOCK_SQUASH_SHA256:
        raise ValueError("expected the normalized factory 1.0.0_87160a SquashFS")
    # Round up the read-only virtio volume for the final kernel page read.
    (work / "tools.squashfs").write_bytes(stock.ljust(VOLUME_SIZE, b"\0"))
    # A canary proves the writer really changed the first erase block of the virtual boot partition.
    initial_boot = bytearray(boot)
    canary = b"Q1-VIRTUAL-BOOT-CANARY"
    initial_boot[: len(canary)] = canary
    expect = {"full_boot": digest(boot), "canary_boot": digest(initial_boot)}
    hashes = {}
    for name, package, payload in [
        ("r3", args.r3, args.r3_sfoo.read_bytes()),
        ("factory", args.rollback, convert(stock, True)),
    ]:
        packaged = fixtures / (name + ".bin")
        shutil.copyfile(package, packaged)
        with tarfile.open(packaged, "r:") as archive:
            kernel = archive.extractfile("sysupgrade-oraybox_q1pro/kernel").read()
            root = archive.extractfile("sysupgrade-oraybox_q1pro/root").read()
        if (
            kernel != boot
            or len(payload) > VOLUME_SIZE
            or root != payload.ljust(VOLUME_SIZE, b"\xff")
        ):
            raise ValueError(
                f"{name} package does not contain the full original boot or the expected software volume"
            )
        hashes[name] = digest(packaged.read_bytes())
        expect[name + "_root"] = digest(root)
        if name == "factory":
            (work / "original.bin").write_bytes(root)
    (fixtures / "expected").write_text("".join(f"{k}={v}\n" for k, v in expect.items()))
    shutil.copyfile(HERE / "phase.sh", fixtures / "phase.sh")
    state = work / "synthetic-state"
    state.mkdir()
    (state / "factory-sentinel").write_text("factory user data\n")
    run(
        "mkfs.ubifs",
        "-U",
        "-r",
        state,
        "-m",
        "8",
        "-e",
        str(LEB),
        "-c",
        "434",
        "-o",
        work / "state.ubifs",
    )
    layout = work / "volumes.ini"
    layout.write_text(
        f"[software]\nmode=ubi\nimage=original.bin\nvol_id=0\nvol_type=dynamic\nvol_name=rootfs\nvol_size={VOLUME_SIZE}\n"
        f"[settings]\nmode=ubi\nimage=state.ubifs\nvol_id=1\nvol_type=dynamic\nvol_name=rootfs_data\nvol_size={434 * LEB}\n"
    )
    run(
        "ubinize",
        "-o",
        work / "root.ubi",
        "-m",
        "8",
        "-s",
        "8",
        "-O",
        "4032",
        "-p",
        "131072",
        layout,
        cwd=work,
    )
    ubi = (work / "root.ubi").read_bytes()
    if len(ubi) > ROOT_SIZE:
        raise ValueError("synthetic UBI exceeds the target partition")
    with (work / "flash.img").open("xb") as f:
        f.write(initial_boot)
        f.write(ubi)
        while f.tell() < BOOT_SIZE + ROOT_SIZE:
            f.write(b"\xff" * min(1 << 20, BOOT_SIZE + ROOT_SIZE - f.tell()))
    with (work / "packages.ext4").open("xb") as f:
        f.truncate(96 << 20)
    run("mkfs.ext4", "-q", "-F", "-d", fixtures, work / "packages.ext4")
    return hashes


def make_ramdisk(work, args):
    cc = args.sdk / "bin/arm-rockchip830-linux-uclibcgnueabihf-gcc"
    run(
        cc,
        "-Os",
        "-static",
        "-Wl,-z,noexecstack",
        "-s",
        HERE / "enter.c",
        "-o",
        work / "enter",
    )
    listing = work / "cpio.list"
    listing.write_text(
        "dir /dev 0755 0 0\nnod /dev/console 0600 0 0 c 5 1\nnod /dev/null 0666 0 0 c 1 3\n"
        "dir /sys 0555 0 0\ndir /proc 0555 0 0\ndir /bin 0755 0 0\ndir /factory 0755 0 0\n"
        f"file /init {HERE / 'init'} 0755 0 0\nfile /enter {work / 'enter'} 0755 0 0\n"
        f"file /bin/busybox {args.busybox.resolve()} 0755 0 0\n"
    )
    archive = subprocess.check_output(
        [args.kernel_build / "usr/gen_init_cpio", "-t", "0", listing]
    )
    (work / "initramfs.cpio.gz").write_bytes(gzip.compress(archive, mtime=0))


def exercise(work, args):
    hashes = make_images(work, args)
    make_ramdisk(work, args)
    cmd = [
        "qemu-system-aarch64",
        "-cpu",
        "cortex-a53,aarch64=off",
        "-m",
        "256M",
        "-smp",
        "1",
        "-nodefaults",
        "-serial",
        "stdio",
        "-display",
        "none",
        "-monitor",
        "none",
        "-no-reboot",
        "-nic",
        "none",
        "-kernel",
        args.kernel_build / "arch/arm/boot/zImage",
        "-initrd",
        work / "initramfs.cpio.gz",
    ]
    # The virt board probes the MMIO transports in reverse insertion order.
    for i, (filename, readonly) in enumerate(
        [("packages.ext4", "on"), ("tools.squashfs", "on"), ("flash.img", "off")]
    ):
        cmd += [
            "-drive",
            f"if=none,format=raw,id=d{i},file={work / filename},readonly={readonly}",
            "-device",
            f"virtio-blk-device,drive=d{i}",
        ]
    dtb = work / "virt.dtb"
    run(*cmd, "-M", f"virt,gic-version=3,dumpdtb={dtb}", capture_output=True)
    run(
        "fdtput",
        "-t",
        "s",
        dtb,
        "/",
        "compatible",
        "oraybox,q1pro",
        "linux,dummy-virt",
        "rockchip,rv1106",
    )
    bootargs = (
        "console=ttyAMA0 earlycon rdinit=/init loglevel=5 panic=1 root=/dev/ram0 storagemedia=mtd "
        'block2mtd.block2mtd=/dev/vda,131072 "mtdparts=block2mtd: /dev/vda:4m(boot),83m(rootfs)" ubi.mtd=rootfs,4032'
    )
    summary = [
        "ARM QEMU stock Linux installer, no NIC; the only writable disk is a disposable block2mtd image",
        f"R3: {hashes['r3']}",
        f"factory-from-backup: {hashes['factory']}",
    ]
    for phase in ["r3", "factory", "verify"]:
        logpath = work / f"{phase}.serial.log"
        with logpath.open("wb") as log:
            result = subprocess.run(
                [
                    *cmd,
                    "-M",
                    "virt,gic-version=3",
                    "-dtb",
                    dtb,
                    "-append",
                    f"{bootargs} q1stage={phase}",
                ],
                stdin=subprocess.DEVNULL,
                stdout=log,
                stderr=subprocess.STDOUT,
                timeout=150,
            )
        text = logpath.read_text(errors="replace").replace("\r", "")
        marker = "Q1_E2E_VERIFY_PASS" if phase == "verify" else "sysupgrade successful"
        lines = [
            line
            for line in text.splitlines()
            if line.startswith(("Q1_E2E", "sysupgrade successful"))
        ]
        if result.returncode or marker not in text or "Q1_E2E FAIL" in text:
            raise RuntimeError(f"{phase} failed: {text[-3500:]}")
        if phase != "r3":
            m = re.search(r"data_lebs=(\d+) .*?data_sha=([0-9a-f]{64})", text)
            if not m or not 1 <= int(m[1]) <= ROOT_SIZE // 131072:
                raise ValueError(f"{phase}: missing volume geometry")
            if digest(b"\xff" * (int(m[1]) * LEB)) != m[2]:
                raise ValueError(f"{phase}: rootfs_data not erased")
            lines.append(
                f"{phase}: all {int(m[1]) * LEB} raw bytes of rootfs_data were 0xff before mounting UBIFS"
            )
        summary.extend(lines)
    return "\n".join(summary) + "\n"


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for flag in ["r3", "rollback", "stock-boot"]:
        p.add_argument("--" + flag, type=Path, required=True)
    p.add_argument("--stock-boot-sha256", required=True,
                   help="SHA-256 recorded when comparing independent reads of the original boot partition")
    p.add_argument(
        "--r3-sfoo", type=Path, default=REPO / "bin/q1pro-image-r3/q1pro-rootfs.sfoo"
    )
    p.add_argument(
        "--stock-squashfs",
        type=Path,
        default=CACHE / "usb-kernel/stock-rootfs.squashfs",
    )
    p.add_argument(
        "--busybox", type=Path, default=CACHE / "image-tools/bin/busybox-arm"
    )
    p.add_argument("--sdk", type=Path, default=CACHE / "buildkit-v0.2.8")
    p.add_argument(
        "--kernel-build", type=Path, default=CACHE / "usb-kernel/mdev-system-build"
    )
    p.add_argument("--log", type=Path)
    p.add_argument(
        "--work",
        type=Path,
        help="new empty directory to retain the disposable devices and guest serial logs",
    )
    a = p.parse_args()
    if a.work:
        a.work.mkdir(mode=0o700)
        report = exercise(a.work.resolve(), a)
    else:
        with tempfile.TemporaryDirectory(prefix="q1-stock-flash-") as tmp:
            report = exercise(Path(tmp), a)
    print(report, end="")
    if a.log:
        a.log.write_text(report)
