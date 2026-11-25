#!/usr/bin/env python3
"""Test an ARM Improv peripheral against a real virtual Bluetooth central and Wi-Fi AP."""
import argparse
import gzip
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
CACHE = Path.home() / '.cache/jetkvm-q1pro'
INIT = '''#!/bin/busybox sh
set -eu
b=/bin/busybox
$b mount -t devtmpfs devtmpfs /dev
exec </dev/console >/dev/console 2>&1
$b mount -t sysfs sysfs /sys
$b mount -t proc proc /proc
$b ip link set lo up
for f in /mods/*.ko; do $b insmod "$f"; done
export PATH=/bin:/usr/bin:/usr/sbin
export DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/q1-ble/bus
for rf in /sys/class/rfkill/rfkill*; do [ ! -e "$rf/soft" ] || echo 0 >"$rf/soft"; done
$b ip addr add 192.0.2.1/24 dev wlan1
$b ip addr add 192.0.2.2/24 dev wlan0
/usr/sbin/hostapd /etc/hostapd.conf >/tmp/ap.log 2>&1 &
/usr/sbin/wpa_supplicant -Dnl80211 -i wlan0 -c /run/wifi.conf >/tmp/wpa.log 2>&1 &
/usr/bin/dbus-daemon --config-file=/etc/q1-ble-bus.conf --nofork --nopidfile >/tmp/dbus.log 2>&1 &
/btvirt -l2 >/tmp/btvirt.log 2>&1 &
$b sleep 1
/usr/bin/bluetoothd -n -f /etc/q1-ble-bluez.conf -P battery >/tmp/bluez.log 2>&1 &
$b sleep 2
if /check -test.v -test.run '^TestVirtualBluetoothToWPA2$' -test.timeout 110s; then
    $b echo Q1_BLE_WIFI_PASS
else
    $b echo Q1_BLE_WIFI_FAIL
    $b dmesg | $b grep -iE "smp|ecdh|crypto.*fail|error.*key" || true
    for f in /tmp/ap.log /tmp/bluez.log /tmp/btvirt.log /tmp/dbus.log /tmp/wpa.log; do $b echo "$f"; $b tail -n 12 "$f" || true; done
fi
$b sync
$b poweroff -f
'''

def cmd(*args, **kw):
    return subprocess.run(args, check=True, **kw)

def test(args, work):
    root=work/'root'; root.mkdir(exist_ok=False)
    scratch=work/'stock'
    unsq=shutil.which('unsquashfs') or CACHE/'image-tools/bin/unsquashfs'
    cmd(unsq,'-quiet','-no-progress','-force','-d',root,args.image,'bin','etc/passwd','etc/group','lib/*.so*','usr/lib/*.so*','usr/sbin/wpa_supplicant','usr/bin/bluetoothd','usr/bin/dbus-daemon','etc/q1-ble-bus.conf','etc/q1-ble-bluez.conf',stdout=subprocess.DEVNULL)
    files=['usr/bin/bluetoothd','usr/bin/dbus-daemon','usr/sbin/hostapd',
           'usr/lib/libgthread-2.0.so*','usr/lib/libglib-2.0.so*','usr/lib/libdbus-1.so*','usr/lib/libexpat.so*',
           'lib/libubox.so*','lib/libubus.so*','usr/lib/libubox.so*','usr/lib/libubus.so*','usr/lib/libnl-tiny.so*']
    cmd(unsq,'-quiet','-no-progress','-d',scratch,args.stock,*files,stdout=subprocess.DEVNULL)
    # Prefer the selected release's libraries and daemons. Stock supplies the
    # test AP as well as the Bluetooth runtime when using an older rootfs.
    for source in scratch.rglob('*'):
        target=root/source.relative_to(scratch)
        if target.exists() or target.is_symlink():
            continue
        if source.is_symlink():
            target.symlink_to(os.readlink(source))
        elif source.is_dir():
            target.mkdir(parents=True,exist_ok=True)
        else:
            shutil.copy2(source,target)
    for d in ('etc','dev','sys','proc','tmp','mods','var/lib/bluetooth','var/lib/dbus','run/q1-ble/bonds','run/wifictrl'):
        (root/d).mkdir(parents=True,exist_ok=True)
    (root/'var/lib/dbus/machine-id').write_text('9241dca170c9441cbc13888867706cec\n')
    for source,target in [('bus.conf','q1-ble-bus.conf'),('bluez.conf','q1-ble-bluez.conf')]:
        installed=root/'etc'/target
        if installed.exists():
            if installed.read_bytes() != (HERE/source).read_bytes():
                raise ValueError(f'release Bluetooth configuration differs: {target}')
        else:
            shutil.copyfile(HERE/source,installed)
    (root/'run/wifi.conf').write_text('ctrl_interface=/run/wifictrl\nupdate_config=1\nnetwork={\n'
        '  ssid="q1-virtual-network"\n  psk="only-for-this-radio-fixture"\n}\n')
    (root/'etc/hostapd.conf').write_text(
        'interface=wlan1\ndriver=nl80211\nssid=q1-virtual-network\nhw_mode=g\nchannel=1\n'
        'wpa=2\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\nwpa_passphrase=only-for-this-radio-fixture\n')
    release=next((args.kernel/'lib/modules').iterdir()).name
    modules=[]
    for name in ('aes_generic','ecb','cmac','ccm','ctr','hmac','sha256_generic','virtio_rng','drbg','algif_skcipher','algif_hash','af_packet','8021q','ecdh_generic','hci_vhci','mac80211_hwsim'):
        for entry in subprocess.check_output(['modprobe','-d',args.kernel,'-S',release,'--show-depends',name],text=True).splitlines():
            if entry.startswith('insmod '):
                path=Path(entry.split()[1])
                if path not in modules:modules.append(path)
    for index,module in enumerate(modules):
        dest=root/'mods'/f'{index:02}.ko'
        dest.write_bytes(gzip.decompress(module.read_bytes()) if module.suffix=='.gz' else module.read_bytes())
    shutil.copy2(args.btvirt,root/'btvirt')
    cmd('go','test','-c','-o',root/'check','./tools/q1pro-ble',cwd=REPO,env={**os.environ,'GOARCH':'arm','GOOS':'linux','GOARM':'7','CGO_ENABLED':'0'})
    (root/'init').write_text(INIT);(root/'init').chmod(0o755)
    ram=work/'guest.cpio.gz'
    with ram.open('wb') as dst:
        cmd('fakeroot','sh','-ec','mknod dev/console c 5 1; chmod 600 dev/console; find . -print0 | cpio --null --quiet -o -H newc | gzip -1',cwd=root,stdout=dst)
    kernel=next((args.kernel/'boot').glob('vmlinuz-*'))
    serial=work/'guest.log'
    with serial.open('w') as f:
        cmd('qemu-system-arm','-M','virt','-cpu','cortex-a15','-m','384M','-smp','2','-nodefaults','-nic','none','-device','virtio-rng-device','-display','none','-monitor','none','-serial','stdio','-no-reboot','-kernel',kernel,'-initrd',ram,'-append','console=ttyAMA0 rdinit=/init q1-ble-guest=1 panic=-1 loglevel=3',stdout=f,stderr=subprocess.STDOUT,timeout=135)
    lines=serial.read_text().replace('\r','').splitlines();print('\n'.join(x for x in lines if 'virtual_arm_test.go' in x or 'Q1_BLE' in x or 'PASS' in x))
    if 'Q1_BLE_WIFI_PASS' not in lines:raise RuntimeError(f'ARM provisioning failed; see {serial}')

if __name__=='__main__':
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--btvirt',required=True,type=Path,help='BlueZ 5.66 ARM emulator executable')
    ap.add_argument('--image',type=Path,default=REPO/'bin/q1pro-image/q1pro-rootfs.squashfs')
    ap.add_argument('--stock',type=Path,default=CACHE/'usb-kernel/stock-rootfs.squashfs')
    ap.add_argument('--kernel',type=Path,default=CACHE/'system-test/kernel-lts')
    ap.add_argument('--work',type=Path)
    a=ap.parse_args()
    if a.work:
        a.work.mkdir(parents=True,exist_ok=True);test(a,a.work)
    else:
        with tempfile.TemporaryDirectory(prefix='q1-ble-test-') as tmp:test(a,Path(tmp))
