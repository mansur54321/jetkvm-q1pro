#!/usr/bin/env python3
"""Validate the final board initramfs under a separate ARM control kernel and log the actual vendor-kernel limit."""

import argparse
import hashlib
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1] / "q1pro-system-test"))
import run as guest  # noqa: E402


def sha(p):
    with p.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def check(work, profile, control):
    manifest = json.loads((profile / "manifest.json").read_bytes())
    for name, info in manifest["outputs"].items():
        if sha(profile / name) != info["sha256"]:
            raise ValueError(f"artifact mismatch: {name}")
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    qemu = [
        "qemu-system-aarch64",
        "-cpu",
        "cortex-a53,aarch64=off",
        "-smp",
        "1",
        "-m",
        "256M",
        "-nodefaults",
        "-no-reboot",
        "-display",
        "none",
        "-monitor",
        "none",
        "-serial",
        "stdio",
    ]
    model = "virt,gic-version=3"
    dtb = work / "virt.dtb"
    subprocess.run(
        [*qemu, "-kernel", control, "-M", f"{model},dumpdtb={dtb}"],
        capture_output=True,
        check=True,
    )
    subprocess.run(
        [
            "fdtput",
            "-t",
            "s",
            dtb,
            "/",
            "compatible",
            "linux,dummy-virt",
            "oraybox,q1pro",
            "rockchip,rv1106",
        ],
        check=True,
    )
    boot = [*qemu, "-M", model, "-dtb", dtb, "-initrd", profile / "initramfs.cpio.gz"]
    cmdline = "console=ttyAMA0 loglevel=5 panic=1 mem=216M rdinit=/init rockit.mcu_fw_path=/lib/firmware/hpmcu_wrap.bin rockit.mcu_fw_addr=0xff6fe000"
    net = [
        "-netdev",
        f"{guest.USER_NET},hostfwd=tcp:127.0.0.1:{port}-:80",
        *guest.NET_CARD,
    ]
    with guest.vm(
        work, [*boot, "-kernel", control, *net, "-append", cmdline], "control.log"
    ) as (proc, log):
        status = guest.await_status(proc, port, log)
        if status != {"isSetup": False, "sku": "oraybox-q1pro"}:
            raise ValueError(f"bad first-boot response: {status}")
        guest.verify_ui(port)
        index = guest.request(port, "/")
        control_text = log.read_text(errors="replace")
        if (
            b"index-Lft3ZBuV.js" not in index
            or "Q1 RAM: executing release /sbin/init on read-only loop0"
            not in control_text
        ):
            raise ValueError("unmodified full board initramfs did not reach R4")
    actual = profile / "zImage"
    with guest.vm(
        work,
        [*boot, "-kernel", actual, "-nic", "none", "-append", cmdline],
        "sdk-video.log",
    ) as (proc, log):
        until = time.monotonic() + 35
        while time.monotonic() < until and proc.poll() is None:
            text = log.read_text(errors="replace")
            if "Kernel panic" in text:
                break
            time.sleep(0.25)
        text = log.read_text(errors="replace")
    result = {
        "initramfs": sha(profile / "initramfs.cpio.gz"),
        "actual_board_zImage": sha(actual),
        "control_kernel": sha(control),
        "control_vm": "QEMU virt, RAM-only, no disk; different kernel and no Rockit MCU hardware",
        "control_root": "squashfs loop0 read-only",
        "control_get_device_status_http": 200,
        "control_device_status": status,
        "control_r4_ui_entry": "index-Lft3ZBuV.js",
        "exact_vendor_kernel_on_virt": "pre-PID1 MCU exception"
        if "Kernel panic" in text and "vmcu_probe" in text
        else "not established",
    }
    (profile / "qemu-check.json").write_text(json.dumps(result, indent=2) + "\n")
    for filename, src in (
        ("control-boot.log", work / "control.log"),
        ("vendor-virt.log", work / "sdk-video.log"),
    ):
        (profile / filename).write_bytes(src.read_bytes())
    print(json.dumps(result, indent=2))
    if result["exact_vendor_kernel_on_virt"] != "pre-PID1 MCU exception":
        raise RuntimeError("unexpected exact-kernel outcome; inspect vendor-virt.log")


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("profile", type=Path)
    p.add_argument("--control-kernel", required=True, type=Path)
    a = p.parse_args()
    with tempfile.TemporaryDirectory(prefix="q1-ram-profile-validation-") as d:
        check(Path(d), a.profile.resolve(), a.control_kernel.resolve())
