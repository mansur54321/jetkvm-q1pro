#!/usr/bin/env python3
"""Run the unmodified Oray Linux sysupgrade validator in an ARM guest without storage devices."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

import build

spec = importlib.util.spec_from_file_location("ram_archive", build.HERE.parent / "q1pro-ram-system/build.py")
ram = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ram)


def test(image, factory, kernel, output):
    if build.digest(factory.read_bytes()) != build.STOCK_SQUASH_SHA256:
        raise ValueError("unexpected factory root")
    listing = subprocess.check_output([build.unsquashfs(), "-lln", factory], text=True).splitlines()
    files = ["bin/busybox", "sbin/sysupgrade", "sbin/uci", "usr/bin/fwtool", "usr/bin/jsonfilter",
             "usr/bin/jshn", "usr/libexec/validate_firmware_image", "lib/functions.sh", "lib/functions",
             "lib/upgrade", "lib/preinit/02_sysinfo", "usr/share/libubox/jshn.sh", "etc/board.d/01_leds"]
    for line in listing:
        if "squashfs-root/" not in line:
            continue
        path = line.split("squashfs-root/", 1)[1].split(" -> ", 1)[0]
        if line.startswith("l") and line.endswith("busybox") and "/etc/" not in path:
            files.append(path)
        elif re.fullmatch(r"lib/[^/]+\.so(?:\.[^/]*)*|usr/lib/libjson-c\.so[^/]*", path):
            files.append(path)
    with tempfile.TemporaryDirectory(prefix="q1-linux-upgrade-check-") as temp:
        work = Path(temp)
        stage = work / "fixture"
        subprocess.run([build.unsquashfs(), "-quiet", "-no-progress", "-d", stage, factory, *sorted(set(files))], check=True)
        for directory in ("dev", "proc", "tmp", "sys", "etc/config", "var/run"):
            (stage / directory).mkdir(exist_ok=True, parents=True)
        # The test input lives in /tmp, just like the regular validator expects.
        shutil.copyfile(image, stage / "tmp/release.bin")
        negative = stage / "tmp/foreign.bin"
        with tarfile.open(negative, mode="w", format=tarfile.USTAR_FORMAT) as arc:
            build.add(arc, "sysupgrade-validation-other")
        meta = work / "foreign.json"
        meta.write_text(json.dumps({"metadata_version": "1.1", "compat_version": "1.0", "supported_devices": ["validation,different-board"]}))
        subprocess.run(["qemu-arm", "-L", stage, stage / "usr/bin/fwtool", "-I", meta, negative], check=True)
        script = stage / "init"
        script.write_text("""#!/bin/sh
export PATH=/bin:/usr/bin:/sbin:/usr/sbin
test_release() {
    mount -t proc proc /proc || return 1
    mount -t sysfs sysfs /sys || return 1
    . /lib/functions.sh
    . /lib/functions/preinit.sh
    . /lib/preinit/02_sysinfo
    do_sysinfo_generic
    test "$(cat /tmp/sysinfo/board_name)" = 'oraybox,q1pro' || return 1
    echo 'Q1TEST: factory preinit detected oraybox,q1pro'
    /usr/libexec/validate_firmware_image /tmp/release.bin >/tmp/result || return 1
    jsonfilter -i /tmp/result -e '@.tests.fwtool_device_match' | grep -qx true || return 1
    jsonfilter -i /tmp/result -e '@.valid' | grep -qx true || return 1
    /sbin/sysupgrade -T /tmp/release.bin || return 1
    echo 'Q1TEST: original Oray Linux sysupgrade -T accepted the actual archive'
    /usr/libexec/validate_firmware_image /tmp/foreign.bin >/tmp/negative || return 1
    jsonfilter -i /tmp/negative -e '@.tests.fwtool_device_match' | grep -qx false || return 1
    if /sbin/sysupgrade -T /tmp/foreign.bin; then return 1; fi
    echo 'Q1TEST: mismatch rejected by the same factory model checker'
}
if test_release; then echo Q1_SYSUPGRADE_TEST_PASS; else echo Q1_SYSUPGRADE_TEST_FAIL; fi
poweroff -f
""")
        script.chmod(0o755)
        archive = work / "test.cpio.gz"
        ram.pack(stage, archive, ("console", "null"))
        args = ["qemu-system-arm", "-cpu", "cortex-a15", "-smp", "1", "-m", "512M", "-nodefaults", "-nic", "none",
                "-display", "none", "-monitor", "none", "-serial", "stdio", "-no-reboot", "-kernel", kernel, "-initrd", archive]
        dtb = work / "virt.dtb"
        subprocess.run([*args, "-M", f"virt,highmem=off,dumpdtb={dtb}"], check=True, capture_output=True)
        # Only the board identity and MTD boot selection are fixtures. There is no flash or disk.
        subprocess.run(["fdtput", "-t", "s", dtb, "/", "compatible", build.BOARD, "linux,dummy-virt", "rockchip,rv1106"], check=True)
        cmdline = "console=ttyAMA0 loglevel=4 panic=1 rdinit=/init root=/dev/ram0 storagemedia=mtd"
        result = subprocess.run([*args, "-M", "virt,highmem=off", "-dtb", dtb, "-append", cmdline],
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=90)
        log = result.stdout.decode(errors="replace").replace("\r", "")
        if result.returncode or "Q1_SYSUPGRADE_TEST_PASS" not in log:
            raise RuntimeError(log[-6500:])
        summary = [f"package SHA-256 {build.digest(image.read_bytes())}", "QEMU no NIC, no persistent devices, no forced acceptance",
                   f"fixture board={build.BOARD}, storagemedia=mtd, default compat 1.0"]
        summary += [line for line in log.splitlines() if line.startswith(("Q1TEST:", "Q1_SYSUPGRADE_TEST_PASS"))]
        output.write_text("\n".join(summary) + "\n")
        print(output.read_text(), end="")


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("image", type=Path)
    p.add_argument("--factory-rootfs", type=Path, default=build.CACHE / "usb-kernel/stock-rootfs.squashfs")
    p.add_argument("--kernel", type=Path, default=build.CACHE / "system-test/kernel/boot/vmlinuz-virt")
    p.add_argument("--log", type=Path)
    a = p.parse_args()
    test(a.image.resolve(), a.factory_rootfs.resolve(), a.kernel.resolve(), a.log or a.image.with_suffix(".validator.log"))
