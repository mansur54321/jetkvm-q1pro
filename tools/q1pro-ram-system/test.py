#!/usr/bin/env python3
"""Boot the RAM-only profile on QEMU, with virtio Ethernet and no writable disk."""
import argparse
import gzip
import http.cookiejar
import json
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

import build

sys.path.insert(0, str(build.HERE.parent / "q1pro-system-test"))
import run as qemu


def prepare_overlay(work, kernel, source):
    stage = work / "overlay"
    for name in ("modules", "qemu-proc", "qemu"):
        (stage / name).mkdir(parents=True)
    busy = stage / "qemu/busybox"
    busy.write_bytes(subprocess.check_output([build.unsquashfs(), "-cat", source, "bin/busybox"]))
    busy.chmod(0o755)
    names = qemu.add_modules(stage, kernel)
    (stage / "modules/loop.ko").write_bytes(gzip.decompress(
        (kernel / "lib/modules" / qemu.VERSION / "kernel/drivers/block/loop.ko.gz").read_bytes()))
    lines = ["#!/qemu/busybox sh", "set -eu", "/qemu/busybox mount -t proc proc /qemu-proc"]
    lines += ["/qemu/busybox insmod /modules/" + name for name in [*names, "loop.ko"]]
    lines += ["/qemu-monitor &", "exec /init", ""]
    (stage / "qemu-init").write_text("\n".join(lines))
    (stage / "qemu-init").chmod(0o755)
    shutil.copyfile(build.HERE / "qemu-monitor.sh", stage / "qemu-monitor")
    (stage / "qemu-monitor").chmod(0o755)
    overlay = work / "qemu-overlay.cpio.gz"
    build.pack(stage, overlay)
    return overlay


def benchmark_extracted(work, image):
    stage = work / "uncompressed-root"
    excluded = work / "exclude"
    excluded.write_text("dev/console\ndev/null\n")
    subprocess.run([build.unsquashfs(), "-quiet", "-no-progress", "-exclude-file", excluded, "-d", stage, image], check=True)
    (stage / "etc/rcS").write_text(build.ram_rc(image))
    (stage / "init").symlink_to("sbin/init")
    output = work / "extracted.cpio.gz"
    build.pack(stage, output, ("console", "null"))
    return output


def check_http(proc, port, log, auth):
    if qemu.await_status(proc, port, log) != {"isSetup": False, "sku": "oraybox-q1pro"}:
        raise RuntimeError("guest did not present an unconfigured Q1")
    jar = http.cookiejar.CookieJar()
    session = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))

    def request(path, payload=None):
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(f"http://127.0.0.1:{port}" + path, data=data, headers={"Content-Type": "application/json"})
        with session.open(req, timeout=12) as response:
            if response.status != 200:
                raise ValueError(f"{path} returned HTTP {response.status}")
            return json.load(response)

    if request("/device/status") != {"isSetup": False, "sku": "oraybox-q1pro"}:
        raise ValueError("fresh RAM state expected")
    if auth:
        password = secrets.token_urlsafe(24)
        request("/device/setup", {"localAuthMode": "password", "password": password})
        jar.clear()
        try:
            request("/device")
        except urllib.error.HTTPError as exc:
            if exc.code != 401:
                raise
        else:
            raise ValueError("anonymous request should return HTTP 401")
        request("/auth/login-local", {"password": password})
        if request("/device")["authMode"] != "password" or not request("/device/status")["isSetup"]:
            raise ValueError("authenticated device or configured state missing")
        qemu.verify_ui(port)


def check_boot(work, command, disk, overlay, port, name, auth, loop):
    combined = work / "emulated.cpio.gz"
    with combined.open("wb") as stream:
        for archive in (disk, overlay):
            with archive.open("rb") as source:
                shutil.copyfileobj(source, stream)
    with qemu.vm(work, [*command, "-initrd", combined], name + ".log") as (proc, log):
        try:
            check_http(proc, port, log, auth)
            time.sleep(2)
            serial = log.read_text(errors="replace")
            mounts = re.findall(r"^Q1_FS (.+)$", serial, flags=re.MULTILINE)
            root = next(line for line in mounts if " / / " in line)
            if loop and not (" - squashfs /dev/loop0 ro" in root and " / / ro" in root):
                raise ValueError(f"unexpected rootfs mount: {root}")
            data = next(line for line in mounts if " / /mnt/data " in line)
            user = next(line for line in mounts if " /jetkvm-q1pro /userdata " in line)
            if data.split()[2] != user.split()[2] or not all(" - tmpfs " in line and "size=16384k" in line for line in (data, user)):
                raise ValueError(f"unexpected RAM storage: {data}, {user}")
            samples = [int(x) for x in re.findall(r"Q1_MEMORY total=[0-9]+ available=([0-9]+)", serial)]
            total = int(re.search(r"Q1_MEMORY total=([0-9]+)", serial)[1])
            return {"fresh_setup": True, "authenticated": auth, "root": root, "data": data, "userdata": user,
                    "mem_total_kib": total, "min_sampled_available_kib": min(samples), "final_available_kib": samples[-1]}
        except Exception:
            print(log.read_text(errors="replace")[-3500:], file=sys.stderr)
            raise


def machine(work, kernel, port, bsp):
    if bsp:
        program = ["qemu-system-aarch64", "-cpu", "cortex-a53,aarch64=off"]
        model = "virt,gic-version=3,highmem=off"
    else:
        program = ["qemu-system-arm", "-cpu", "cortex-a15"]
        model = "virt,highmem=off"
    base = [*program, "-m", "256M", "-smp", "1", "-nodefaults", "-display", "none", "-monitor", "none",
            "-serial", "stdio", "-no-reboot", "-kernel", kernel, "-netdev",
            qemu.USER_NET + f",hostfwd=tcp:127.0.0.1:{port}-:80", *qemu.NET_CARD]
    dtb = work / ("bsp.dtb" if bsp else "virt.dtb")
    subprocess.run([*base, "-M", f"{model},dumpdtb={dtb}"], check=True, capture_output=True)
    subprocess.run(["fdtput", "-t", "s", dtb, "/", "compatible", "linux,dummy-virt", "oraybox,q1pro", "rockchip,rv1106"], check=True)
    init = "/init" if bsp else "/qemu-init"
    return [*base, "-M", model, "-dtb", dtb, "-append", f"console=ttyAMA0 rdinit={init} panic=1 mem=216M"]


def check_bsp(work, directory, image, port):
    config_file = work / "bsp.config"
    shutil.copyfile(directory / ".config", config_file)
    config = config_file.read_text().splitlines()
    for name in ("BLK_DEV_LOOP", "SQUASHFS_XZ", "BLK_DEV_INITRD", "VIRTIO_MMIO", "VIRTIO_NET"):
        if "CONFIG_" + name + "=y" not in config:
            raise ValueError("BSP requires built-in " + name)
    for name in ("DEVTMPFS", "INITRD_ASYNC"):
        if "# CONFIG_" + name + " is not set" not in config:
            raise ValueError("expected BSP test configuration with " + name + " disabled")
    kernel = work / "bsp-zImage"
    shutil.copyfile(directory / "arch/arm/boot/zImage", kernel)
    command = machine(work, kernel, port, bsp=True)
    with qemu.vm(work, [*command, "-initrd", image], "bsp.log") as (proc, log):
        try:
            check_http(proc, port, log, auth=True)
            serial = log.read_text(errors="replace")
            if "Q1 RAM: executing release /sbin/init on read-only loop0" not in serial:
                raise RuntimeError("BSP did not enter the embedded root")
        except Exception:
            print(log.read_text(errors="replace")[-3500:], file=sys.stderr)
            raise
    return {"kernel_sha256": build.sha(kernel), "config_sha256": build.sha(config_file),
            "initramfs_sha256": build.sha(image), "devtmpfs": False, "test_overlay": False,
            "fresh_setup_http": 200, "password_login_http": 200, "authenticated_device_http": 200}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, default=build.REPO / "bin/q1pro-image")
    parser.add_argument("--out", type=Path, default=build.REPO / "bin/q1pro-ram-system")
    parser.add_argument("--compare-extracted", action="store_true", help="boot a disposable fully expanded root for memory comparison")
    parser.add_argument("--bsp-build", type=Path, help="also boot the unmodified HSQS cpio with this QEMU-ready ARM BSP build")
    args = parser.parse_args()
    profile = json.loads((args.out / "manifest.json").read_text())["profiles"]["emulated-hsqs"]
    with tempfile.TemporaryDirectory(prefix="q1-ram-system-") as temp:
        work = Path(temp)
        image = work / "emulated-hsqs.cpio.gz"
        ref = work / "q1pro-rootfs.squashfs"
        shutil.copyfile(args.out / image.name, image)
        shutil.copyfile(args.release / ref.name, ref)
        if build.sha(image) != profile["initramfs_sha256"] or build.sha(ref) != profile["root_sha256"]:
            raise ValueError("RAM profile or release does not match build manifest")
        kernel = qemu.fetch_kernel(work)
        overlay = prepare_overlay(work, kernel, ref)
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        cmd = machine(work, kernel / "boot/vmlinuz-virt", port, bsp=False)
        report = {"profile": profile, "generic_kernel": qemu.VERSION, "guest_parameters": "one ARMv7 CPU, mem=216M, no disks",
                  "qemu_overlay_sha256": build.sha(overlay)}
        report["loop_boot"] = check_boot(work, cmd, image, overlay, port, "loop-boot", True, True)
        report["loop_fresh_after_power_cycle"] = check_boot(work, cmd, image, overlay, port, "loop-fresh", False, True)
        if args.compare_extracted:
            raw = benchmark_extracted(work, ref)
            report["extracted_boot"] = check_boot(work, cmd, raw, overlay, port, "extracted", True, False)
            report["extracted_cpio_gzip_bytes"] = raw.stat().st_size
            report["extracted_cpio_bytes"] = len(gzip.decompress(raw.read_bytes()))
        if args.bsp_build:
            report["bsp_full_initramfs"] = check_bsp(work, args.bsp_build, image, port)
        print(json.dumps(report, indent=2))
        (args.out / "qemu-test.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
