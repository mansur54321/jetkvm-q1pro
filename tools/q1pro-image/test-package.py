#!/usr/bin/env python3
"""Negative cases for authorizing an offline Q1 update file."""

import argparse
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile

from package import IMAGE, MANIFEST, SIGNATURE, pack, signature, verify


def keypair(d, name):
    private, public = d / name, d / (name + ".pub")
    subprocess.run(
        ["openssl", "genpkey", "-algorithm", "ED25519", "-out", private], check=True
    )
    subprocess.run(
        ["openssl", "pkey", "-in", private, "-pubout", "-out", public], check=True
    )
    return private, public


def replace(pkg, destination, changes):
    with tarfile.open(pkg) as old, tarfile.open(destination, "w") as new:
        for member in old:
            data = changes.get(member.name)
            if data is None:
                data = old.extractfile(member).read()
            member.size = len(data)
            new.addfile(member, io.BytesIO(data))


def reject(pkg, key):
    try:
        verify(pkg, key)
    except (ValueError, subprocess.CalledProcessError):
        return
    raise AssertionError(f"unexpectedly accepted {pkg.name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("build", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        private, public = keypair(d, "test")
        _, wrong = keypair(d, "other")
        good = d / "valid"
        pack(args.build, private, good)
        record, payload = verify(good, public)
        reject(good, wrong)
        for attr, value in [
            ("board", "another-board"),
            ("volume_id", 1),
            ("kernel", "1.0"),
            ("size", 0),
        ]:
            metadata = json.dumps({**record, attr: value}).encode()
            bad = d / attr
            replace(
                good, bad, {MANIFEST: metadata, SIGNATURE: signature(metadata, private)}
            )
            reject(bad, public)
        bad = d / "changed-bit"
        data = bytearray(payload)
        data[len(data) // 2] ^= 8
        replace(good, bad, {IMAGE: bytes(data)})
        reject(bad, public)
        print(
            "Signed package accepted; untrusted key, wrong board, kernel, volume, length and changed payload rejected."
        )


if __name__ == "__main__":
    main()
