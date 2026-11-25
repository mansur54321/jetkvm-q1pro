#!/usr/bin/env python3
"""Boot R3, interrupt and roll back one real UBI write, then boot the signed R4 and R3 again."""

import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess as sp
import sys
import tempfile
import urllib.request

repo = Path(__file__).resolve().parents[2]
cache = Path.home() / ".cache/jetkvm-q1pro"
sys.path.insert(0, str(repo / "tools/q1pro-system-test"))
import run as g  # noqa: E402
import ubi_boot  # noqa: E402

sys.path.insert(0, str(repo / "tools/q1pro-image"))
import package as package_tools  # noqa: E402


def run(*args, **kw):
    return sp.run(args, check=True, **kw)


def sha(b):
    return hashlib.sha256(b).hexdigest()


def user_state(work, port):
    lines = ssh(
        work,
        port,
        'set -e; test "$(cat /mnt/data/vendor-marker)" = "preserve this file"; test -s /userdata/kvm_config.json; /bin/busybox cat /version; for p in /userdata/dropbear/.ssh/authorized_keys /userdata/dropbear/host_ed25519 /mnt/data/vendor-marker; do /bin/busybox wc -c < "$p"; done; /bin/busybox sync',
    ).stdout
    return lines.splitlines()


def ssh(work, port, cmd):
    proc = sp.run(
        [
            "ssh",
            "-F",
            "/dev/null",
            "-p",
            str(port),
            "-i",
            str(work / "flash/client"),
            "-o",
            "BatchMode=yes",
            "-o",
            "IdentitiesOnly=yes",
            "-o",
            "IdentityAgent=none",
            "-o",
            "HostKeyAlias=q1-ubi-test",
            "-o",
            "StrictHostKeyChecking=yes",
            "-o",
            f"UserKnownHostsFile={work}/flash/known_hosts",
            "-o",
            "ConnectTimeout=5",
            "root@127.0.0.1",
            cmd,
        ],
        capture_output=True,
        text=True,
        timeout=25,
        check=False,
    )
    if proc.returncode:
        raise RuntimeError(
            f"isolated guest SSH rc={proc.returncode}: {proc.stdout[-800:]} / {proc.stderr[-1200:]}"
        )
    return proc


def fixture(work, args):
    r3 = args.r3
    f = work / "flash"
    f.mkdir()
    ubi_boot.make_flash(
        f, r3 / "q1pro-rootfs.squashfs", software_payload=r3 / "q1pro-rootfs.sfoo"
    )
    env = {
        **os.environ,
        "CGO_ENABLED": "0",
        "GOOS": "linux",
        "GOARCH": "arm",
        "GOARM": "7",
    }
    run(
        "go",
        "test",
        "-tags=q1maintenanceguest",
        "-c",
        "-trimpath",
        "-ldflags=-s -w",
        "-o",
        work / "maintenance.test",
        "./tools/q1pro-maintenance",
        cwd=repo,
        env=env,
    )
    for release, tar in [(args.r4, args.update), (args.r3, args.previous)]:
        _, payload = package_tools.verify(tar, args.key)
        if payload != (release / "q1pro-rootfs.sfoo").read_bytes():
            raise ValueError("signed package differs from supplied release")
    for label, update, previous in [
        ("r3-to-r4", args.update, args.previous),
        ("r4-to-r3", args.previous, args.update),
    ]:
        data = work / label
        data.mkdir()
        for src, dest in [
            (update, "update"),
            (previous, "previous"),
            (args.key, "owner.pub"),
            (work / "maintenance.test", "maintenance"),
        ]:
            shutil.copyfile(src, data / dest)
        (data / "maintenance").chmod(0o755)
        with (work / (label + ".ext4")).open("xb") as d:
            d.truncate(60 * 1024**2)
        run("mkfs.ext4", "-q", "-F", "-d", data, work / (label + ".ext4"))


def trial(work, args):
    fixture(work, args)
    kernel = args.kernel
    r3, r4 = args.r3, args.r4
    hp, sp_ = [], []
    for result in (hp, sp_):
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        result.append(s.getsockname()[1])
        s.close()
    hp, sp_ = hp[0], sp_[0]
    pw = secrets.token_urlsafe(24)
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar)
    )
    prev_meta = None
    for phase, ver in [
        ("injected", "0.1.0-q1pro.3"),
        ("r3-to-r4", "0.1.0-q1pro.3"),
        ("r4-to-r3", "0.1.0-q1pro.4"),
        ("final", "0.1.0-q1pro.3"),
    ]:
        disk = work / (
            ("r4-to-r3" if phase in ("r4-to-r3", "final") else "r3-to-r4") + ".ext4"
        )
        base = [
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
            "-monitor",
            "none",
            "-display",
            "none",
            "-no-reboot",
            "-kernel",
            kernel,
            "-drive",
            f"file={disk},if=none,id=fixtures,format=raw,readonly=on",
            "-device",
            "virtio-blk-device,drive=fixtures",
            "-drive",
            f"file={work}/flash/flash.img,if=none,id=flash,format=raw",
            "-device",
            "virtio-blk-device,drive=flash",
            "-netdev",
            f"user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:{hp}-:80,hostfwd=tcp:127.0.0.1:{sp_}-:22",
            *g.NET_CARD,
        ]
        dt = work / "virt.dtb"
        if not dt.exists():
            run(*base, "-M", f"virt,gic-version=3,dumpdtb={dt}", capture_output=True)
            run(
                "fdtput",
                "-t",
                "s",
                dt,
                "/",
                "compatible",
                "linux,dummy-virt",
                "oraybox,q1pro",
                "rockchip,rv1106",
            )
        cmdline = "console=ttyAMA0 earlycon mem=216M loglevel=5 panic=1 block2mtd.block2mtd=/dev/vda,131072 ubi.mtd=0,4032 ubi.block=0,0 root=/dev/ubiblock0_0 rootfstype=squashfs ro softdog.soft_margin=15"
        if phase == "injected":
            cmdline += " q1test-injected-write=1"
        cmd = [*base, "-machine", "virt,gic-version=3", "-dtb", dt, "-append", cmdline]
        with g.vm(work, cmd, phase + ".serial") as (p, log):
            status = g.await_status(p, hp, log)
            assert status["isSetup"] == (phase != "injected"), status
            if phase == "injected":
                g.request(
                    hp, "/device/setup", {"localAuthMode": "password", "password": pw}
                )
                g.request(hp, "/auth/login-local", {"password": pw}, opener)
            assert list(jar)
            device = json.loads(g.request(hp, "/device", session=opener))
            assert device.get("authMode") == "password", device.keys()
            meta = user_state(work, sp_)
            assert meta[0] == ver, meta
            if prev_meta is not None:
                assert prev_meta == meta[1:]
            prev_meta = meta[1:]
            print(
                "Q1_HANDOFF active",
                phase,
                ver,
                "old cookie accepted; SSH key and host identity retained; original user sentinel retained",
                flush=True,
            )
            if phase == "final":
                continue
            print(
                "Q1_HANDOFF softdog node",
                ssh(
                    work, sp_, "/bin/busybox cat /sys/class/watchdog/watchdog0/dev"
                ).stdout.strip(),
                flush=True,
            )
            staged = "/tmp/q1-owner/maintenance"
            shell = (
                "set -e; mkdir /tmp/q1-fixtures /tmp/q1-owner; mount -t ext4 -o ro,noload /dev/vdb /tmp/q1-fixtures; for f in update previous owner.pub maintenance; do cp /tmp/q1-fixtures/$f /tmp/q1-owner/; done; chmod 700 "
                + staged
                + "; umount /tmp/q1-fixtures; "
            )
            prep = (
                staged
                + " handoff-prepare /tmp/q1-owner/update /tmp/q1-owner/previous /tmp/q1-owner/owner.pub"
            )
            ssh(work, sp_, shell + prep)
            if phase == "injected":
                ssh(
                    work,
                    sp_,
                    staged + " handoff-discard; test ! -e /run/jetkvm-handoff",
                )
                ssh(work, sp_, prep)
            wanted = (r3 if phase == "r4-to-r3" else r4) / "q1pro-rootfs.sfoo"
            pin = sha(wanted.read_bytes())
            ssh(work, sp_, staged + " handoff-activate " + pin)
            p.wait(timeout=80)
            txt = log.read_text(errors="replace").replace("\r", "")
            marker = (
                "Q1_HANDOFF_PREVIOUS_SOFTWARE_RESTORED"
                if phase == "injected"
                else "Q1_HANDOFF_SIGNED_UPDATE_PASS"
            )
            assert p.returncode == 0 and marker in txt, txt[-2300:]
            if phase == "injected":
                assert "marker set" in txt and "actual timeout 15s" in txt
                pings = re.search(
                    r"(\d+) watchdog keepalives before final magic close", txt
                )
                assert pings and int(pings[1]) >= 16, (
                    "watchdog did not span its deadline"
                )

            for line in txt.splitlines():
                if "Q1_HANDOFF" in line or "Q1 handoff:" in line:
                    print(line.strip(), flush=True)
    print("Q1_HANDOFF_ROUNDTRIP_PASS", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--kernel",
        type=Path,
        default=cache / "usb-kernel/handoff-build/arch/arm/boot/zImage",
    )
    parser.add_argument("--r3", type=Path, default=repo / "bin/q1pro-image-r3")
    parser.add_argument("--r4", type=Path, default=repo / "bin/q1pro-image-r4-final")
    for name in ("key", "update", "previous"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument(
        "--work", type=Path, help="new directory for synthetic flash and guest logs"
    )
    args = parser.parse_args()
    for name in ("r3", "r4", "kernel", "key", "update", "previous"):
        setattr(args, name, getattr(args, name).resolve(strict=True))
    if args.work:
        args.work.mkdir(mode=0o700)
        trial(args.work.resolve(), args)
    else:
        with tempfile.TemporaryDirectory(prefix="q1-preserve-") as tmp:
            trial(Path(tmp), args)
