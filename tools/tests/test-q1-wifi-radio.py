#!/usr/bin/env python3
"""Exercise a private WPA access point and station with mac80211_hwsim in a VM."""
import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

PROJECT = Path(__file__).resolve().parents[2]
INIT = """#!/bin/bash
export PATH=/bin
mount -t devtmpfs devtmpfs /dev
exec </dev/console >/dev/console 2>&1
finish() {
    rc=$?
    if [ "$rc" = 0 ]; then
        echo Q1-RADIO-PASS
    else
        cat /run/ap/log /run/test/log || true
        echo Q1-RADIO-FAIL
    fi
    echo b >/proc/sysrq-trigger
    while :; do sleep 1; done
}
set -eu
mount -t proc proc /proc
mount -t sysfs sys /sys
trap finish EXIT
for mod in /mods/*; do insmod "$mod"; done
ip link set lo up
ip link set wlan1 up
ip link set wlan0 up
if [ -x /bin/hostapd ]; then
    hostapd -B -f /run/ap/log /run/hostapd.conf
else
    wpa_supplicant -B -Dnl80211 -iwlan1 -c /run/ap.conf -f /run/ap/log
fi
wpa_supplicant -B -Dnl80211 -iwlan0 -c /run/test/wifi.conf -f /run/test/log
for (( i=0; i<40; i++ )); do
    if [[ -S /run/ap/ctrl/wlan1 ]]; then break; fi
    sleep .25
done
Q1_WIFI_SECURITY=$(cat /run/security) Q1_WIFI_TEST_DIR=/run/test Q1_WIFI_HWSIM=1 /wifi.test -test.v -test.run '^TestStationWithVirtualRadio$' -test.timeout=160s
"""


def command(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def add_binary(root, name):
    program = shutil.which(name)
    if not program:
        raise RuntimeError(f"missing host test utility {name}")
    shutil.copy2(program, root / "bin" / name)
    libs = command("ldd", program, capture_output=True, text=True).stdout
    for lib in set(re.findall(r"(?:^|\s)(/[^\s]+)", libs)):
        target = root / lib.lstrip("/")
        if target.exists():
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(lib, target)


def run_guest(kernel, work, security):
    root = work / "initramfs"
    for directory in ("bin", "dev", "sys", "proc", "mods", "run/ap", "run/test"):
        (root / directory).mkdir(parents=True)
    for name in ("bash", "mount", "insmod", "ip", "wpa_supplicant", "wpa_cli", "sleep", "cat"):
        add_binary(root, name)
    (root / "init").write_text(INIT)
    (root / "init").chmod(0o755)
    (root / "run/test/wifi.conf").write_text("ctrl_interface=/run/test/ctrl\nupdate_config=1\n")
    ap_auth = {
        "WPA-PSK": 'psk="wifi-simulation-key"',
        "SAE": 'ieee80211w=2\n sae_password="wifi-simulation-key"',
        "OWE": 'ieee80211w=2',
    }
    (root / "run/security").write_text(security)
    (root / "run/ap.conf").write_text(
        'ctrl_interface=/run/ap/ctrl\nnetwork={\n ssid="Q1-radio-test"\n'
        f' mode=2\n frequency=2412\n key_mgmt={security}\n proto=RSN\n pairwise=CCMP\n'
        f' {ap_auth[security]}\n}}\n')
    if shutil.which("hostapd"):
        add_binary(root, "hostapd")
        ap_key = {"WPA-PSK": "wpa_passphrase=wifi-simulation-key", "SAE": "sae_password=wifi-simulation-key", "OWE": ""}
        mfp = 0 if security == "WPA-PSK" else 2
        (root / "run/hostapd.conf").write_text(
            'interface=wlan1\ndriver=nl80211\nctrl_interface=/run/ap/ctrl\nssid=Q1-radio-test\n'
            'hw_mode=g\nchannel=1\nwpa=2\nrsn_pairwise=CCMP\n'
            f'wpa_key_mgmt={security}\nieee80211w={mfp}\n{ap_key[security]}\n')
    elif security == "OWE":
        raise RuntimeError("OWE testing requires hostapd in PATH")
    modules = {}
    for name in ("aesni_intel", "cmac", "ccm", "mac80211_hwsim"):
        dependencies = command("modprobe", "--set-version", kernel.parent.name, "--show-depends",
                               name, text=True, capture_output=True).stdout
        for line in dependencies.splitlines():
            if line.startswith("insmod "):
                modules[Path(line.split()[1])] = None
    for index, src in enumerate(modules):
        out = root / "mods" / f"{index:02d}.ko"
        if src.suffix == ".ko":
            shutil.copy2(src, out)
        else:
            decoder = {".zst": "zstd", ".xz": "xz", ".gz": "gzip"}[src.suffix]
            with out.open("wb") as dst:
                command(decoder, "-dc", src, stdout=dst)
    command("go", "test", "-c", "-o", root / "wifi.test", "./internal/q1wifi",
            cwd=PROJECT, env={**os.environ, "CGO_ENABLED": "0"})
    archive = work / "initramfs.cpio.gz"
    command("bash", "-o", "pipefail", "-c",
            'find . -print0 | cpio --quiet --owner=0:0 --null -o -H newc | gzip -1 > "$1"',
            "pack", archive, cwd=root)
    accel = ["-accel", "kvm", "-cpu", "host"] if os.access("/dev/kvm", os.R_OK | os.W_OK) else ["-accel", "tcg"]
    result = command("qemu-system-x86_64", *accel, "-m", "512", "-smp", "2",
                     "-nodefaults", "-no-reboot", "-nic", "none", "-display", "none", "-serial", "stdio",
                     "-kernel", kernel, "-initrd", archive, "-append",
                     "console=ttyS0 rdinit=/init sysrq_always_enabled panic=-1 loglevel=3",
                     capture_output=True, text=True, timeout=200)
    print(result.stdout, end="")
    if "Q1-RADIO-PASS" not in result.stdout:
        raise SystemExit("radio integration failed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kernel", type=Path, help="installed x86_64 vmlinuz under /usr/lib/modules/<release>/")
    parser.add_argument("--security", choices=("WPA-PSK", "SAE", "OWE"), default="WPA-PSK")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="q1-radio-") as temp:
        run_guest(args.kernel.resolve(), Path(temp), args.security)
