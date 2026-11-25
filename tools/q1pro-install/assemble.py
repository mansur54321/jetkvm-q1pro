#!/usr/bin/env python3
"""Assemble the Q1 Pro installer and rollback image from local factory backups."""

import argparse
from pathlib import Path
import re
import subprocess
import sys
import tempfile

TOOLS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS / "q1pro-sysupgrade"))

from build import boot_partition, build
from stock import read_stock


def boot_checksum(value):
    if not re.fullmatch(r"[0-9a-fA-F]{64}", value):
        raise argparse.ArgumentTypeError("expected a 64-digit SHA-256 digest")
    return value.lower()


def assemble(args):
    args.out.mkdir(mode=0o700)
    try:
        with tempfile.TemporaryDirectory(prefix=".assemble-", dir=args.out) as temp:
            work = Path(temp)
            boot_partition(args.boot, work, args.boot_sha256)
            factory = work / "factory.squashfs"
            factory.write_bytes(read_stock(args.stock_mtd6))

            release = work / "release"
            subprocess.run([
                sys.executable, "-B", TOOLS / "q1pro-image/build.py",
                "--mtd6", args.stock_mtd6, "--sdk", args.sdk, "--app", args.app,
                "--busybox", args.busybox, "--dropbear", args.dropbear,
                "--wifi", args.wifi, "--output", release,
            ], check=True)

            images = work / "images"
            images.mkdir(mode=0o700)
            install = build(release, args.boot, factory, images, boot_sha256=args.boot_sha256)
            rollback = build(release, args.boot, factory, images,
                             boot_sha256=args.boot_sha256, stock_mtd6=args.stock_mtd6)
            for path in images.iterdir():
                path.chmod(0o600)
                path.rename(args.out / path.name)
            release.chmod(0o700)
            for path in release.iterdir():
                path.chmod(0o600)
            release.rename(args.out / "software-volume")
    except BaseException:
        if not any(args.out.iterdir()):
            args.out.rmdir()
        raise

    for name, result in (("Installer", install), ("Factory rollback", rollback)):
        print(f"{name}  {args.out / result['package']}")
        print(f"SHA-256  {result['sha256']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--boot", type=Path, required=True, help="full 4 MiB boot partition backup")
    parser.add_argument("--boot-sha256", type=boot_checksum, required=True,
                        help="digest confirmed by a second read from the same device")
    parser.add_argument("--stock-mtd6", type=Path, required=True, help="stock 1.0.0_87160a mtd6 backup")
    parser.add_argument("--sdk", type=Path, required=True, help="extracted JetKVM ARM buildkit")
    parser.add_argument("--app", type=Path, required=True, help="JetKVM ARM application from the release")
    parser.add_argument("--busybox", type=Path, required=True, help="ARM BusyBox built for Q1 Pro")
    parser.add_argument("--dropbear", type=Path, required=True, help="ARM dropbearmulti built for Q1 Pro")
    parser.add_argument("--wifi", type=Path, required=True, help="directory with ARM wpa_supplicant and wpa_cli")
    parser.add_argument("--out", type=Path, required=True, help="new private directory for both local images")
    args = parser.parse_args()
    try:
        assemble(args)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Assembly failed: {error}\n")


if __name__ == "__main__":
    main()
