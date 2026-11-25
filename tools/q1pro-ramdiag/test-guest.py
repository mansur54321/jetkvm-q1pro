#!/usr/bin/env python3
"""Boot the RAM diagnostic on QEMU's ARM virt board, without a disk or a NIC."""
import argparse
import os
import selectors
import subprocess
import time
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('kernel', type=Path)
    parser.add_argument('ramdisk', type=Path)
    parser.add_argument('log', type=Path)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--media', type=Path, metavar='QEMU_DTB', help='use the NBD integration fixture')
    mode.add_argument('--generic-kernel', action='store_true',
                        help='only verify /init, allow a kernel without built-in DWC3 and NBD')
    args = parser.parse_args()
    if args.generic_kernel:
        machine = ['qemu-system-arm', '-machine', 'virt', '-cpu', 'cortex-a7']
    else:
        # The Rockchip FIQ handler uses the GICv2 aliased acknowledge register.
        machine = ['qemu-system-aarch64', '-machine', 'virt,gic-version=3',
                   '-cpu', 'cortex-a53,aarch64=off']
    command = [*machine, '-m', '256M' if args.media else '128M', '-nodefaults', '-nic', 'none', '-display', 'none',
               '-monitor', 'none', '-serial', 'stdio', '-no-reboot',
               '-kernel', args.kernel, '-initrd', args.ramdisk,
               '-append', 'console=ttyAMA0 earlycon loglevel=5 rdinit=/init']
    if args.media:
        command.extend(['-dtb', args.media])
    marker = 'Q1 QEMU media PASS' if args.media else 'q1-ramdiag: idle'
    output = bytearray()
    with subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT) as process:
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                deadline = time.monotonic() + 45
                while time.monotonic() < deadline:
                    if marker.encode() in output or b'Kernel panic' in output:
                        break
                    if not selector.select(deadline - time.monotonic()):
                        break
                    chunk = os.read(process.stdout.fileno(), 65536)
                    if not chunk:
                        break
                    output.extend(chunk)
        finally:
            process.terminate()
    log = output.decode(errors='replace').replace('\r', '').rstrip() + '\n'
    args.log.write_text(log)
    if args.media:
        required = ['loaded=libcomposite', 'loaded=usb_f_hid', 'loaded=usb_f_mass_storage',
                    '--- PASS: TestRemoteNBDReadsLastSector', marker]
    else:
        required = ['Q1 Pro RAM diagnostic', 'compatible=linux,dummy-virt', 'net=lo operstate=down', marker]
        if not args.generic_kernel:
            required += ['kernel=5.10.160-q1-ramdiag', 'dwc3.driver=registered', 'nbd0.sectors=0']
    for value in required:
        if value not in log:
            raise SystemExit(f'missing {value!r} in {args.log}')
    print(log[log.index(required[0]):].strip())


if __name__ == '__main__':
    main()
