#!/usr/bin/env python3
"""Boot the packed Q1 userspace as a read-only ARM guest on QEMU virt."""
import argparse
import contextlib
import gzip
import hashlib
import http.cookiejar
import json
import os
import re
import secrets
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

HERE = Path(__file__).resolve().parent
CACHE = Path.home() / ".cache/jetkvm-q1pro"
VERSION = "6.12.117-0-virt"
ALPINE = "linux-virt-6.12.117-r0.apk"
APK_SHA256 = "647e1c0ccaf79e270cacfa78ade4cbc1d355e324ca63b0c0e8ab0d2fea913349"
USER_NET = "user,id=net0,restrict=on"
NET_CARD = ["-device", "virtio-net-device,netdev=net0,mac=52:54:00:41:52:61"]


def run(*cmd, **options):
    return subprocess.run(cmd, check=True, **options)


def fetch_kernel(work):
    store = CACHE / "system-test"
    store.mkdir(exist_ok=True, parents=True)
    apk = store / ALPINE
    if not apk.is_file():
        url = "https://dl-cdn.alpinelinux.org/alpine/v3.22/main/armv7/" + ALPINE
        with tempfile.NamedTemporaryFile(dir=store) as temp, urllib.request.urlopen(url, timeout=120) as response:
            shutil.copyfileobj(response, temp)
            temp.flush()
            with open(temp.name, "rb") as downloaded:
                if hashlib.file_digest(downloaded, "sha256").hexdigest() != APK_SHA256:
                    raise RuntimeError("downloaded kernel checksum mismatch")
            try:
                os.link(temp.name, apk)
            except FileExistsError:
                pass
    with apk.open("rb") as source:
        if hashlib.file_digest(source, "sha256").hexdigest() != APK_SHA256:
            raise RuntimeError("cached kernel checksum mismatch")
    kernel = work / "kernel"
    kernel.mkdir()
    run("bsdtar", "-xf", apk, "-C", kernel, "boot/vmlinuz-virt", "lib/modules")
    return kernel


def add_modules(archive, kernel):
    modules = kernel / "lib/modules" / VERSION
    deps = dict(line.split(":", 1) for line in (modules / "modules.dep").read_text().splitlines())
    paths = {Path(key).name.split(".")[0]: key for key in deps}
    ordered = []
    def add(file):
        if file in ordered:
            return
        for dep in reversed(deps[file].split()):
            add(dep)
        ordered.append(file)
    for name in ("virtio_mmio", "virtio_blk", "virtio_net", "af_packet", "configfs", "squashfs", "crc32c_generic", "ext4"):
        add(paths[name])
    for file in ordered:
        (archive / "modules" / Path(file).stem).write_bytes(gzip.decompress((modules / file).read_bytes()))
    return [Path(file).stem for file in ordered]


def prepare_boot(work, kernel, image, unsquash, sdk, hardware_stubs, with_probe=False):
    tag = "virt" if hardware_stubs else "stock-init"
    archive = work / ("initrd-" + tag)
    for d in ("bin", "dev", "modules", "image"):
        (archive / d).mkdir(parents=True)
    busy = archive / "bin/busybox"
    with busy.open("wb") as dst:
        run(unsquash, "-cat", image, "bin/busybox", stdout=dst)
    busy.chmod(0o755)
    names = add_modules(archive, kernel)
    compiler = sdk / "bin/arm-rockchip830-linux-uclibcgnueabihf-gcc"
    run(compiler, "-static", "-Os", "-s", HERE / "enter-root.c", "-o", archive / "enter-root")
    shell = ["#!/bin/busybox sh", "set -eu", "/bin/busybox mount -t devtmpfs devtmpfs /dev"]
    shell += ["/bin/busybox insmod /modules/" + name for name in names]
    shell += ["/enter-root --mount-disks"]
    if hardware_stubs:
        rc = run(unsquash, "-cat", image, "etc/rcS", capture_output=True).stdout.decode()
        ubi = '[ "$(cat /sys/class/ubi/ubi0_1/name)" = rootfs_data ]\nmount -t ubifs ubi0:rootfs_data /mnt/data'
        if rc.count(ubi) != 1:
            raise ValueError("unexpected UBI preparation; review virt substitution")
        (archive / "rcS.virt").write_text(rc.replace(ubi, "mount -t ext4 /dev/q1-test-state /mnt/data"))
        (archive / "rcS.virt").chmod(0o755)
        (archive / "board-modules").touch()
        shell += ["/bin/busybox mount -o bind /rcS.virt /image/etc/rcS",
                  "/bin/busybox mount -o bind /board-modules /image/etc/jetkvm-modules"]
        if with_probe:
            go_env = {**os.environ, "GOOS": "linux", "GOARCH": "arm", "GOARM": "7", "CGO_ENABLED": "0"}
            run("go", "build", "-p=4", "-mod=readonly", "-trimpath", "-ldflags=-s -w",
                "-o", archive / "probe", HERE / "testdata/client.go", cwd=HERE.parents[1], env=go_env)
            shutil.copyfile(work / "test-password", archive / "test-password")
            for directory in ("proc", "sys"):
                (archive / directory).mkdir()
            shell += ["/bin/busybox mount -t proc proc /proc", "/bin/busybox mount -t sysfs sysfs /sys",
                      "/probe >/dev/console 2>&1 &"]
    shell += ["/bin/busybox echo 'Q1 TEST: exec packed /sbin/init'", "exec /enter-root", ""]
    (archive / "init").write_text("\n".join(shell))
    (archive / "init").chmod(0o755)
    output = work / (tag + ".cpio.gz")
    with output.open("wb") as dst:
        run("fakeroot", "--", "sh", "-ec",
            'cd "$1"; mknod dev/console c 5 1; mknod dev/null c 1 3; find . -print0 | cpio --null -o -H newc --owner=0:0 --quiet | gzip -1',
            "build-initrd", str(archive), stdout=dst)
    return output


@contextlib.contextmanager
def vm(work, args, logname):
    path = work / logname
    print("booting", path, flush=True)
    with path.open("wb") as log:
        process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT)
        try:
            yield process, path
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def request(port, path, payload=None, session=None, *, host="127.0.0.1"):
    req = urllib.request.Request(f"http://{host}:{port}" + path,
                                 data=json.dumps(payload).encode() if payload is not None else None,
                                 headers={"Content-Type": "application/json"})
    if session is None:
        session = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with session.open(req, timeout=12) as response:
        return response.read()


def verify_ui(port):
    html = request(port, "/")
    script = re.search(rb'<script[^>]+src="(/static/[^"]+\.js)"', html)
    if b"<title>JetKVM</title>" not in html or not script:
        raise RuntimeError("packed JetKVM HTML or its JS entry point is missing")
    asset = request(port, script[1].decode("ascii"))
    if len(asset) < 256 or asset.lstrip().startswith(b"<!"):
        raise RuntimeError("embedded JS request did not return the UI bundle")


def await_status(proc, port, log, *, host="127.0.0.1"):
    deadline = time.monotonic() + 100
    while time.monotonic() < deadline and proc.poll() is None:
        if b"JetKVM startup incomplete" in log.read_bytes():
            raise RuntimeError(f"guest init failed before HTTP startup, see {log}")
        try:
            return json.loads(request(port, "/device/status", host=host))
        except (OSError, ValueError, urllib.error.HTTPError):
            time.sleep(0.5)
    raise RuntimeError("Q1 /device/status did not become ready; inspect console log")


def prepare_guest(work, original_image, netdev, reserve_dma_budget=False):
    image = work / "image.squashfs"
    shutil.copyfile(original_image, image)
    print("image", hashlib.sha256(image.read_bytes()).hexdigest(), image, flush=True)
    kernel = fetch_kernel(work)
    disk = work / "userdata.ext4"
    with disk.open("wb") as f:
        f.truncate(64 * 1024**2)
    run("mkfs.ext4", "-q", "-F", disk)
    disks = ["-drive", f"if=none,format=raw,readonly=on,id=root,file={image}",
             "-device", "virtio-blk-device,drive=root",
             "-drive", f"if=none,format=raw,id=data,file={disk}",
             "-device", "virtio-blk-device,drive=data"]
    base = ["qemu-system-arm", "-cpu", "cortex-a15", "-m", "256M", "-smp", "1",
            "-nodefaults", "-display", "none", "-monitor", "none", "-serial", "stdio",
            "-no-reboot", "-kernel", str(kernel / "boot/vmlinuz-virt"), *disks,
            "-netdev", netdev, *NET_CARD]
    dtb = work / "virt.dtb"
    run(*base, "-M", f"virt,highmem=off,dumpdtb={dtb}", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    run("fdtput", "-t", "s", dtb, "/", "compatible", "linux,dummy-virt", "oraybox,q1pro", "rockchip,rv1106")
    command_line = "console=ttyAMA0 rdinit=/init panic=1 loglevel=4"
    if reserve_dma_budget:
        command_line += " mem=216M"
    print("QEMU virt: 1 vCPU, 256 MiB RAM,",
          "40 MiB excluded from Linux" if reserve_dma_budget else "full guest RAM visible", flush=True)
    guest = [*base, "-M", "virt,highmem=off", "-dtb", str(dtb),
             "-append", command_line]
    return image, kernel, guest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", type=Path, default=HERE.parents[1] / "bin/q1pro-image/q1pro-rootfs.squashfs")
    parser.add_argument("--sdk", type=Path, default=CACHE / "buildkit-v0.2.8")
    parser.add_argument("--unsquashfs", default=shutil.which("unsquashfs") or str(CACHE / "image-tools/bin/unsquashfs"))
    parser.add_argument("--work", type=Path, help="new directory for test fixtures and serial logs")
    parser.add_argument("--serve", action="store_true", help="serve a fresh guest without the test client or setup")
    parser.add_argument("--port", type=int, help="loopback HTTP port, default 8717 for --serve and auto for tests")
    parser.add_argument("--reserve-dma-budget", action="store_true",
                        help="exclude 40 MiB from Linux-visible memory on a 256 MiB, single-core guest")
    args = parser.parse_args()
    if args.work is None:
        work = Path(tempfile.mkdtemp(prefix="q1-system-test-"))
    else:
        work = args.work.resolve()
        work.mkdir(mode=0o700)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", args.port or (8717 if args.serve else 0)))
        port = sock.getsockname()[1]
    netdev = USER_NET + f",hostfwd=tcp:127.0.0.1:{port}-:80"
    image, kernel, guest = prepare_guest(work, args.image.resolve(strict=True), netdev, args.reserve_dma_budget)
    if args.serve:
        virt = prepare_boot(work, kernel, image, args.unsquashfs, args.sdk, True)
        with vm(work, [*guest, "-initrd", virt], "guest.log") as (proc, log):
            status = await_status(proc, port, log)
            if status != {"isSetup": False, "sku": "oraybox-q1pro"}:
                raise RuntimeError(f"guest did not start with empty userdata: {status}")
            verify_ui(port)
            print(f"Serving the ARM guest on http://127.0.0.1:{port}/", flush=True)
            if proc.wait() != 0:
                raise RuntimeError(f"QEMU stopped; inspect {log}")
        return
    password = secrets.token_urlsafe(24)
    secret = work / "test-password"
    secret.touch(mode=0o600)
    secret.write_text(password)
    original = prepare_boot(work, kernel, image, args.unsquashfs, args.sdk, False)
    with vm(work, [*guest, "-initrd", original], "original.log") as (proc, log):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline and proc.poll() is None:
            serial = log.read_bytes()
            if b"/sys/class/ubi/ubi0_1/name" in serial and b"JetKVM startup incomplete" in serial:
                break
            time.sleep(0.25)
        else:
            raise RuntimeError("packed /sbin/init and original rcS did not reach their documented storage check")
        print("original init refused to start without Q1 storage", flush=True)
    virt = prepare_boot(work, kernel, image, args.unsquashfs, args.sdk, True, with_probe=True)
    for attempt in range(2):
        with vm(work, [*guest, "-initrd", virt], f"userspace-{attempt + 1}.log") as (proc, log):
            status = await_status(proc, port, log)
            if status.get("isSetup") is not bool(attempt):
                raise RuntimeError(f"unexpected setup status: {status}")
            print("guest", attempt + 1, "actual /device/status", status, flush=True)
            if attempt == 0:
                reply = json.loads(request(port, "/device/setup", {"localAuthMode": "password", "password": password}))
                print("actual /device/setup", reply, flush=True)
                if not json.loads(request(port, "/device/status"))["isSetup"]:
                    raise RuntimeError("setup did not persist in running guest")
            if status.get("sku") != "oraybox-q1pro":
                raise RuntimeError(f"unexpected SKU: {status}")
            deadline = time.monotonic() + 125
            while time.monotonic() < deadline and proc.poll() is None:
                serial = log.read_bytes()
                failure = next((line for line in serial.splitlines() if b"Q1 WebRTC failure:" in line), None)
                if failure:
                    raise RuntimeError(failure.decode(errors="replace"))
                if b"Q1 verified developer SSH closed" in serial and b"Q1 WebRTC verified getVideoState" in serial:
                    break
                time.sleep(0.25)
            else:
                raise RuntimeError(f"WebRTC client timed out; inspect {log}")
            memory = [line.decode().strip() for line in serial.splitlines()
                      if line.startswith((b"Q1 guest memory ", b"Q1 process "))]
            if len(memory) != 4 or not any("CPUs=1 " in line for line in memory):
                raise RuntimeError(f"incomplete guest memory report: {memory}")
            for line in memory:
                print(line, flush=True)
            print("WebRTC getVideoState ready=false error=device_unavailable width=0 height=0", flush=True)
            if b"Q1 WebRTC verified fixed_edid and no custom_edid" not in serial:
                raise RuntimeError("Q1 EDID capability was not verified")
            storage = re.search(rb"Q1 storage download-only free_bytes=(\d+)", serial)
            if not storage:
                raise RuntimeError("storage capability or available space was not verified")
            print("WebRTC reports fixed_edid, download-only storage, free bytes", int(storage[1]), flush=True)
            print("Authenticated developer RPC starts Dropbear and stops its SSH listener", flush=True)
            for path, payload in (("/device", None), ("/auth/login-local", {"password": password + "invalid"})):
                try:
                    request(port, path, payload)
                except urllib.error.HTTPError as err:
                    if err.code != 401:
                        raise
                else:
                    raise RuntimeError(f"expected 401: {path}")
            jar = http.cookiejar.CookieJar()
            session = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
            reply = json.loads(request(port, "/auth/login-local", {"password": password}, session))
            device = json.loads(request(port, "/device", session=session))
            if reply.get("message") != "Login successful" or device.get("authMode") != "password":
                raise RuntimeError("authenticated device endpoint rejected the new session")
            print("HTTP 401 for anonymous device and wrong password; login and authenticated /device HTTP 200", flush=True)
            verify_ui(port)
    print("PASS: actual setup survives second userspace boot; hardware substitutions: UBI and board modules", flush=True)


if __name__ == "__main__":
    main()
