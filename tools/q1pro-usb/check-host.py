#!/usr/bin/env python3
"""Check Q1 Pro USB enumeration from the attached Linux computer."""

import argparse
import hashlib
from pathlib import Path
import sys


# Subclass, protocol, interrupt IN packet size and descriptor from host sysfs.
# Stock entries are from 1.0.0_87160a. JetKVM entries match internal/usbgadget.
STOCK = [
    ("keyboard", 1, 1, 8, "81c9b5a7d7ecf4b95e29ce9b164556dcabd48b7a4eda208cac92a53506580de8"),
    ("relative mouse", 1, 2, 5, "ccb3c0cd06a21c92e9751c3f1413e0b185317046589f2727f6fa5209fa689512"),
    ("absolute mouse", 1, 2, 7, "f64612a0513207777371825e22839cf1a53a26ef0b79e840cdf37bcde1585096"),
    ("multitouch", 0, 0, 44, "a4dc1148211a3bf64ddd8d1467459ecad8d091afdbab7a1a04ae32125672a056"),
]
JETKVM = [
    ("keyboard", 1, 1, 8, "f3e62a32638f14efff361d5f49be6a23cce36f13ff900522aedd41935b3c3721"),
    ("absolute mouse", 0, 2, 6, "31ed91fe5a53642cefa538d1376b92c2e6034e533648aa21a3d6ac156a8acb94"),
    ("relative mouse", 1, 2, 5, "92155ddf022d4091f5e15eea5871107cd6ff7a4e6ada0ef931bd14b86c5632d0"),
    ("wake", 0, 0, 1, "a003424f933a7c9fce0ae07b49c213ec6b9fe76b5ebd71dd906aaeba80c30ab6"),
]


def read(path):
    return path.read_text().strip()


def endpoints(interface):
    return sorted((read(ep / "direction"), read(ep / "type"), int(read(ep / "wMaxPacketSize"), 16))
                  for ep in interface.glob("ep_*"))


def check(profile, port, bus=Path("/sys/bus/usb/devices")):
    vendor, product = ("0525", "a4ac") if profile == "stock" else ("1d6b", "0104")
    devices = [p.parent for p in bus.glob("*/idVendor")
               if (not port or p.parent.name == port) and read(p) == vendor
               and read(p.parent / "idProduct") == product]
    if len(devices) != 1:
        matches = ", ".join(p.name for p in devices) or "none"
        raise ValueError(f"expected one {vendor}:{product}, matching USB ports: {matches}")
    device = devices[0]
    print(f"{device.name} {vendor}:{product}, {read(device / 'speed')} Mbit/s")
    interfaces = sorted(bus.glob(f"{device.name}:*"), key=lambda p: int(read(p / "bInterfaceNumber"), 16))
    hid = [p for p in interfaces if read(p / "bInterfaceClass") == "03"]
    expected = STOCK if profile == "stock" else JETKVM
    if len(hid) != len(expected):
        raise ValueError(f"expected {len(expected)} HID interfaces, found {len(hid)}")
    for interface, (name, subclass, protocol, size, digest) in zip(hid, expected):
        actual_endpoints = endpoints(interface)
        wanted_endpoints = [("in", "Interrupt", size)]
        if name == "keyboard":
            wanted_endpoints.append(("out", "Interrupt", size))
        if actual_endpoints != wanted_endpoints:
            raise ValueError(f"{name}: unexpected endpoints {actual_endpoints}")
        if (int(read(interface / "bInterfaceSubClass"), 16), int(read(interface / "bInterfaceProtocol"), 16)) != (subclass, protocol):
            raise ValueError(f"{name}: unexpected USB subclass/protocol")
        descriptors = list(interface.glob("*/report_descriptor"))
        if len(descriptors) != 1:
            raise ValueError(f"{name}: expected one bound HID report descriptor")
        actual = hashlib.sha256(descriptors[0].read_bytes()).hexdigest()
        if actual != digest:
            raise ValueError(f"{name}: HID descriptor mismatch: {actual}")
        print(f"{interface.name} {name}, {size}-byte interrupt IN, descriptor OK")

    storage = [p for p in interfaces if read(p / "bInterfaceClass") == "08"]
    if profile == "jetkvm":
        if len(storage) != 1:
            raise ValueError(f"expected one virtual media interface, found {len(storage)}")
        interface = storage[0]
        if (read(interface / "bInterfaceSubClass"), read(interface / "bInterfaceProtocol")) != ("06", "50"):
            raise ValueError("unexpected mass storage subclass/protocol")
        if [(d, t) for d, t, _ in endpoints(interface)] != [("in", "Bulk"), ("out", "Bulk")]:
            raise ValueError("mass storage must expose bulk IN and OUT endpoints")
        print(f"{interface.name} USB mass storage, bulk IN and OUT")
    elif len(interfaces) != len(hid):
        raise ValueError("unexpected additional interface in stock default configuration")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", choices=("stock", "jetkvm"), help="expected default USB profile")
    parser.add_argument("--port", help="Linux USB port, e.g. 3-4, when multiple devices match")
    args = parser.parse_args()
    try:
        check(args.profile, args.port)
    except (OSError, ValueError) as exc:
        sys.exit(str(exc))
