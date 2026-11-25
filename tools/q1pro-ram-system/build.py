#!/usr/bin/env python3
"""Wrap the release root in a read-only loop-backed initramfs, without accessing device storage."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
CACHE = Path.home() / ".cache/jetkvm-q1pro"
sys.path.insert(0, str(HERE.parent / "q1pro-image"))
from stock import convert

UBI = '[ "$(cat /sys/class/ubi/ubi0_1/name)" = rootfs_data ]\nmount -t ubifs ubi0:rootfs_data /mnt/data'
RAM = 'mount -t tmpfs -o size=16m,mode=0755,nosuid,nodev tmpfs /mnt/data'
DEVICES = {"console": ("c", 5, 1), "null": ("c", 1, 3), "loop0": ("b", 7, 0), "loop-control": ("c", 10, 237)}


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def unsquashfs():
    return shutil.which("unsquashfs") or str(CACHE / "image-tools/bin/unsquashfs")


def ram_rc(image):
    rc = subprocess.check_output([unsquashfs(), "-cat", image, "etc/rcS"]).decode()
    if "/sbin/mdev -d" not in rc or "/sbin/mdev -s" not in rc:
        raise ValueError("release lacks /dev support for the Q1 kernel with CONFIG_DEVTMPFS=n")
    if rc.count(UBI) != 1:
        raise ValueError("unrecognized release storage setup in /etc/rcS")
    return rc.replace(UBI, RAM)


def pack(stage, dest, nodes=()):
    # Keep fake device metadata and cpio in the same fakeroot session.
    script = 'set -eu\ncd "$1"\n'
    for name in nodes:
        kind, major, minor = DEVICES[name]
        script += f'mknod -m 0600 dev/{name} {kind} {major} {minor}\n'
    script += ('find . -exec touch -h -d @0 {} +\n'
               'set -o pipefail\n'
               'find . -print0 | LC_ALL=C sort -z | cpio --null -o --quiet -H newc '
               '--reproducible --owner=0:0 | gzip -n -1')
    with dest.open("wb") as output:
        subprocess.run(["fakeroot", "--", "bash", "-c", script, "q1-cpio", stage], stdout=output, check=True)
    for name in nodes:
        (stage / "dev" / name).unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, default=REPO / "bin/q1pro-image")
    parser.add_argument("--out", type=Path, default=REPO / "bin/q1pro-ram-system")
    parser.add_argument("--cc", default=CACHE / "buildkit-v0.2.8/bin/arm-rockchip830-linux-uclibcgnueabihf-gcc")
    args = parser.parse_args()
    images = {"board-sfoo": args.release / "q1pro-rootfs.sfoo", "emulated-hsqs": args.release / "q1pro-rootfs.squashfs"}
    reference = images["emulated-hsqs"]
    if convert(images["board-sfoo"].read_bytes(), False) != reference.read_bytes():
        raise ValueError("SFOO and standard root are not the same release")
    manifest = json.loads((args.release / "manifest.json").read_text())
    for image in images.values():
        if sha(image) != manifest["hashes"][image.name]:
            raise ValueError(f"release manifest disagrees with {image.name}")
    forbidden = [name for name in manifest["files"] if name.startswith(("userdata/", "mnt/data/"))
                 or name.endswith(("authorized_keys", "_host_key", ".pem", "/shadow"))]
    if forbidden:
        raise ValueError(f"unexpected provisioned state in release: {forbidden}")
    args.out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=args.out) as temp:
        work = Path(temp).resolve()
        stage = work / "initramfs"
        for name in ("dev", "image"):
            (stage / name).mkdir(parents=True)
        subprocess.run([args.cc, "-static", "-Os", "-s", "-Wall", "-Wextra", "-Werror", HERE / "init.c",
                        "-o", stage / "init"], check=True)
        rc = stage / "rcS.ram"
        rc.write_text(ram_rc(reference))
        rc.chmod(0o755)
        info = {"app": manifest["app"], "rcS_ram_sha256": sha(rc), "profiles": {}}
        for name, image in images.items():
            shutil.copyfile(image, stage / "root.img")
            if sha(stage / "root.img") != manifest["hashes"][image.name]:
                raise ValueError(f"release changed while copying {image.name}")
            output = work / (name + ".cpio.gz")
            pack(stage, output, DEVICES)
            info["profiles"][name] = {"root_sha256": sha(image), "initramfs_sha256": sha(output),
                                      "root_bytes": image.stat().st_size, "initramfs_bytes": output.stat().st_size}
        (work / "manifest.json").write_text(json.dumps(info, indent=2) + "\n")
        for name in [*(k + ".cpio.gz" for k in images), "manifest.json"]:
            os.replace(work / name, args.out / name)
    print(json.dumps(info, indent=2))


if __name__ == "__main__":
    main()
