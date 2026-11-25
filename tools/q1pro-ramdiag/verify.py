#!/usr/bin/env python3
"""Check the diagnostic ARM binaries, initramfs inventory and module versioned imports."""
import gzip
import hashlib
import struct
import subprocess
import sys
from pathlib import Path


def elf(data, kind):
    if data[:7] != b'\x7fELF\x01\x01\x01' or struct.unpack_from('<HH', data, 16) != (kind, 40):
        raise ValueError('expected little-endian ARM ELF32')


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def check(kernel, build, package):
    release = (build / 'include/config/kernel.release').read_text().strip()
    if not release.endswith('-q1-ramdiag'):
        raise ValueError(release)
    elf((build / 'vmlinux').read_bytes(), 2)
    image = (package / 'zImage').read_bytes()
    if image[36:40] != bytes.fromhex('18286f01'):
        raise ValueError('missing ARM zImage magic')
    extracted_config = subprocess.check_output([kernel / 'scripts/extract-ikconfig', package / 'zImage'])
    if extracted_config != (package / 'kernel.config').read_bytes():
        raise ValueError('kernel.config differs from the configuration embedded in zImage')

    archive = gzip.decompress((package / 'initramfs.cpio.gz').read_bytes())
    inventory = subprocess.check_output(['cpio', '-it', '--quiet'], input=archive).decode().splitlines()
    module_dir = f'lib/modules/{release}'
    expected = {'dev', 'dev/console', 'proc', 'sys', 'init', 'lib', 'lib/modules', module_dir}
    modules = {'libcomposite', 'usb_f_hid', 'usb_f_mass_storage'}
    expected.update(f'{module_dir}/{name}.ko' for name in modules)
    if sorted(expected) != sorted(inventory):
        raise ValueError(f'unexpected cpio inventory: {inventory}')
    print(f'ARM kernel {release}, embedded config and {len(inventory)} archive entries verified')
    init = subprocess.check_output(['cpio', '-i', '--quiet', '--to-stdout', 'init'], input=archive)
    elf(init, 2)
    if init != (build / 'ramdiag-init').read_bytes():
        raise ValueError('packaged /init differs')
    segments = command('readelf', '-lW', build / 'ramdiag-init')
    if 'INTERP' in segments:
        raise ValueError('init requires a dynamic loader')
    stack = [line.split()[-2] for line in segments.splitlines() if 'GNU_STACK' in line]
    if stack != ['RW']:
        raise ValueError(f'init stack permissions: {stack}')
    print('init: static ARM ELF, GNU_STACK RW')

    exports = {}
    for line in (build / 'Module.symvers').read_text().splitlines():
        crc, symbol, provider, *_ = line.split()
        if provider == 'vmlinux' or Path(provider).name in modules:
            exports[symbol] = int(crc, 16)
    for name in sorted(modules):
        data = subprocess.check_output(['cpio', '-i', '--quiet', '--to-stdout', f'{module_dir}/{name}.ko'],
                                       input=archive)
        path = build / 'ramdiag-modules' / f'{name}.ko'
        elf(data, 1)
        if data != path.read_bytes():
            raise ValueError(f'{name}: packaged module differs')
        vermagic = command('modinfo', '-F', 'vermagic', path)
        if vermagic.split()[0] != release:
            raise ValueError(f'{name}: {vermagic}')
        deps = command('modinfo', '-F', 'depends', path)
        if deps and not set(deps.split(',')) <= modules:
            raise ValueError(f'{name}: unpackaged dependencies {deps}')
        imports = {}
        for line in command('modprobe', '--show-modversions', path).splitlines():
            crc, symbol = line.split()
            imports[symbol] = int(crc, 16)
        undefined = {fields[0] for line in command('nm', '-u', '--format=posix', path).splitlines()
                     if len(fields := line.split()) >= 2 and fields[1] == 'U'}
        if 'module_layout' not in imports or undefined - imports.keys():
            raise ValueError(f'{name}: incomplete symbol versions')
        if any(exports.get(symbol) != crc for symbol, crc in imports.items()):
            raise ValueError(f'{name}: incompatible module exports')
        print(f'{name}: {len(imports)} import CRCs, {vermagic}')
    print(f"vmlinux sha256 {hashlib.file_digest((build / 'vmlinux').open('rb'), 'sha256').hexdigest()}")


if __name__ == '__main__':
    check(*(Path(arg) for arg in sys.argv[1:]))
