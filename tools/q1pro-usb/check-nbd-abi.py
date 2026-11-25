#!/usr/bin/env python3
"""Compare an ARM nbd.ko with exports in the Q1 Pro 1.0.0_87160a kernel."""

import argparse
import hashlib
import lzma
from pathlib import Path
import re
import struct
import subprocess
import sys


IMAGE_SHA256 = "b21640375423c68c12fd176efd4902bc88ae0cb4a8525edca523bd0286ce031e"
VERMAGIC = "5.10.160 mod_unload modversions ARMv7 thumb2 p2v8"


def kernel_exports(boot):
    # FIT's external zImage contains an XZ signature in its decompressor as
    # well as the compressed ARM Image. Only use the byte-verified Image.
    for match in re.finditer(b"\xfd7zXZ\x00", boot):
        decoder = lzma.LZMADecompressor()
        try:
            image = decoder.decompress(boot[match.start():], max_length=8 * 1024 * 1024)
        except lzma.LZMAError:
            continue
        if decoder.eof and hashlib.sha256(image).hexdigest() == IMAGE_SHA256:
            break
    else:
        raise ValueError("cannot find the expected Q1 Pro ARM kernel Image")

    # This Image has three-word ARM kernel_symbol entries. The corresponding
    # CRC table uses one word per entry in the same linker order.
    virtual_base = 0xb0008000
    name_anchor = 0x5791f0  # address of the module_layout name pointer
    crc_anchor = 0x58de6c   # module_layout CRC (0x69774b8d)

    def symbol_at(offset):
        address = struct.unpack_from("<I", image, offset)[0] - virtual_base
        if not 0 <= address < len(image):
            return None
        end = image.find(b"\0", address, address + 256)
        name = image[address:end] if end >= 0 else b""
        return name.decode("ascii") if re.fullmatch(rb"[A-Za-z0-9_.]+", name) else None

    first = name_anchor
    while symbol_at(first - 12):
        first -= 12
    exports = {}
    offset = first
    while (name := symbol_at(offset)) is not None:
        crc_offset = crc_anchor + (offset - name_anchor) // 3
        exports[name] = struct.unpack_from("<I", image, crc_offset)[0]
        offset += 12
    return exports


def imports(module):
    output = subprocess.check_output(["modprobe", "--show-modversions", module], text=True)
    return {name: int(crc, 16) for crc, name in (line.split() for line in output.splitlines())}


def check(boot, module):
    header = module.read_bytes()[:20]
    if header[:7] != b"\x7fELF\x01\x01\x01" or header[16:20] != b"\x01\x00\x28\x00":
        raise ValueError("expected an ELF32 little-endian ARM relocatable module")
    for field, expected in (("name", "nbd"), ("vermagic", VERMAGIC)):
        actual = subprocess.check_output(["modinfo", "-F", field, module], text=True).strip()
        if actual != expected:
            raise ValueError(f"{field}: {actual!r}, expected {expected!r}")
    expected = kernel_exports(boot.read_bytes())
    required = imports(module)
    undefined = subprocess.check_output(["nm", "-u", "--format=posix", module], text=True)
    missing_versions = {line.split()[0] for line in undefined.splitlines()
                        if len(line.split()) >= 2 and line.split()[1] == "U"} - required.keys()
    if "module_layout" not in required or missing_versions:
        raise ValueError(f"incomplete module symbol versions: {sorted(missing_versions)}")
    errors = []
    for name, crc in sorted(required.items()):
        actual = expected.get(name)
        if actual != crc:
            stock = f"{actual:#010x}" if actual is not None else "missing"
            errors.append(f"{name}: module {crc:#010x}, kernel {stock}")
    if errors:
        raise ValueError(f"{len(errors)} of {len(required)} incompatible kernel imports:\n" + "\n".join(errors))
    print(f"ARM, vermagic and all {len(required)} versioned imports match the stock kernel")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("boot", type=Path, help="stock mtd3 boot partition")
    parser.add_argument("module", type=Path, help="independently built nbd.ko")
    args = parser.parse_args()
    try:
        check(args.boot, args.module)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(str(exc))
