#!/usr/bin/env python3
"""Build a Q1 Pro Linux sysupgrade USTAR with its original FIT and release SFOO."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
sys.path.insert(0, str(HERE.parent / "q1pro-image"))
from stock import LEB, STOCK_SQUASH_SHA256, VOLUME_SIZE, convert, read_stock
from package import payload_geometry

BOARD = "oraybox,q1pro"
PROFILE = "oraybox_q1pro"
TARGET = "rockchip/rv1106_xrk"
FACTORY_SFOO_SHA256 = "6c012dd89176ad12493e03a0e5d9cbde956abbdaccbe9675a9ee4a88602a04c6"
FIT_87160A = "10c8b64e02a39dd24b7db65658b71fd182d305fc478dbf0f9134d9aa419ba067"
CACHE = Path.home() / ".cache/jetkvm-q1pro"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def unsquashfs():
    return shutil.which("unsquashfs") or str(CACHE / "image-tools/bin/unsquashfs")


def boot_partition(boot, work, expected_sha256):
    if not boot.is_file():
        raise ValueError("--boot must be a saved full 4 MiB mtd3-boot.bin")
    data = boot.read_bytes()
    if len(data) != 4 * 1024 * 1024:
        raise ValueError("expected the complete 4 MiB Q1 Pro boot partition")
    if digest(data) != expected_sha256.lower():
        raise ValueError("boot partition does not match the supplied SHA-256")
    def prop(path, name, kind="s"):
        return subprocess.check_output(["fdtget", "-t", kind, boot, path, name], text=True).strip()
    if data[:4] != bytes.fromhex("d00dfeed") or int.from_bytes(data[4:8], "big") > len(data):
        raise ValueError("invalid stock FIT header")
    conf = "/configurations/" + prop("/configurations", "default")
    sizes = []
    hashes = {}
    for key, expected_type in (("fdt", "flat_dt"), ("kernel", "kernel")):
        path = "/images/" + prop(conf, key)
        off, size = (int(prop(path, field, "x"), 16) for field in ("data-position", "data-size"))
        if size <= 0 or off < int.from_bytes(data[4:8], "big") or off + size > len(data):
            raise ValueError("FIT external payload location outside input")
        section = data[off:off + size]
        hashes[key] = digest(section)
        stored = bytes(int(b, 16) for b in prop(path + "/hash", "value", "bx").split())
        if prop(path, "type") != expected_type or prop(path + "/hash", "algo") != "sha256" or bytes.fromhex(hashes[key]) != stored:
            raise ValueError(f"FIT {key} type or embedded digest mismatch")
        if key == "fdt":
            dtb = work / "board.dtb"
            dtb.write_bytes(section)
            compat = subprocess.check_output(["fdtget", "-t", "s", dtb, "/", "compatible"], text=True).split()
            if not compat or compat[0] != BOARD or "rockchip,rv1106" not in compat:
                raise ValueError("not a Q1 Pro RV1106 DTB")
        sizes.append(off + size)
    end = max(sizes)
    padded = (end + 511) // 512 * 512
    if len(data) < padded or any(data[end:padded]) or digest(data[:padded]) != FIT_87160A:
        raise ValueError("not the untouched sector-aligned stock 1.0.0_87160a FIT")
    return data, padded, hashes


def add(archive, name, content=None):
    member = tarfile.TarInfo(name)
    member.mtime = member.uid = member.gid = 0
    if content is None:
        member.type = tarfile.DIRTYPE
        member.mode = 0o755
        archive.addfile(member)
    else:
        member.mode = 0o644
        member.size = len(content)
        archive.addfile(member, io.BytesIO(content))


def release_payload(release):
    manifest = json.loads((release / "manifest.json").read_bytes())
    if manifest["sku"] != "oraybox-q1pro" or manifest["stock"] != "1.0.0_87160a" or manifest["kernel"] != "5.10.160-android":
        raise ValueError("incompatible release manifest")
    raw = (release / "q1pro-rootfs.sfoo").read_bytes()
    sq = (release / "q1pro-rootfs.squashfs").read_bytes()
    if convert(raw, False) != sq or digest(raw) != manifest["hashes"]["q1pro-rootfs.sfoo"] or digest(sq) != manifest["hashes"]["q1pro-rootfs.squashfs"]:
        raise ValueError("SFOO and normalized release hashes disagree")
    if len(raw) != manifest["image_bytes"] or manifest["volume0_capacity"] != VOLUME_SIZE:
        raise ValueError("unexpected software volume geometry")
    payload_geometry(raw)
    system = manifest["system"]
    if not system or any(ch not in "0123456789abcdefghijklmnopqrstuvwxyz.-" for ch in system):
        raise ValueError("invalid system version")
    return raw, system, manifest


def build(release, boot, factory, output, *, boot_sha256, stock_mtd6=None):
    if not factory.is_file() or digest(factory.read_bytes()) != STOCK_SQUASH_SHA256:
        raise ValueError("expected the normalized software SquashFS of factory 1.0.0_87160a")
    if stock_mtd6 is None:
        raw, system, manifest = release_payload(release)
        name = f"jetkvm-q1pro-{system}-linux-fullboot-sysupgrade.bin"
        revision = manifest["app"]["revision"]
        dist = "JetKVM Q1 Pro"
        manifest_name = "fullboot-manifest.json"
    else:
        if not stock_mtd6.is_file():
            raise ValueError("--stock-mtd6 must be a saved ordinary MTD6 file")
        raw = convert(read_stock(stock_mtd6), True)
        if digest(raw) != FACTORY_SFOO_SHA256:
            raise ValueError("original software volume differs from known stock SFOO")
        payload_geometry(raw)
        system = revision = "1.0.0_87160a"
        dist = "Local Q1 Pro factory backup"
        name = "factory-from-backup-linux-fullboot-sysupgrade.bin"
        manifest_name = "factory-from-backup-fullboot-manifest.json"
    metadata = {"metadata_version": "1.1", "compat_version": "1.0", "supported_devices": [BOARD],
                "version": {"dist": dist, "version": system, "revision": revision,
                            "target": TARGET, "board": PROFILE}}
    root = raw + b"\xff" * (VOLUME_SIZE - len(raw))
    output.mkdir(parents=True, mode=0o700 if stock_mtd6 else 0o755, exist_ok=True)
    if stock_mtd6 and (output.is_symlink() or output.stat().st_mode & 0o077):
        raise ValueError("factory recovery output requires a private directory (0700)")
    with tempfile.TemporaryDirectory(dir=output) as temp:
        work = Path(temp).resolve()
        normalized = work / "verify-padded.squashfs"
        normalized.write_bytes(convert(root, False))
        if stock_mtd6 is None:
            version = subprocess.check_output([unsquashfs(), "-cat", normalized, "version"], text=True).strip()
            app_digest = digest(subprocess.check_output([unsquashfs(), "-cat", normalized, "usr/bin/jetkvm_app"]))
            if version != system or app_digest != manifest["files"]["usr/bin/jetkvm_app"]:
                raise ValueError("release version or app readback from padded root differs")
            origin = {"app": manifest["app"], "padded_root_app_sha256": app_digest}
        else:
            identity = subprocess.check_output([unsquashfs(), "-cat", normalized, "etc/openwrt_release"], text=True)
            if f"DISTRIB_REVISION='{revision}'" not in identity.splitlines():
                raise ValueError("unexpected original OS revision in padded stock root")
            origin = {"source": "verified factory software volume 0", "stock_revision": revision}
        boot_data, fit_bytes, fit_hashes = boot_partition(boot, work, boot_sha256)
        package = work / name
        folder = "sysupgrade-" + PROFILE
        with tarfile.open(package, mode="w", format=tarfile.USTAR_FORMAT) as arc:
            add(arc, folder)
            add(arc, folder + "/CONTROL", f"BOARD={PROFILE}\n".encode())
            add(arc, folder + "/kernel", boot_data)
            add(arc, folder + "/root", root)
        meta = work / "metadata.json"
        meta.write_text(json.dumps(metadata, sort_keys=True, separators=(",", ":")) + "\n")
        sysroot = work / "factory-tools"
        subprocess.run([unsquashfs(), "-quiet", "-no-progress", "-d", sysroot, factory,
                        "usr/bin/fwtool", "lib/libc.so.0", "lib/libuClibc-1.0.31.so",
                        "lib/ld-uClibc.so.0", "lib/ld-uClibc.so.1", "lib/ld-uClibc-1.0.31.so"], check=True)
        fwtool = ["qemu-arm", "-L", sysroot, sysroot / "usr/bin/fwtool"]
        subprocess.run([*fwtool, "-I", meta, package], check=True)
        recovered = work / "checked.json"
        subprocess.run([*fwtool, "-i", recovered, package], check=True)
        if json.loads(recovered.read_bytes()) != metadata:
            raise ValueError("fwtool metadata readback differs")
        with tarfile.open(package) as arc:
            if arc.getnames() != [folder, *[folder + "/" + n for n in ("CONTROL", "kernel", "root")]]:
                raise ValueError("unexpected archive members")
            if arc.extractfile(folder + "/kernel").read() != boot_data or arc.extractfile(folder + "/root").read() != root:
                raise ValueError("archive payload differs")
        record = {"package": name, "sha256": digest(package.read_bytes()), "bytes": package.stat().st_size,
                  "metadata": metadata, **origin, "kernel_member_sha256": digest(boot_data), "kernel_member_bytes": len(boot_data),
                  "kernel_fit_sha256": digest(boot_data[:fit_bytes]), "kernel_fit_bytes": fit_bytes, "fit_components": fit_hashes, "root_sfoo_sha256": digest(raw),
                  "root_sfoo_bytes": len(raw), "root_member_sha256": digest(root), "root_member_bytes": len(root),
                  "root_erase_padding_bytes": len(root) - len(raw), "root_reserved_lebs": len(root) // LEB,
                  "factory_tools_rootfs_sha256": STOCK_SQUASH_SHA256}
        report = work / manifest_name
        report.write_text(json.dumps(record, indent=2) + "\n")
        package.chmod(0o600)
        if stock_mtd6:
            for new_file in (package, report):
                new_file.chmod(0o600)
                old_file = output / new_file.name
                if old_file.is_symlink() or (old_file.exists() and old_file.read_bytes() != new_file.read_bytes()):
                    raise FileExistsError(f"refusing to replace a different backup: {old_file}")
        os.replace(package, output / name)
        os.replace(report, output / manifest_name)
    return record


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    source = cli.add_mutually_exclusive_group()
    source.add_argument("--release", type=Path, help="Q1 JetKVM release directory (default R3)")
    source.add_argument("--stock-mtd6", type=Path, help="build a private factory recovery file from this saved NAND partition")
    cli.add_argument("--boot", type=Path, required=True, help="complete 4 MiB mtd3-boot.bin from the target Q1 Pro")
    cli.add_argument("--boot-sha256", required=True, help="SHA-256 from an independent read of the target's boot partition")
    cli.add_argument("--factory-rootfs", type=Path, default=CACHE / "usb-kernel/stock-rootfs.squashfs")
    cli.add_argument("--out", type=Path, help="output directory (for a recovery file, defaults to a private directory next to MTD6)")
    a = cli.parse_args()
    output = a.out or (a.stock_mtd6.resolve().parent / "linux-sysupgrade-rollback" if a.stock_mtd6 else REPO / "bin/q1pro-sysupgrade")
    release = a.release or REPO / "bin/q1pro-image-r3"
    print(json.dumps(build(release.resolve(), a.boot.resolve(), a.factory_rootfs.resolve(), output.absolute(),
                           boot_sha256=a.boot_sha256, stock_mtd6=a.stock_mtd6), indent=2))
