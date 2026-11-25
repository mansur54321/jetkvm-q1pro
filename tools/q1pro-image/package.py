#!/usr/bin/env python3
"""Create and verify signed, local Q1 Pro update packages. Host files only."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import tarfile
import tempfile

from stock import VOLUME_SIZE, convert, read_stock

KIND = {
    "format": 1,
    "board": "oraybox,q1pro",
    "kernel": "5.10.160-android",
    "stock_base": "1.0.0_87160a",
    "partition": "rootfs",
    "ubi_volume": "rootfs",
    "volume_id": 0,
}
MANIFEST, SIGNATURE, IMAGE = "update.json", "update.ed25519", "rootfs.sfoo"


def payload_geometry(image):
    if not 96 <= len(image) <= VOLUME_SIZE:
        raise ValueError("payload length exceeds the Q1 software volume")
    header = convert(image[:96], False)
    if (
        struct.unpack_from("<I", header, 12)[0] != 262144
        or struct.unpack_from("<H", header, 20)[0] != 4
        or struct.unpack_from("<HH", header, 28) != (4, 0)
        or not 96 < struct.unpack_from("<Q", header, 40)[0] <= len(image)
    ):
        raise ValueError("invalid Q1 SquashFS header")


def signature(manifest, key, sig=None):
    kind = ["-pubin"] if sig is not None else []
    public = subprocess.check_output(
        ["openssl", "pkey", *kind, "-in", key, "-pubout", "-outform", "DER"]
    )
    if len(public) != 44 or not public.startswith(
        bytes.fromhex("302a300506032b6570032100")
    ):
        raise ValueError("expected an Ed25519 key")
    with tempfile.TemporaryDirectory() as d:
        tmp = Path(d)
        msg = tmp / "message"
        msg.write_bytes(manifest)
        cmd = ["openssl", "pkeyutl", "-rawin", "-in", msg, "-inkey", key]
        if sig is None:
            return subprocess.check_output(cmd + ["-sign"])
        signature_file = tmp / "signature"
        signature_file.write_bytes(sig)
        subprocess.run(
            cmd + ["-verify", "-pubin", "-sigfile", signature_file],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )


def pack(directory, key, output):
    payload = (directory / "q1pro-rootfs.sfoo").read_bytes()
    record = json.loads((directory / "manifest.json").read_bytes())
    digest = hashlib.sha256(payload).hexdigest()
    if (
        digest != record["hashes"]["q1pro-rootfs.sfoo"]
        or record["sku"] != "oraybox-q1pro"
        or record["stock"] != KIND["stock_base"]
        or record["kernel"] != KIND["kernel"]
        or record["volume0_capacity"] != VOLUME_SIZE
        or record["image_bytes"] != len(payload)
    ):
        raise ValueError("builder manifest does not match this board and payload")
    write_package(payload, record["app"]["version"], key, output)


def write_package(payload, version, key, output):
    payload_geometry(payload)
    metadata = json.dumps(
        {
            **KIND,
            "sha256": hashlib.sha256(payload).hexdigest(),
            "size": len(payload),
            "app": version,
        },
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    signed = signature(metadata, key)
    if len(signed) != 64:
        raise ValueError("expected an Ed25519 signing key")
    # Exclusive creation prevents replacing a prior release or signing key by mistake.
    with tempfile.NamedTemporaryFile(dir=output.parent) as dst:
        with tarfile.open(
            fileobj=dst, mode="w", format=tarfile.USTAR_FORMAT
        ) as archive:
            for name, data in [
                (MANIFEST, metadata),
                (SIGNATURE, signed),
                (IMAGE, payload),
            ]:
                hdr = tarfile.TarInfo(name)
                hdr.size = len(data)
                hdr.mode = 0o644
                archive.addfile(hdr, io.BytesIO(data))
        dst.flush()
        os.fsync(dst.fileno())
        os.link(dst.name, output)


def verify(package, trusted_key):
    if not package.is_file() or package.stat().st_size > VOLUME_SIZE + 32768:
        raise ValueError("invalid package size or path")
    with tarfile.open(package, mode="r:") as src:
        files = src.getmembers()
        if (
            [f.name for f in files] != [MANIFEST, SIGNATURE, IMAGE]
            or any(not f.isfile() or f.pax_headers for f in files)
            or not 1 <= files[0].size <= 4096
            or files[1].size != 64
            or files[2].size > VOLUME_SIZE
        ):
            raise ValueError("unexpected package members")

        def content(entry):
            with src.extractfile(entry) as member:
                return member.read()

        meta = content(files[0])
        signature(meta, trusted_key, content(files[1]))
        info = json.loads(meta)
        if any(info.get(name) != value for name, value in KIND.items()):
            raise ValueError(
                "signed payload targets a different board, kernel or UBI volume"
            )
        data = content(files[2])
        payload_geometry(data)
        if (
            info.get("size") != len(data)
            or info.get("sha256") != hashlib.sha256(data).hexdigest()
        ):
            raise ValueError("payload size or digest mismatch")
        return info, data


def main():
    p = argparse.ArgumentParser(description=__doc__)
    operations = p.add_subparsers(dest="op", required=True)
    create = operations.add_parser("pack")
    create.add_argument("build", type=Path)
    create.add_argument("private_key", type=Path)
    create.add_argument("package", type=Path)
    factory = operations.add_parser("pack-stock", help="package the verified factory software volume from this board's NAND backup")
    factory.add_argument("mtd6", type=Path)
    factory.add_argument("private_key", type=Path)
    factory.add_argument("package", type=Path)
    check = operations.add_parser("verify")
    check.add_argument("package", type=Path)
    check.add_argument("trusted_public_key", type=Path)
    a = p.parse_args()
    if a.op == "pack":
        pack(a.build, a.private_key, a.package)
    elif a.op == "pack-stock":
        if not a.mtd6.is_file():
            p.error("pack-stock requires a saved, regular MTD6 backup file")
        payload = convert(read_stock(a.mtd6), True)
        write_package(payload, KIND["stock_base"], a.private_key, a.package)
    else:
        print(json.dumps(verify(a.package, a.trusted_public_key)[0], sort_keys=True))


if __name__ == "__main__":
    main()
