#!/usr/bin/env python3
"""Test the Q1 web UI, official userspace daemon, peer login and WebRTC in a private network."""

import argparse
import contextlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
sys.path.insert(0, str(REPO / "tools/q1pro-system-test"))
import run as guest  # noqa: E402

VERSION = "1.104.7"
CONTROL = "http://192.0.2.1:3330"
GUEST = "http://192.0.2.50"


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def build_fixture(work):
    ctrl = work / "control-build"
    ctrl.mkdir()
    shutil.copyfile(HERE / "testdata/control.go", ctrl / "main.go")
    run("go", "mod", "init", "q1-testcontrol", cwd=ctrl)
    run(
        "go",
        "get",
        f"tailscale.com/tstest/integration/testcontrol@v{VERSION}",
        f"tailscale.com/derp/derpserver@v{VERSION}",
        cwd=ctrl,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    run("go", "build", "-o", work / "control", ".", cwd=ctrl)
    run(
        "go",
        "build",
        "-trimpath",
        "-ldflags=-s -w",
        "-o",
        work / "media",
        HERE / "testdata/media.go",
        cwd=REPO,
        env={
            **os.environ,
            "CGO_ENABLED": "0",
            "GOOS": "linux",
            "GOARCH": "arm",
            "GOARM": "7",
        },
    )
    run(
        "ffmpeg",
        "-v",
        "error",
        "-f",
        "lavfi",
        "-i",
        "testsrc2=size=64x48:rate=6",
        "-frames:v",
        "1",
        "-c:v",
        "libvpx",
        "-g",
        "1",
        "-f",
        "ivf",
        work / "sample.ivf",
    )


def build_initrd(work, image, kernel):
    unsquash = shutil.which("unsquashfs") or guest.CACHE / "image-tools/bin/unsquashfs"
    guest.prepare_boot(
        work, kernel, image, unsquash, guest.CACHE / "buildkit-v0.2.8", True
    )
    stage = work / "initrd-virt"
    for n in ("media", "sample.ivf"):
        shutil.copy2(work / n, stage / n)
    init = stage / "init"
    text = init.read_text()
    if text.count("exec /enter-root") != 1:
        raise ValueError("unexpected test bootstrap")
    init.write_text(
        text.replace(
            "exec /enter-root",
            "/media &\nfor n in 1 2 3 4 5; do [ -s /media-root-ready ] && break; /bin/busybox sleep 1; done\n[ -s /media-root-ready ]\nexec /enter-root",
        )
    )
    archive = work / "media-test.cpio.gz"
    with archive.open("wb") as output:
        run(
            "fakeroot",
            "--",
            "sh",
            "-ec",
            'cd "$1"; mknod dev/console c 5 1; mknod dev/null c 1 3; find . -print0 | cpio --null --quiet -o -H newc --owner=0:0 | gzip -1',
            "q1-tailnet-archive",
            stage,
            stdout=output,
        )
    return archive


def authorize_client(start):
    cli = ["tailscale", "--socket=/tmp/peer.sock"]
    start(
        [
            "tailscaled",
            "--tun=q1tspeer",
            "--socket=/tmp/peer.sock",
            "--statedir=/tmp/peer",
            "--state=/tmp/peer/state",
            "--no-logs-no-support",
            "--port=0",
        ],
        "client-daemon.log",
    )
    for _ in range(100):
        if Path("/tmp/peer.sock").exists():
            break
        time.sleep(0.1)
    start(
        [
            *cli,
            "up",
            "--login-server=" + CONTROL,
            "--accept-dns=false",
            "--netfilter-mode=off",
            "--timeout=60s",
        ],
        "private-client-login.log",
    )
    authorized = False
    for _ in range(90):
        reply = subprocess.run([*cli, "status", "--json"], capture_output=True).stdout
        if reply:
            status = json.loads(reply)
            if status.get("AuthURL") and not authorized:
                url = status["AuthURL"]
                if not url.startswith(CONTROL + "/auth/"):
                    raise RuntimeError("unexpected control server")
                with urllib.request.urlopen(url, timeout=3):
                    pass
                authorized = True
            if status.get("BackendState") == "Running":
                return
        time.sleep(0.2)
    raise RuntimeError("client did not join private tailnet")


def test(work):
    if os.readlink("/proc/self/ns/net") == os.environ["Q1_TEST_PARENT_NETNS"]:
        raise RuntimeError("expected private network namespace")
    for args in [
        ("link", "set", "lo", "up"),
        ("tuntap", "add", "dev", "q1vm", "mode", "tap"),
        ("addr", "add", "192.0.2.1/24", "dev", "q1vm"),
        ("link", "set", "q1vm", "up"),
    ]:
        run("ip", *args)
    image, kernel, command = guest.prepare_guest(
        work,
        Path("/tmp/images/q1.squashfs"),
        "tap,id=net0,ifname=q1vm,script=no,downscript=no",
        reserve_dma_budget=True,
    )
    initrd = build_initrd(work, image, kernel)
    password = secrets.token_urlsafe(18)
    with contextlib.ExitStack() as processes:

        def start(args, name):
            return processes.enter_context(guest.vm(work, args, name))

        start([work / "control"], "control.log")
        start(
            [
                "dnsmasq",
                "--no-daemon",
                "--conf-file=/dev/null",
                "--user=root",
                "--group=root",
                "--port=0",
                "--interface=q1vm",
                "--bind-interfaces",
                "--dhcp-range=192.0.2.50,192.0.2.50,255.255.255.0,1h",
                "--dhcp-option=3",
                "--dhcp-option=6",
                f"--dhcp-leasefile={work}/leases",
                f"--pid-file={work}/dhcp.pid",
            ],
            "dhcp.log",
        )
        proc, serial = start([*command, "-initrd", initrd], "guest.log")
        if guest.await_status(proc, 80, serial, host="192.0.2.50")["isSetup"]:
            raise RuntimeError("expected fresh guest")
        authorize_client(start)
        modules = [str(REPO / "ui/node_modules"), os.environ.get("NODE_PATH", "")]
        env = {**os.environ, "HOME": "/tmp", "NODE_PATH": os.pathsep.join(modules)}

        def browser(mode, origin):
            run(
                "node",
                HERE / "testdata/browser.cjs",
                mode,
                origin,
                password,
                "/tmp/images/upstream.tgz",
                work / "q1.ip",
                env=env,
                timeout=150,
            )

        browser("lan", GUEST)
        node = (work / "q1.ip").read_text()
        status = guest.await_status(proc, 80, serial, host=node)
        if not status["isSetup"]:
            raise RuntimeError("expected the same configured Q1 across the tailnet")
        browser("tail", "http://" + node)
        browser("media", "http://" + node + ":8078")
        browser("disable", GUEST)


def execute(work, args):
    build_fixture(work)
    cmd = [
        "bwrap",
        "--unshare-user",
        "--uid",
        "0",
        "--gid",
        "0",
        "--unshare-net",
        "--unshare-pid",
        "--unshare-ipc",
        "--unshare-uts",
        "--die-with-parent",
        "--new-session",
        "--cap-add",
        "CAP_NET_ADMIN",
        "--cap-add",
        "CAP_NET_RAW",
        "--cap-add",
        "CAP_NET_BIND_SERVICE",
        "--ro-bind",
        "/",
        "/",
        "--dev",
        "/dev",
        "--dev-bind",
        "/dev/net/tun",
        "/dev/net/tun",
        "--proc",
        "/proc",
        "--tmpfs",
        "/tmp",
        "--tmpfs",
        "/run",
        "--ro-bind",
        REPO,
        "/tmp/source",
        "--dir",
        "/tmp/images",
        "--ro-bind",
        args.image.resolve(),
        "/tmp/images/q1.squashfs",
        "--ro-bind",
        args.archive.resolve(),
        "/tmp/images/upstream.tgz",
        "--bind",
        work,
        "/tmp/results",
        "--setenv",
        "PYTHONDONTWRITEBYTECODE",
        "1",
        "--setenv",
        "Q1_TEST_PARENT_NETNS",
        os.readlink("/proc/self/ns/net"),
    ]
    for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "DBUS_SESSION_BUS_ADDRESS"):
        cmd += ["--unsetenv", name]
    run(
        *cmd,
        sys.executable,
        "-u",
        "/tmp/source/tools/q1pro-tailscale/test.py",
        "--isolated",
    )


if __name__ == "__main__":
    if sys.argv[1:] == ["--isolated"]:
        test(Path("/tmp/results"))
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("image", type=Path)
        parser.add_argument("archive", type=Path)
        parser.add_argument(
            "--work",
            type=Path,
            help="new private directory to retain guest images and diagnostic logs",
        )
        args = parser.parse_args()
        if args.work:
            args.work.mkdir(mode=0o700)
            execute(args.work.resolve(), args)
        else:
            with tempfile.TemporaryDirectory(prefix="q1-tailnet-") as directory:
                execute(Path(directory), args)
