#!/usr/bin/env python3
"""Build an offline Q1 Pro software volume from an explicit hardware allowlist."""
import argparse
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from stock import convert, read_stock, VOLUME_SIZE

HERE = Path(__file__).resolve().parent
TOOLCHAIN = "arm-rockchip830-linux-uclibcgnueabihf"
KERNEL = "5.10.160-android"
SYSTEM_VERSION = "0.1.0-q1pro.4"
ALSA = "usr/lib/libasound.so.2.0.0"
FIRMWARE = ["lib/firmware/hpmcu_wrap.bin", "lib/firmware/lt7911d/lt7911d.bin"]
BLE_TOOLS = ["usr/bin/bluetoothd", "usr/bin/hciattach", "usr/bin/dbus-daemon"]
BLE_LIBRARIES = {
    "libdbus-1.so.3": "libdbus-1.so.3.26.1",
    "libexpat.so.1": "libexpat.so.1.6.12",
    "libglib-2.0.so.0": "libglib-2.0.so.0.6600.4",
    "libgthread-2.0.so.0": "libgthread-2.0.so.0.6600.4",
}
APP = "usr/bin/jetkvm_app"
SQUASH = "q1pro-rootfs.squashfs"
PAYLOAD = "q1pro-rootfs.sfoo"


def run(*args, capture=False):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE if capture else None,
                          env={**os.environ, "LC_ALL": "C"}).stdout


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def needed(path):
    return re.findall(r"\(NEEDED\).*?\[(.*?)\]", run("readelf", "-d", path, capture=True).decode())


def install(source, target, mode):
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    target.chmod(mode)


def link(path, dest):
    if not path.exists() and not path.is_symlink():
        path.symlink_to(dest)


def install_runtime(root, sdk):
    """Follow DT_NEEDED, the ELF interpreter and ALSA's explicit dlopen."""
    pending = ["ld-uClibc.so.0"]
    for path in (APP, ALSA, "usr/sbin/wpa_supplicant", "usr/bin/wpa_cli"):
        pending.extend(needed(root/path))
    done = set()
    while pending:
        soname = pending.pop()
        if soname in done:
            continue
        done.add(soname)
        source = next((p for d in ["lib", "usr/lib"] if (p := sdk/d/soname).is_file()), None)
        if source is None:
            raise RuntimeError(f"missing runtime dependency {soname}")
        resolved = source.resolve()
        target = root/"lib"/resolved.name
        if not target.exists():
            install(resolved, target, 0o755)
            pending.extend(needed(target))
        if soname != resolved.name:
            link(root/"lib"/soname, resolved.name)
    link(root/"usr/lib/libasound.so.2", "libasound.so.2.0.0")


def manifest(root):
    return {str(p.relative_to(root)): os.readlink(p) if p.is_symlink() else sha(p)
            for p in sorted(root.rglob("*")) if p.is_symlink() or p.is_file()}


def stage(args, root, work):
    sq = work/"stock.squashfs"
    sq.write_bytes(read_stock(args.mtd6))
    modules = [x.split()[0] for x in (HERE/"modules.list").read_text().splitlines() if x.strip()]
    modules.extend((HERE/"wifi.modules").read_text().splitlines())
    modules.extend(["bluetooth", "hci_uart", "aic8800_btlpm"])
    wifi_firmware = (HERE/"wifi.firmware").read_text().splitlines()
    board_files = [ALSA, *wifi_firmware, "usr/share/alsa/alsa.conf", *FIRMWARE,
                   *[f"lib/modules/{KERNEL}/{m}.ko" for m in modules]]
    board_files.extend(BLE_TOOLS)
    for soname, realname in BLE_LIBRARIES.items():
        board_files.extend([f"usr/lib/{soname}", f"usr/lib/{realname}"])
    run("unsquashfs", "-quiet", "-no-progress", "-d", root, sq, *board_files)
    for d in "bin sbin lib usr/bin usr/sbin etc dev proc sys run tmp root userdata mnt/data var/lib/dbus".split():
        (root/d).mkdir(parents=True, exist_ok=True)
    install(args.app, root/APP, 0o755)
    install(args.busybox, root/"bin/busybox", 0o755)
    install(args.dropbear, root/"usr/sbin/dropbearmulti", 0o755)
    link(root/"usr/sbin/dropbear", "dropbearmulti")
    link(root/"usr/bin/dropbearkey", "../sbin/dropbearmulti")
    install(HERE/"dropbear.sh", root/"usr/sbin/dropbear.sh", 0o755)
    (root/"etc/passwd").write_text("root:x:0:0:root:/userdata/dropbear:/bin/sh\n")
    (root/"etc/group").write_text("root:x:0:\n")
    (root/"etc/shells").write_text("/bin/sh\n")
    install(args.wifi/"wpa_supplicant", root/"usr/sbin/wpa_supplicant", 0o755)
    install(args.wifi/"wpa_cli", root/"usr/bin/wpa_cli", 0o755)
    install(HERE/"wifi.sh", root/"sbin/start-wifi", 0o755)
    bluetooth = HERE.parent/"q1pro-ble"
    install(bluetooth/"radio.sh", root/"usr/sbin/q1-ble-radio", 0o755)
    install(bluetooth/"bus.conf", root/"etc/q1-ble-bus.conf", 0o644)
    install(bluetooth/"bluez.conf", root/"etc/q1-ble-bluez.conf", 0o644)
    link(root/"var/lib/bluetooth", "/run/q1-ble/bonds")
    link(root/"var/lib/dbus/machine-id", "/run/q1-ble/machine-id")
    install(HERE/"status-led.sh", root/"sbin/q1-status-led", 0o755)
    install(HERE/"wifi.modules", root/"etc/jetkvm-wifi-modules", 0o644)
    install_runtime(root, args.sdk/TOOLCHAIN/"sysroot")
    device_tree = root/"proc/device-tree"
    device_tree.mkdir()
    (device_tree/"compatible").write_bytes(b"oraybox,q1pro\0rockchip,rv1106\0")
    app_info = json.loads(run("qemu-arm", "-L", root, root/APP, "--version-json", capture=True))
    (device_tree/"compatible").unlink()
    device_tree.rmdir()
    build_time = datetime.fromisoformat(app_info["buildDate"])
    (root/"etc/jetkvm-build-epoch").write_text(str(int(build_time.timestamp())) + "\n")
    install(HERE/"rcS", root/"etc/rcS", 0o755)
    install(HERE/"mdev.conf", root/"etc/mdev.conf", 0o644)
    install(HERE/"launch", root/"sbin/start-jetkvm", 0o755)
    install(HERE/"modules.list", root/"etc/jetkvm-modules", 0o644)
    (root/"etc/jetkvm-sku").write_text("oraybox-q1pro\n")
    (root/"version").write_text(SYSTEM_VERSION + "\n")
    (root/"etc/inittab").write_text("::sysinit:/etc/rcS\n::respawn:/sbin/start-jetkvm\n::respawn:/sbin/start-wifi\n::respawn:/sbin/q1-status-led\n::shutdown:/bin/sync\n")
    for name in ("hosts", "hostname", "resolv.conf"):
        link(root/"etc"/name, "/run/"+name)
    link(root/"etc/mtab", "/proc/self/mounts")
    link(root/"var/run", "/run")
    applets = run("qemu-arm", root/"bin/busybox", "--list", capture=True).decode().splitlines()
    for name in applets:
        if name == "busybox":
            continue
        if name in ("init", "reboot", "halt", "poweroff", "insmod", "mdev"):
            link(root/"sbin"/name, "../bin/busybox")
        else:
            link(root/"bin"/name, "busybox")
    return manifest(root)


def verify(payload, expected, work):
    raw = payload.read_bytes()
    if len(raw) > VOLUME_SIZE or not raw.startswith(b"SFOO"):
        raise ValueError("unexpected board image or volume 0 overflow")
    canonical = convert(raw, False)
    sq = work/"verify.squashfs"
    sq.write_bytes(canonical)
    root = work/"verify"
    listing = run("unsquashfs", "-lln", sq, capture=True).decode()
    for name, major, minor in [("console",5,1), ("null",1,3)]:
        if not re.search(rf"^c[^\n]+\s{major},\s*{minor}\s[^\n]+/dev/{name}$", listing, re.MULTILINE):
            raise ValueError(f"invalid /dev/{name}")
    exclude = work/"devices.exclude"
    exclude.write_text("dev/console\ndev/null\n")
    run("unsquashfs", "-quiet", "-no-progress", "-exclude-file", exclude, "-d", root, sq)
    if manifest(root) != expected:
        raise ValueError("packed payload differs from staging tree")
    for name in ["sbin/init", "bin/sh", "bin/mount", "bin/grep", "bin/tr", "bin/ip",
                 "sbin/insmod", "sbin/mdev", "bin/kill", "bin/date", "usr/sbin/q1-ble-radio", "bin/flock", "bin/start-stop-daemon", "bin/rm", "bin/mv", "usr/sbin/dropbear", "usr/bin/dropbearkey", "etc/rcS", "sbin/start-jetkvm", APP]:
        if not os.access(root/name, os.X_OK):
            raise ValueError(f"required executable missing: {name}")
    libs = [root/APP, *[root/p for p in BLE_TOOLS], root/"usr/sbin/wpa_supplicant", root/"usr/bin/wpa_cli", *[p for d in ["lib","usr/lib"] for p in (root/d).glob("*.so*") if not p.is_symlink()]]
    for binary in libs:
        for soname in needed(binary):
            if not any((root/d/soname).is_file() for d in ["lib", "usr/lib"]):
                raise ValueError(f"{binary.name} requires {soname}")
    # Test the loader and main executable with this root's libraries. The
    # synthetic DT property supplies the SoC identifier to Rockit's constructor.
    (root/"proc/device-tree").mkdir()
    (root/"proc/device-tree/compatible").write_bytes(b"oraybox,q1pro\0rockchip,rv1106\0")
    version = json.loads(run("qemu-arm", "-L", root, root/APP, "--version-json", capture=True))
    if version.get("platform") != "linux/arm":
        raise ValueError("application did not identify the expected target")
    help_text = subprocess.run(["qemu-arm", root/"sbin/mdev", "--help"], capture_output=True, check=True)
    if b"-d" not in help_text.stdout + help_text.stderr:
        raise ValueError("BusyBox mdev lacks netlink daemon support")
    run("qemu-arm", root/"bin/sh", "-n", root/"etc/rcS")
    run("qemu-arm", root/"bin/sh", "-n", root/"sbin/start-jetkvm")
    run("qemu-arm", root/"bin/sh", "-n", root/"usr/sbin/dropbear.sh")
    run("qemu-arm", root/"bin/sh", "-n", root/"usr/sbin/q1-ble-radio")
    run("qemu-arm", root/"bin/sh", "-n", root/"sbin/start-wifi")
    run("qemu-arm", root/"bin/sh", "-n", root/"sbin/q1-status-led")
    return version


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ["mtd6", "sdk", "busybox", "dropbear", "wifi", "app", "output"]:
        p.add_argument("--"+name, type=Path, required=True)
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".q1pro-", dir=args.output) as temp:
        work = Path(temp)
        root = work/"root"
        files = stage(args, root, work)
        sq, payload = work/SQUASH, work/PAYLOAD
        run("mksquashfs", root, sq, "-quiet", "-no-progress", "-noappend", "-all-root",
            "-no-xattrs", "-comp", "xz", "-b", "256K", "-processors", "2", "-all-time", "1",
            "-mkfs-time", "1", "-p", "dev/console c 0600 0 0 5 1", "-p", "dev/null c 0666 0 0 1 3")
        payload.write_bytes(convert(sq.read_bytes(), True))
        version = verify(payload, files, work)
        result = {"sku": "oraybox-q1pro", "stock": "1.0.0_87160a", "kernel": KERNEL, "system": SYSTEM_VERSION,
                  "volume0_capacity": VOLUME_SIZE, "image_bytes": payload.stat().st_size,
                  "hashes": {f.name: sha(f) for f in (sq,payload)}, "app": version,
                  "files": files}
        mf = work/"manifest.json"
        mf.write_text(json.dumps(result, sort_keys=True, ensure_ascii=False, indent=2)+"\n")
        for f in [sq, payload, mf]:
            os.replace(f, args.output/f.name)
        print(f"Verified {len(files)} files, {result['image_bytes']} of {VOLUME_SIZE} bytes")
        print("SHA-256", result["hashes"][PAYLOAD], PAYLOAD)


if __name__ == "__main__":
    main()
