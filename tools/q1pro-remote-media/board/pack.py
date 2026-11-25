#!/usr/bin/env python3
"""Assemble a no-flash Q1 board RAM development profile using R4 userspace and a coherent BSP."""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from contract import MCU_FW, STOCK_FDT, audit

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
spec = importlib.util.spec_from_file_location(
    "q1_ram", HERE.parents[1] / "q1pro-ram-system/build.py"
)
ram = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ram)
RADXA_REV = "5562c254b0231be34588dc608ec28c4f1d613285"


def out(*args):
    return subprocess.check_output(args, text=True).strip()


def digest(blob):
    return hashlib.sha256(blob).hexdigest()


def norm(s):
    return s.replace("-", "_")


def exports(path):
    result = {}
    for line in path.read_text().splitlines():
        crc, symbol, *_ = line.split()
        result[symbol] = int(crc, 16)
    return result


class Drivers:
    def __init__(self, kernel, aic, dest):
        self.kernel = kernel
        self.dest = dest
        self.compat = exports(kernel / "Module.symvers") | exports(
            aic / "Module.symvers"
        )
        self.files = {norm(p.stem): p for d in (kernel, aic) for p in d.rglob("*.ko")}
        self.builtin = {
            norm(Path(line).stem)
            for line in (kernel / "modules.builtin").read_text().splitlines()
        }
        self.release = (kernel / "include/config/kernel.release").read_text().strip()
        self.records = {}

    def order(self, names):
        result = []

        def add(name):
            if name in result or name in self.builtin:
                return
            if name not in self.files:
                raise ValueError(f"missing driver or dependency: {name}")
            f = self.files[name]
            if name not in self.records:
                if out("modinfo", "-F", "vermagic", f).split()[0] != self.release:
                    raise ValueError(f"foreign module: {name}")
                versions = {
                    name: int(crc, 16)
                    for crc, name in (
                        line.split()
                        for line in out(
                            "modprobe", "--show-modversions", f
                        ).splitlines()
                    )
                }
                if "module_layout" not in versions or any(
                    self.compat.get(k) != v for k, v in versions.items()
                ):
                    raise ValueError(f"kernel symbol CRC mismatch: {name}")
                self.records[name] = {
                    "file": f.name,
                    "sha256": ram.sha(f),
                    "checked_crcs": len(versions),
                }
                shutil.copyfile(f, self.dest / f.name)
            for dep in filter(None, out("modinfo", "-F", "depends", f).split(",")):
                add(norm(dep))
            result.append(name)

        for n in names:
            add(norm(n))
        return [self.files[n].stem for n in result]


def pack(args):
    release = args.release.resolve()
    kernel = args.kernel.resolve()
    aic = args.aic_objects.resolve()
    radxa = args.radxa.resolve()
    report = json.loads((release / "manifest.json").read_bytes())
    sq = release / "q1pro-rootfs.squashfs"
    if (
        ram.sha(sq) != report["hashes"][sq.name]
        or report.get("system") != "0.1.0-q1pro.4"
    ):
        raise ValueError("verified R4 release required")
    config = (kernel / ".config").read_text().splitlines()
    for option in (
        "MTD",
        "MMC_BLOCK",
        "PSTORE",
        "WATCHDOG",
        "INITRD_ASYNC",
        "USB_DUMMY_HCD",
    ):
        if "# CONFIG_" + option + " is not set" not in config:
            raise ValueError(f"unexpected board test configuration: {option}")
    for option in (
        "BLK_DEV_NBD",
        "BLK_DEV_LOOP",
        "USB_F_FS",
        "USB_F_HID",
        "USB_F_MASS_STORAGE",
        "SQUASHFS_XZ",
        "RD_GZIP",
        "VIDEO_LT7911D",
        "ROCKCHIP_THUNDER_BOOT",
        "TUN",
        "WIREGUARD",
        "MODVERSIONS",
        "CFG80211_REQUIRE_SIGNED_REGDB",
    ):
        if "CONFIG_" + option + "=y" not in config:
            raise ValueError(f"missing board test configuration: {option}")
    if out("git", "-C", radxa, "rev-parse", "HEAD") != RADXA_REV:
        raise ValueError("unexpected SDIO firmware source revision")
    dt = args.dtb.read_bytes()
    if digest(dt) != STOCK_FDT:
        raise ValueError("expected the unmodified Q1 FIT device tree")
    hw = audit(dt)
    args.out.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=args.out) as td:
        work = Path(td).resolve()
        tree = work / "root"
        stage = work / "outer"
        for name in ("dev", "image", "lib/firmware"):
            (stage / name).mkdir(parents=True, exist_ok=True)
        exclude = work / "exclude"
        exclude.write_text("dev/console\ndev/null\nlib/modules\n")
        subprocess.run(
            [
                ram.unsquashfs(),
                "-quiet",
                "-no-progress",
                "-exclude-file",
                exclude,
                "-d",
                tree,
                sq,
            ],
            check=True,
        )
        if (tree / "lib/modules").exists():
            raise ValueError(
                "factory kernel modules must not be carried into the new ABI"
            )
        if (
            ram.sha(tree / "usr/bin/jetkvm_app")
            != report["files"]["usr/bin/jetkvm_app"]
        ):
            raise ValueError("application mismatch")
        fw = tree / "lib/firmware"
        micro = fw / "hpmcu_wrap.bin"
        if ram.sha(micro) != MCU_FW or micro.stat().st_size > 8192:
            raise ValueError("unmatched or oversized MCU firmware")
        shutil.copyfile(micro, stage / "lib/firmware/hpmcu_wrap.bin")
        version = (kernel / "include/config/kernel.release").read_text().strip()
        mods = tree / "lib/modules" / version
        mods.mkdir(parents=True)
        driver = Drivers(kernel, aic, mods)
        # These are the native RV1106 implementations, not loadable copies of the factory binaries.
        builtin_required = (
            "mpp_vcodec",
            "rockit",
            "rk_dvbm",
            "video_rkcif",
            "video_rkisp",
            "rga3",
            "usb_f_fs",
            "usb_f_mass_storage",
            "nbd",
        )
        if not set(builtin_required) <= driver.builtin:
            raise ValueError(
                f"missing built-ins: {set(builtin_required) - driver.builtin}"
            )
        if b"rockchip,rk628" in dt:
            raise ValueError(
                "this profile is only for the Q1 Pro with an LT7911D, not an RK628 board"
            )
        original = (tree / "etc/jetkvm-modules").read_text().splitlines()
        # The original package shares the optional RK628/class/CEC module list with other Oray boards.
        # This exact DT uses LT7911D; the SDK LT7911D driver has no dependency on those helpers.
        inactive = {"cec", "rockchip-hdmirx-class", "video-rk628", "rk628-csi"}
        core = driver.order(
            [
                line.split()[0]
                for line in original
                if line.strip() and line.split()[0] not in inactive
            ]
        )
        # Preload BT dependencies for the original BLE script's ordered flat insmod calls.
        core += [n for n in driver.order(["bluetooth", "hci_uart"]) if n not in core]
        (tree / "etc/jetkvm-modules").write_text("\n".join(core) + "\n")
        radio = driver.order(["cfg80211", "aic8800_bsp", "aic8800_fdrv"])
        driver.order(["aic8800_btlpm"])
        (tree / "etc/jetkvm-wifi-modules").write_text("\n".join(radio) + "\n")
        fwdir = radxa / "src/SDIO/driver_fw/fw/aic8800D80"
        fw_files = []
        # SDIO request_firmware() on the chosen driver uses basenames at /lib/firmware.
        for rel in out(
            "git",
            "-C",
            radxa,
            "ls-tree",
            "-r",
            "--name-only",
            RADXA_REV,
            "--",
            str(fwdir.relative_to(radxa)),
        ).splitlines():
            src = radxa / rel
            if not src.is_file():
                raise ValueError(f"missing Radxa firmware: {rel}")
            original_blob = subprocess.check_output(
                ["git", "-C", radxa, "show", RADXA_REV + ":" + rel]
            )
            if src.read_bytes() != original_blob:
                raise ValueError(f"modified firmware: {rel}")
            shutil.copyfile(src, fw / src.name)
            fw_files.append(src.name)
        if "fw_patch_8800d80_u02_ext0.bin" not in fw_files:
            raise ValueError("missing D80 patch table extension")
        regdb = args.regdb.resolve()
        regdb_checks = {}
        for n in ("regulatory.db", "regulatory.db.p7s"):
            shutil.copyfile(regdb / n, fw / n)
            regdb_checks[n] = ram.sha(fw / n)
        subprocess.run(
            [
                "openssl",
                "smime",
                "-verify",
                "-binary",
                "-inform",
                "DER",
                "-in",
                regdb / "regulatory.db.p7s",
                "-content",
                regdb / "regulatory.db",
                "-certfile",
                args.regdb_cert.resolve(),
                "-nointern",
                "-noverify",
                "-out",
                os.devnull,
            ],
            check=True,
        )
        cert_der = subprocess.check_output(
            ["openssl", "x509", "-in", args.regdb_cert.resolve(), "-outform", "DER"]
        )
        if cert_der not in (mods / driver.files["cfg80211"].name).read_bytes():
            raise ValueError(
                "the regulatory verification key is absent from the actual cfg80211 module"
            )
        mksquash = shutil.which("mksquashfs") or str(
            ram.CACHE / "image-tools/bin/mksquashfs"
        )
        inner = stage / "root.img"
        subprocess.run(
            [
                mksquash,
                tree,
                inner,
                "-quiet",
                "-no-progress",
                "-noappend",
                "-all-root",
                "-no-xattrs",
                "-comp",
                "xz",
                "-b",
                "256K",
                "-processors",
                "2",
                "-all-time",
                "1",
                "-mkfs-time",
                "1",
                "-p",
                "dev/console c 0600 0 0 5 1",
                "-p",
                "dev/null c 0666 0 0 1 3",
            ],
            check=True,
        )
        if (
            digest(
                subprocess.check_output(
                    [ram.unsquashfs(), "-cat", inner, "usr/bin/jetkvm_app"]
                )
            )
            != report["files"]["usr/bin/jetkvm_app"]
        ):
            raise ValueError("packed ARM binary differs")
        rc = stage / "rcS.ram"
        rc.write_text(ram.ram_rc(inner))
        rc.chmod(0o755)
        subprocess.run(
            [
                args.cc,
                "-static",
                "-Os",
                "-s",
                "-Wall",
                "-Wextra",
                "-Werror",
                ram.HERE / "init.c",
                "-o",
                stage / "init",
            ],
            check=True,
        )
        ram.pack(stage, work / "initramfs.cpio.gz", ram.DEVICES)
        shutil.copyfile(kernel / "arch/arm/boot/zImage", work / "zImage")
        shutil.copyfile(kernel / ".config", work / "kernel.config")
        (work / "factory-fit-reference.dtb").write_bytes(dt)
        (work / "hardware-contract.json").write_text(json.dumps(hw, indent=2) + "\n")
        # These are Linux kernel arguments only; memory/load addresses must come from the actual runtime FDT.
        (work / "cmdline.txt").write_text(
            "console=ttyFIQ0 rdinit=/init ro rockit.mcu_fw_path=/lib/firmware/hpmcu_wrap.bin rockit.mcu_fw_addr=0xff6fe000 rk_dma_heap_cma=24M coherent_pool=0\n"
        )
        outputs = (
            "zImage",
            "initramfs.cpio.gz",
            "factory-fit-reference.dtb",
            "hardware-contract.json",
            "kernel.config",
            "cmdline.txt",
        )
        manifest = {
            "state": "hardware unverified, RAM-only development profile",
            "hardware_boot_verified": False,
            "flash_installer": False,
            "requires_live_loader_fdt": not hw["loader_memory_bank_fixups_present"],
            "kernelrelease": version,
            "factory_kernel_modules_included": 0,
            "source_release": report["system"],
            "source_app": report["app"],
            "source_squashfs_sha256": ram.sha(sq),
            "app_sha256": report["files"]["usr/bin/jetkvm_app"],
            "inner_readonly_squashfs_sha256": ram.sha(inner),
            "inner_squashfs_bytes": inner.stat().st_size,
            "builtins": builtin_required,
            "modules": driver.records,
            "skip_inactive_rk628_stack": sorted(inactive),
            "sdio_firmware": {n: ram.sha(fw / n) for n in sorted(fw_files)},
            "signed_regdb": regdb_checks,
            "outputs": {
                n: {"sha256": ram.sha(work / n), "bytes": (work / n).stat().st_size}
                for n in outputs
            },
        }
        (work / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        for n in (*outputs, "manifest.json"):
            os.replace(work / n, args.out / n)
        print(json.dumps(manifest["outputs"], indent=2))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    for name in ("kernel", "aic-objects", "radxa", "regdb", "regdb-cert", "dtb", "out"):
        cli.add_argument("--" + name, type=Path, required=True)
    cli.add_argument("--release", type=Path, default=REPO / "bin/q1pro-image-r4")
    cli.add_argument(
        "--cc",
        default=ram.CACHE
        / "buildkit-v0.2.8/bin/arm-rockchip830-linux-uclibcgnueabihf-gcc",
    )
    pack(cli.parse_args())
