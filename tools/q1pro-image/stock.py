"""Read the geometry and known software payload of Q1 Pro 1.0.0_87160a."""
import hashlib
from pathlib import Path
import struct
import zlib

ERASE = 131072
VID = 2048
DATA = 4096
LEB = ERASE - DATA
VOLUME_SIZE = 206 * LEB
STOCK_SQUASH_SHA256 = "16cfa9f188d7c9f64e8330c7b1479cc2d5887f2f3920105236f29e7f3f331e7d"


def convert(image, to_stock):
    header = bytearray(image[:96])
    source, target = (b"hsqs", b"SFOO") if to_stock else (b"SFOO", b"hsqs")
    if len(header) != 96 or header[:4] != source:
        raise ValueError("unexpected SquashFS format")
    header[:4] = target
    header[4:8], header[8:12] = header[8:12], header[4:8]
    fields = struct.unpack_from("<4H", header, 20)
    offset = 1 if to_stock else 3
    struct.pack_into("<4H", header, 20, *(fields[offset:] + fields[:offset]))
    return bytes(header) + image[96:]


def ucrc(data):
    return zlib.crc32(data) ^ 0xffffffff


def read_stock(path: Path):
    # Ignore personal storage entirely. Only the software volume and its two
    # volume table records are needed for this specific board revision.
    blocks, layouts = {}, {}
    with path.open("rb") as src:
        if path.stat().st_size != 0x05300000:
            raise ValueError("expected Q1 Pro mtd6 partition")
        for base in range(0, path.stat().st_size, ERASE):
            src.seek(base)
            ec = src.read(64)
            if ec[:4] != b"UBI#":
                continue
            if ucrc(ec[:60]) != struct.unpack_from(">I", ec, 60)[0]:
                raise ValueError(f"invalid UBI EC header at {base}")
            if struct.unpack_from(">II", ec, 16) != (VID, DATA):
                raise ValueError("unsupported UBI offsets")
            src.seek(base + VID)
            hdr = src.read(64)
            if hdr == b"\xff" * 64:
                continue
            if hdr[:4] != b"UBI!" or ucrc(hdr[:60]) != struct.unpack_from(">I", hdr, 60)[0]:
                raise ValueError(f"invalid UBI VID header at {base}")
            vol, index = struct.unpack_from(">II", hdr, 8)
            if vol not in (0, 0x7fffefff):
                continue
            src.seek(base + DATA)
            table = blocks if vol == 0 else layouts
            if index in table:
                raise ValueError("ambiguous duplicate software or volume-table LEB")
            table[index] = src.read(LEB if vol == 0 else 344)
    if set(layouts) != {0, 1} or layouts[0] != layouts[1]:
        raise ValueError("disagreeing UBI layout records")
    for i, (count, name) in enumerate([(206, b"rootfs"), (434, b"rootfs_data")]):
        record = layouts[0][172*i:172*(i+1)]
        if (ucrc(record[:168]) != struct.unpack_from(">I", record, 168)[0]
                or struct.unpack_from(">IIIBBH", record) != (count, 1, 0, 1, 0, len(name))
                or record[16:16+len(name)] != name):
            raise ValueError("unsupported UBI volume table")
    if set(blocks) != set(range(206)):
        raise ValueError("incomplete stock software volume")
    data = b"".join(blocks[i] for i in range(206))
    used = struct.unpack_from("<Q", data, 40)[0]
    raw = data[:used]
    canonical = convert(raw, to_stock=False)
    if hashlib.sha256(canonical).hexdigest() != STOCK_SQUASH_SHA256 or convert(canonical, True) != raw:
        raise ValueError("unrecognized stock software or failed format round trip")
    return canonical
