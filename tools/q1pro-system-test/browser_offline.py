#!/usr/bin/env python3
"""Exercise the packed Q1 web UI and WebRTC in an isolated Linux network."""
import contextlib
import os
from pathlib import Path
import shutil
import socket
import sys
import tempfile

import run as guest


def browser_test(work):
    if [name for _, name in socket.if_nameindex()] != ["lo"]:
        raise RuntimeError("browser test requires an empty network namespace")
    guest.run("ip", "tuntap", "add", "dev", "q1vm", "mode", "tap")
    guest.run("ip", "addr", "add", "192.0.2.1/24", "dev", "q1vm")
    guest.run("ip", "link", "set", "q1vm", "up")
    rootfs = guest.HERE.parents[1] / "bin/q1pro-image/q1pro-rootfs.squashfs"
    image, kernel, command = guest.prepare_guest(work, rootfs, "tap,id=net0,ifname=q1vm,script=no,downscript=no")
    initrd = guest.prepare_boot(work, kernel, image, shutil.which("unsquashfs") or guest.CACHE / "image-tools/bin/unsquashfs",
                                guest.CACHE / "buildkit-v0.2.8", True)
    guest_ip = "192.0.2.50"
    with contextlib.ExitStack() as processes:
        dhcp = ["dnsmasq", "--conf-file=/dev/null", "--no-daemon", "--log-facility=-", "--user=root",
                "--group=root", "--port=0", "--interface=q1vm", "--bind-interfaces",
                f"--dhcp-range={guest_ip},{guest_ip},255.255.255.0,1h", "--dhcp-option=3", "--dhcp-option=6",
                f"--dhcp-leasefile={work / 'leases'}", f"--pid-file={work / 'dnsmasq.pid'}"]
        processes.enter_context(guest.vm(work, dhcp, "dhcp.log"))
        proc, log = processes.enter_context(guest.vm(work, [*command, "-initrd", initrd], "guest.log"))
        status = guest.await_status(proc, 80, log, host=guest_ip)
        if status != {"isSetup": False, "sku": "oraybox-q1pro"}:
            raise RuntimeError(f"expected fresh Q1 rootfs, got {status}")
        modules = [guest.HERE.parents[1] / "ui/node_modules"]
        if os.environ.get("NODE_PATH"):
            modules.append(os.environ["NODE_PATH"])
        env = {**os.environ, "HOME": str(work), "NODE_PATH": os.pathsep.join(map(str, modules))}
        guest.run("node", guest.HERE / "testdata/browser-offline.cjs", f"http://{guest_ip}",
                  env=env, timeout=120)


if __name__ == "__main__":
    root = guest.HERE.parents[1]
    if len(sys.argv) == 2 and sys.argv[1] == "--isolated":
        if os.readlink("/proc/self/ns/net") == os.environ["Q1_TEST_PARENT_NETNS"]:
            sys.exit("refusing to run in the caller's network namespace")
        with tempfile.TemporaryDirectory(prefix="q1-browser-") as temp:
            browser_test(Path(temp))
    elif len(sys.argv) == 1:
        os.execvp("bwrap", ["bwrap", "--unshare-user", "--uid", "0", "--gid", "0", "--unshare-net",
                  "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--new-session",
                  "--cap-add", "CAP_NET_ADMIN", "--cap-add", "CAP_NET_RAW", "--cap-add", "CAP_NET_BIND_SERVICE", "--ro-bind", "/", "/",
                  "--dev", "/dev", "--dev-bind", "/dev/net/tun", "/dev/net/tun", "--proc", "/proc",
                  "--tmpfs", "/tmp", "--tmpfs", "/run", "--ro-bind", str(root), "/tmp/source",
                  "--setenv", "PYTHONDONTWRITEBYTECODE", "1", "--unsetenv", "DBUS_SESSION_BUS_ADDRESS",
                  "--setenv", "Q1_TEST_PARENT_NETNS", os.readlink("/proc/self/ns/net"),
                  sys.executable, "/tmp/source/tools/q1pro-system-test/browser_offline.py", "--isolated"])
    else:
        sys.exit(f"usage: {Path(sys.argv[0]).name}")
