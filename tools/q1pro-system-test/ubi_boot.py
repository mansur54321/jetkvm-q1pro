#!/usr/bin/env python3
"""Boot the released userspace directly from two virtual UBI volumes."""

import argparse
import hashlib
import http.cookiejar
import json
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import urllib.request

import run as guest


def make_flash(work, image, *, software_payload=None):
    state = work / "initial-state"
    state.mkdir()
    (state / "vendor-marker").write_text("preserve this file\n")
    user = state / "jetkvm-q1pro"
    keys = user / "dropbear" / ".ssh"
    keys.mkdir(parents=True, mode=0o700)
    (user / "jetkvm").mkdir()
    (user / "jetkvm/devmode.enable").touch()
    guest.run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", work / "client")
    shutil.copyfile(work / "client.pub", keys / "authorized_keys")
    (keys / "authorized_keys").chmod(0o600)
    unsquashfs = shutil.which("unsquashfs") or guest.CACHE / "image-tools/bin/unsquashfs"
    db = work / "dropbearmulti"
    with db.open("wb") as dst:
        guest.run(unsquashfs, "-cat", image, "usr/sbin/dropbearmulti", stdout=dst)
    db.chmod(0o755)
    result = guest.run("qemu-arm", "-0", "dropbearkey", db, "-t", "ed25519", "-f",
                       user / "dropbear/host_ed25519", capture_output=True, text=True)
    public = next(line.split()[:2] for line in result.stdout.splitlines() if line.startswith("ssh-ed25519 "))
    (work / "known_hosts").write_text("q1-ubi-test " + " ".join(public) + "\n")
    guest.run("mkfs.ubifs", "-U", "-r", state, "-m", "8", "-e", "126976", "-c", "434", "-o", work / "state.ubifs")
    layout = work / "volumes.ini"
    layout.write_text(
        "[software]\nmode=ubi\nimage=software.squashfs\nvol_id=0\nvol_type=dynamic\n"
        "vol_name=rootfs\nvol_size=26157056\n"
        "[settings]\nmode=ubi\nimage=state.ubifs\nvol_id=1\nvol_type=dynamic\n"
        "vol_name=rootfs_data\nvol_size=55107584\n"
    )
    (work / "software.squashfs").symlink_to(software_payload or image)
    flash = work / "flash.img"
    guest.run("ubinize", "-o", flash, "-m", "8", "-s", "8", "-O", "4032", "-p", "131072", layout, cwd=work)
    with flash.open("ab") as f:
        capacity = 83 * 1024 * 1024
        if f.tell() > capacity:
            raise ValueError("virtual volumes exceed the Q1 partition size")
        while f.tell() < capacity:
            f.write(b"\xff" * min(1024 * 1024, capacity - f.tell()))
    return flash


def test(work, kernel, image):
    flash = make_flash(work, image)
    base = ["qemu-system-aarch64", "-cpu", "cortex-a53,aarch64=off", "-m", "256M", "-smp", "1",
            "-nodefaults", "-serial", "stdio", "-monitor", "none", "-display", "none", "-no-reboot",
            "-kernel", kernel, "-drive", f"file={flash},if=none,id=flash,format=raw",
            "-device", "virtio-blk-device,drive=flash"]
    with socket.socket() as http_sock, socket.socket() as ssh_sock:
        http_sock.bind(("127.0.0.1", 0))
        ssh_sock.bind(("127.0.0.1", 0))
        port = http_sock.getsockname()[1]
        ssh_port = ssh_sock.getsockname()[1]
    net = ["-netdev", f"user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:{port}-:80,"
                      f"hostfwd=tcp:127.0.0.1:{ssh_port}-:22", *guest.NET_CARD]
    dtb = work / "virt.dtb"
    guest.run(*base, *net, "-machine", f"virt,gic-version=3,dumpdtb={dtb}", stdout=subprocess.DEVNULL)
    guest.run("fdtput", "-t", "s", dtb, "/", "compatible", "linux,dummy-virt", "oraybox,q1pro", "rockchip,rv1106")
    cmdline = ("console=ttyAMA0 earlycon=pl011,0x09000000 mem=216M loglevel=6 panic=1 "
               "block2mtd.block2mtd=/dev/vda,131072 ubi.mtd=0,4032 ubi.block=0,0 "
               "root=/dev/ubiblock0_0 rootfstype=squashfs ro")
    command = [*base, "-machine", "virt,gic-version=3", "-dtb", dtb, "-append", cmdline]
    password = secrets.token_urlsafe(24)
    for boot in range(2):
        with guest.vm(work, [*command, *net], f"boot-{boot+1}.log") as (proc, log):
            status = guest.await_status(proc, port, log)
            if status != {"isSetup": bool(boot), "sku": "oraybox-q1pro"}:
                raise RuntimeError(f"unexpected device state at boot {boot+1}: {status}")
            if not boot:
                guest.request(port, "/device/setup", {"localAuthMode": "password", "password": password})
            session = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                                  urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            guest.request(port, "/auth/login-local", {"password": password}, session)
            device = json.loads(guest.request(port, "/device", session=session))
            if device.get("authMode") != "password":
                raise RuntimeError("saved account was not accepted")
            guest.verify_ui(port)
            print(f"UBI boot {boot+1}: isSetup={status['isSetup']}, local login and interface OK", flush=True)
            # block2mtd writes through the guest page cache. Flush it before taking this VM down.
            check = """
set -eu
if grep -qw devtmpfs /proc/filesystems; then exit 1; fi
[ "$(cat /sys/class/block/ubiblock0_0/dev)" = "$(awk '$5 == "/" {print $3}' /proc/self/mountinfo)" ]
grep -q ' / squashfs ro,' /proc/mounts
grep -q 'ubi0:rootfs_data /mnt/data ubifs ' /proc/mounts
grep -q 'ubi0:rootfs_data /userdata ubifs ' /proc/mounts
if grep -qw debugfs /proc/filesystems; then
    grep -q ' /sys/kernel/debug debugfs ro,nosuid,nodev,noexec' /proc/mounts
fi
[ "$(cat /mnt/data/vendor-marker)" = 'preserve this file' ]
[ -s /mnt/data/jetkvm-q1pro/kvm_config.json ]
sync
echo 'Q1 UBIFS: sibling and configuration verified'
"""
            verified = guest.run("ssh", "-F", "/dev/null", "-T", "-p", str(ssh_port), "-i", work / "client",
                                 "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
                                 "-o", "HostKeyAlias=q1-ubi-test", "-o", "StrictHostKeyChecking=yes",
                                 "-o", f"UserKnownHostsFile={work/'known_hosts'}", "-o", "ConnectTimeout=5",
                                 "root@127.0.0.1", check, capture_output=True, text=True, timeout=15)
            if "Q1 UBIFS: sibling and configuration verified" not in verified.stdout:
                raise RuntimeError("persisted data or filesystem check failed")
            print(verified.stdout.strip(), flush=True)
    print("PASS: rootfs", hashlib.sha256(image.read_bytes()).hexdigest(), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kernel", type=Path, help="Q1 QEMU test kernel with built-in block2mtd/UBIFS and no devtmpfs")
    parser.add_argument("image", type=Path, help="released standard SquashFS")
    parser.add_argument("--work", type=Path, help="new directory in which to keep the simulated flash and serial logs")
    opts = parser.parse_args()
    if opts.work:
        opts.work.mkdir(mode=0o700)
        test(opts.work.resolve(), opts.kernel.resolve(strict=True), opts.image.resolve(strict=True))
    else:
        with tempfile.TemporaryDirectory(prefix="q1-ubi-boot-") as tmp:
            test(Path(tmp), opts.kernel.resolve(strict=True), opts.image.resolve(strict=True))
