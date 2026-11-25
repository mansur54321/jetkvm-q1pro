#!/usr/bin/env python3
"""Exercise an ARM BSP's real FunctionFS and SCSI CD over dummy_hcd; no host disks or network."""

import argparse
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "ram_build", HERE.parent / "q1pro-ram-system/build.py"
)
ram = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ram)


def test(kernel, image, binary, log, libcomposite):
    with tempfile.TemporaryDirectory(prefix="q1-remote-media-") as td:
        work = Path(td)
        stage = work / "root"
        subprocess.run(
            [
                ram.unsquashfs(),
                "-quiet",
                "-no-progress",
                "-d",
                stage,
                image,
                "bin/busybox",
                "lib/*.so*",
                "usr/lib/*.so*",
            ],
            check=True,
        )
        for folder in ("dev", "tmp", "sys", "proc", "userdata/jetkvm/images"):
            (stage / folder).mkdir(parents=True, exist_ok=True)
        shutil.copyfile(binary, stage / "test")
        (stage / "test").chmod(0o755)
        if libcomposite:
            shutil.copyfile(libcomposite, stage / "libcomposite.ko")
            device_init = "$b insmod /libcomposite.ko || exit 1"
            tests = ["TestQ1KernelWithoutRemoteMediaSupport"]
            mode = "q1media_without_ffs=1"
        else:
            device_init = "$b mount -t devtmpfs devtmpfs /dev || exit 1"
            tests = ["TestQ1RemoteISOOverUSB", "TestRemoteNBDReadsLastSector"]
            mode = "q1media_disposable=1"
        init = stage / "init"
        init.write_text(f"""#!/bin/busybox sh
b=/bin/busybox
$b mount -t proc proc /proc || exit 1
$b mount -t sysfs sysfs /sys || exit 1
{device_init}
$b mount -t configfs configfs /sys/kernel/config || exit 1
$b ip link set lo up || exit 1
if JETKVM_TEST_DISPOSABLE_NBD=1 /test -test.v -test.run '^({"|".join(tests)})$' -test.timeout 90s; then
    $b echo Q1_MEDIA_PASS
else
    $b dmesg | $b tail -n 50
    $b echo Q1_MEDIA_FAIL
fi
$b poweroff -f
""")
        init.chmod(0o755)
        archive = work / "fixture.cpio.gz"
        ram.pack(stage, archive, ("console", "null"))
        args = [
            "qemu-system-aarch64",
            "-cpu",
            "cortex-a53,aarch64=off",
            "-m",
            "256M",
            "-smp",
            "1",
            "-nodefaults",
            "-nic",
            "none",
            "-display",
            "none",
            "-monitor",
            "none",
            "-serial",
            "stdio",
            "-no-reboot",
            "-kernel",
            kernel,
            "-initrd",
            archive,
        ]
        dtb = work / "virt.dtb"
        model = "virt,gic-version=3"
        subprocess.run(
            [*args, "-machine", f"{model},dumpdtb={dtb}"],
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
        with log.open("wb") as out:
            rc = subprocess.run(
                [
                    *args,
                    "-machine",
                    model,
                    "-dtb",
                    dtb,
                    "-append",
                    "console=ttyAMA0 loglevel=4 panic=1 rdinit=/init " + mode,
                ],
                stdout=out,
                stderr=subprocess.STDOUT,
                timeout=110,
            ).returncode
        result = log.read_text(errors="replace")
        if (
            rc
            or "Q1_MEDIA_PASS" not in result
            or any("--- PASS: " + case not in result for case in tests)
        ):
            raise RuntimeError(result[-5600:])
        print("\n".join(line for line in result.splitlines() if "Q1_MEDIA" in line))


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("kernel", "rootfs", "test-binary", "log"):
        p.add_argument("--" + name, type=Path, required=True)
    p.add_argument(
        "--without-ffs",
        type=Path,
        metavar="LIBCOMPOSITE_KO",
        help="test the fallback on a separate BSP guest, loading only its own libcomposite",
    )
    a = p.parse_args()
    test(
        a.kernel.resolve(),
        a.rootfs.resolve(),
        a.test_binary.resolve(),
        a.log.resolve(),
        a.without_ffs,
    )
