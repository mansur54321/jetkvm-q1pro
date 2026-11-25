#!/usr/bin/env python3
"""Exercise the Q1 maintenance tools on a disposable block2mtd in an offline ARM VM."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
TOOLS = HERE.parent
REPO = TOOLS.parent
sys.path.insert(0, str(TOOLS / "q1pro-system-test"))
import ubi_boot
sys.path.insert(0, str(TOOLS / "q1pro-image"))
import package
sys.path.insert(0, str(TOOLS / "q1pro-ram-system"))
import build as ram

spec = importlib.util.spec_from_file_location("package_examples", TOOLS / "q1pro-image/test-package.py")
examples = importlib.util.module_from_spec(spec)
spec.loader.exec_module(examples)


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def padded_sha(data, capacity=package.VOLUME_SIZE):
    if len(data) > capacity:
        raise ValueError("fixture exceeds volume")
    return hashlib.sha256(data + b"\xff" * (capacity - len(data))).hexdigest()


def make_cases(work, fixtures, release, stock_mtd6):
    key, public = examples.keypair(work, "fixture-signing")
    (fixtures / "owner.pub").write_bytes(public.read_bytes())
    valid = fixtures / "approved.tar"
    package.pack(release, key, valid)
    record, payload = package.verify(valid, public)
    # The same trusted key signs this negative case, isolating the target-volume check.
    wrong_volume = json.dumps({**record, "volume_id": 1}, sort_keys=True).encode()
    examples.replace(valid, fixtures / "wrong-volume.tar",
                     {package.MANIFEST: wrong_volume, package.SIGNATURE: package.signature(wrong_volume, key)})
    with tarfile.open(valid) as archive:
        sig = bytearray(archive.extractfile(package.SIGNATURE).read())
    sig[0] ^= 1
    examples.replace(valid, fixtures / "bad-signature.tar", {package.SIGNATURE: bytes(sig)})
    image = release / "q1pro-rootfs.squashfs"
    (fixtures / "initial.sha256").write_text(padded_sha(image.read_bytes()))
    (fixtures / "installed.sha256").write_text(padded_sha(payload))
    factory = fixtures / "factory.tar"
    run(sys.executable, "-B", TOOLS / "q1pro-image/package.py", "pack-stock", stock_mtd6.resolve(), key, factory)
    _, original = package.verify(factory, public)
    (fixtures / "factory.sha256").write_text(padded_sha(original))


def guest_init(work, ramdisk):
    stage = work / "append"
    stage.mkdir()
    rc = run("bsdtar", "-xOf", ramdisk, "rcS.ram", capture_output=True).stdout
    tail = (HERE / "test-guest.sh").read_bytes()
    script = stage / "rcS.ram"
    script.write_bytes(rc + b"\n" + tail)
    script.chmod(0o755)
    add = work / "guest-tail.cpio.gz"
    ram.pack(stage, add)
    boot = work / "test-only.cpio.gz"
    with boot.open("wb") as out:
        out.write(ramdisk.read_bytes())
        out.write(add.read_bytes())
    return boot


def boot(work, kernel, image, flash, fixture_disk, phase):
    # virt discovers MMIO transports in reverse insertion order: fixture vdb is read-only, flash is vda.
    args = ["qemu-system-aarch64", "-cpu", "cortex-a53,aarch64=off", "-m", "512M", "-smp", "2",
            "-nodefaults", "-serial", "stdio", "-monitor", "none", "-display", "none", "-nic", "none",
            "-no-reboot", "-kernel", kernel, "-initrd", image,
            "-drive", f"file={fixture_disk},id=fixture,if=none,format=raw,readonly=on",
            "-device", "virtio-blk-device,drive=fixture",
            "-drive", f"file={flash},id=nand,if=none,format=raw", "-device", "virtio-blk-device,drive=nand"]
    model = "virt,gic-version=3"
    dtb = work / f"{phase}.dtb"
    run(*args, "-machine", f"{model},dumpdtb={dtb}", capture_output=True)
    run("fdtput", "-t", "s", dtb, "/", "compatible", "linux,dummy-virt", "oraybox,q1pro", "rockchip,rv1106")
    cmdline = ("console=ttyAMA0 rdinit=/init panic=1 q1maintenance_test=1 "
               f"block2mtd.block2mtd=/dev/vda,131072 ubi.mtd=0,4032 q1maintenance_phase={phase}")
    if phase == "interrupt":
        cmdline += " ubi.block=0,0"
    serial = work / f"{phase}.log"
    with serial.open("wb") as stream:
        try:
            result = subprocess.run([*args, "-machine", model, "-dtb", dtb, "-append", cmdline],
                                    stdout=stream, stderr=subprocess.STDOUT, timeout=240)
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError("guest timed out:\n" + serial.read_text(errors="replace")[-7500:]) from exc
    log = serial.read_text(errors="replace")
    if result.returncode or "Q1_MAINTENANCE_GUEST_PASS" not in log or "--- PASS: TestVirtualUBIMaintenance" not in log:
        raise RuntimeError(log[-7500:])
    for line in log.splitlines():
        if "Q1_MAINT" in line or "--- PASS: TestVirtualUBIMaintenance" in line:
            print(line)


def test(work, args):
    if not args.stock_mtd6.is_file():
        raise ValueError("--stock-mtd6 must be a saved, ordinary file")
    release = args.release.resolve()
    ramdisk = args.ramdisk.resolve()
    record = json.loads((ramdisk.parent / "manifest.json").read_text())["profiles"]["emulated-hsqs"]
    if ram.sha(ramdisk) != record["initramfs_sha256"] or ram.sha(release / "q1pro-rootfs.squashfs") != record["root_sha256"]:
        raise RuntimeError("release and RAM image hashes must match")
    fixtures = work / "fixture-files"
    fixtures.mkdir()
    flash = ubi_boot.make_flash(work, release / "q1pro-rootfs.squashfs")
    (fixtures / "userdata.sha256").write_text(padded_sha((work / "state.ubifs").read_bytes(), 434 * 126976))
    make_cases(work, fixtures, release, args.stock_mtd6)
    env = {**os.environ, "GOOS": "linux", "GOARCH": "arm", "GOARM": "7", "CGO_ENABLED": "0"}
    run("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", fixtures / "maintenance", "./tools/q1pro-maintenance", cwd=REPO, env=env)
    run("go", "test", "-c", "-tags=q1maintenanceguest", "-trimpath", "-buildvcs=false", "-o", fixtures / "guest.test", "./tools/q1pro-maintenance", cwd=REPO, env=env)
    fixture_disk = work / "read-only-fixtures.ext4"
    with fixture_disk.open("xb") as fd:
        fd.truncate(160 * 1024 * 1024)
    run("mkfs.ext4", "-q", "-F", "-d", fixtures, fixture_disk)
    initrd = guest_init(work, ramdisk)
    for phase in ("interrupt", "recover"):
        boot(work, args.kernel.resolve(), initrd, flash, fixture_disk, phase)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--kernel", required=True, type=Path, help="QEMU BSP zImage with block2mtd, ubiblock and virtio built in")
    cli.add_argument("--stock-mtd6", required=True, type=Path, help="saved stock MTD6 file, used only to generate the signed factory software package")
    cli.add_argument("--release", type=Path, default=REPO / "bin/q1pro-image")
    cli.add_argument("--ramdisk", type=Path, default=REPO / "bin/q1pro-ram-system/emulated-hsqs.cpio.gz")
    cli.add_argument("--work", type=Path, help="new local directory to keep the serial log and disposable fixtures")
    args = cli.parse_args()
    if args.work:
        args.work.mkdir(mode=0o700)
        test(args.work.resolve(), args)
    else:
        with tempfile.TemporaryDirectory(prefix="q1-maintenance-") as temp:
            test(Path(temp), args)
