#!/usr/bin/env python3
"""Read-only cross-check of an original Q1 FDT and the supported SDK MCU layout."""

import argparse
import hashlib
import json
from pathlib import Path
import struct

import libfdt

STOCK_FDT = "ed26ec4480aab30e3c02236341e1c64eff17949f40c960c152c42f233c9dfffe"
MCU_FW = "7c6c18c71183b4874ceb5b4e24b96d4221f1b2e877bc049f67fcab3c9ea1b236"
# RV1106 addresses used by the SDK's published mcu.S .L124 and mcu_fw_addr.
MCU_MMIO = [
    0xFF3B8A04,
    0xFF076044,
    0xFF5C0008,
    0xFF5C000C,
    0xFF5C0030,
    0xFF5C0034,
    0xFFA00414,
]
BOARD_PATHS = (
    "/i2c@ff320000/lt7911d@2b",
    "/mipi0-csi2",
    "/rkcif-mipi-lvds",
    "/csi2-dphy0",
    "/csi2-dphy-hw@ff3e8000",
    "/rkcif@ffa10000",
    "/rkisp@ffa00000",
    "/rga@ff980000",
    "/rkvenc@ffa50000",
    "/rkvenc-pp@ffa60000",
    "/usbdrd/usb@ffb00000",
    "/ethernet@ffa80000",
    "/mmc@ff9a0000",
    "/sdio-pwrseq",
    "/serial@ff4f0000",
    "/i2s@ffae0000",
    "/dpin-sound",
    "/sram@ff6c0000",
    "/sram@ff6c0000/hpmcu-sram@3e000",
    "/clock-controller@ff3a0000",
    "/mailbox@ff5c0000",
)


def audit(data):
    dt = libfdt.Fdt(data)

    def prop(node, key):
        val = dt.getprop(node, key, quiet=(libfdt.NOTFOUND,))
        return b"" if isinstance(val, int) else bytes(val)

    def words(data):
        return (
            list(struct.unpack(f">{len(data) // 4}I", data))
            if data and len(data) % 4 == 0
            else []
        )

    def strings(data):
        return [v.decode() for v in data.rstrip(b"\0").split(b"\0") if v]

    def hexwords(data):
        return [hex(v) for v in words(data)]

    def references(node, key, count):
        args = words(prop(node, key))
        result = []
        while args:
            n = dt.node_offset_by_phandle(args.pop(0))
            num = words(prop(n, count))
            if len(num) != 1 or num[0] > len(args):
                raise ValueError(f"invalid {key} phandle")
            result.append({"provider": dt.get_path(n), "args": args[: num[0]]})
            del args[: num[0]]
        return result

    paths = {}
    bank = []
    reserved = []
    resources = []
    off = -1
    depth = 0
    while True:
        off, depth = dt.next_node(off, depth, quiet=(libfdt.NOTFOUND,))
        if off < 0 or depth < 0:
            break
        name = dt.get_path(off)
        paths[name] = off
        if strings(prop(off, "device_type")) == ["memory"]:
            bank.append({"path": name, "reg": hexwords(prop(off, "reg"))})
        if name.startswith("/reserved-memory/") and name.count("/") == 2:
            reserved.append({"path": name, "reg": hexwords(prop(off, "reg"))})
        if name.count("/") == 1:
            reg = words(prop(off, "reg"))
            if reg and len(reg) % 2 == 0:
                resources.extend(
                    (reg[i], reg[i] + reg[i + 1], name) for i in range(0, len(reg), 2)
                )
    if strings(prop(0, "compatible"))[:2] != ["oraybox,q1pro", "rockchip,rv1106"]:
        raise ValueError("unexpected device-tree identity")
    nodes = {}
    for name in BOARD_PATHS:
        node = paths[name]
        selected = {
            "compatible": strings(prop(node, "compatible")),
            "status": (strings(prop(node, "status")) or ["okay"])[0],
            "reg_cells": hexwords(prop(node, "reg")),
            "irqs": words(prop(node, "interrupts")),
            "clocks": references(node, "clocks", "#clock-cells"),
            "resets": references(node, "resets", "#reset-cells"),
            "dmas": references(node, "dmas", "#dma-cells"),
        }
        for key, count in (
            ("phys", "#phy-cells"),
            ("iommus", "#iommu-cells"),
            ("power-domains", "#power-domain-cells"),
        ):
            if prop(node, key):
                selected[key] = references(node, key, count)
        parent = node
        while parent >= 0:
            ph = words(prop(parent, "interrupt-parent"))
            if ph:
                selected["interrupt_controller"] = dt.get_path(
                    dt.node_offset_by_phandle(ph[0])
                )
                break
            if parent == 0:
                break
            parent = dt.parent_offset(parent)
        for key in (
            "mmc-pwrseq",
            "vmmc-supply",
            "vqmmc-supply",
            "phy-supply",
            "interrupt-parent",
        ):
            ph = words(prop(node, key))
            if ph:
                selected[key] = dt.get_path(dt.node_offset_by_phandle(ph[0]))
        for key in ("pinctrl-0", "pinctrl-1"):
            ph = words(prop(node, key))
            if ph:
                selected[key] = [dt.get_path(dt.node_offset_by_phandle(v)) for v in ph]
        for key in ("reset-gpios", "power-gpios", "plugin-det-gpios"):
            if prop(node, key):
                selected[key] = references(node, key, "#gpio-cells")
        for key in (
            "dr_mode",
            "phy-names",
            "clock-names",
            "reset-names",
            "interrupt-names",
            "dma-names",
        ):
            if prop(node, key):
                selected[key] = strings(prop(node, key))
        for key in (
            "dma-coherent",
            "non-removable",
            "cap-sdio-irq",
            "keep-power-in-suspend",
        ):
            if not isinstance(dt.getprop(node, key, quiet=(libfdt.NOTFOUND,)), int):
                selected[key] = True
        nodes[name] = selected
    sram = words(prop(paths["/sram@ff6c0000"], "reg"))
    child = words(prop(paths["/sram@ff6c0000/hpmcu-sram@3e000"], "reg"))
    if sram != [0xFF6C0000, 0x40000] or child != [0x3E000, 0x2000]:
        raise ValueError("factory MCU window mismatch")
    endpoint_links = []
    for name, off in paths.items():
        if "endpoint" not in name or not name.startswith(
            (
                "/i2c@ff320000/lt7911d@2b",
                "/mipi0-csi2",
                "/csi2-dphy0",
                "/rkcif-mipi-lvds",
            )
        ):
            continue
        ph = words(prop(off, "remote-endpoint"))
        if ph:
            other = dt.node_offset_by_phandle(ph[0])
            if words(prop(other, "remote-endpoint")) != [dt.get_phandle(off)]:
                raise ValueError("non-reciprocal CSI link")
            endpoint_links.append([name, dt.get_path(other)])
    # Linux FDT header reservation list is independent of the /reserved-memory nodes.
    reserve_map = []
    for i in range(dt.num_mem_rsv()):
        start, size = dt.get_mem_rsv(i)
        reserve_map.append([hex(start), hex(size)])
    return {
        "fdt_sha256": hashlib.sha256(data).hexdigest(),
        "model": strings(prop(0, "model")),
        "dram_banks": bank,
        "reserved_memory": reserved,
        "fdt_reservations": reserve_map,
        "csi_graph": endpoint_links,
        "mcu": {
            "firmware_address": "0xff6fe000",
            "firmware_max_bytes": 8192,
            "firmware_sha256": MCU_FW,
            "driver_registers_in_top_level_reg": {
                "0x%08x" % reg: [p for a, b, p in resources if a <= reg < b]
                for reg in MCU_MMIO
            },
        },
        "nodes": nodes,
        "loader_memory_bank_fixups_present": bool(bank),
    }


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("fdt", type=Path)
    p.add_argument("--out", type=Path)
    a = p.parse_args()
    r = audit(a.fdt.read_bytes())
    text = json.dumps(r, indent=2, ensure_ascii=False) + "\n"
    if a.out:
        a.out.write_text(text)
    else:
        print(text, end="")
