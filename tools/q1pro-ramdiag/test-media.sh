#!/bin/sh
# Run the application's block-device integration test on the diagnostic kernel in QEMU.
set -eu
diag=$(realpath "${1:?diagnostic artifact directory}")
rootfs=$(realpath "${2:?candidate rootfs.squashfs}")
tests=$(realpath "${3:?ARM Go test executable of the root package}")
case $(readelf -h "$tests" | sed -n 's/^ *Machine: *//p') in
    ARM) ;;
    *) echo "expected ARM application tests" >&2; exit 1 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
root="$work/root"
unsquashfs -no-progress -d "$root" "$rootfs" bin/busybox 'lib/*.so*' 'usr/lib/*.so*' > /dev/null
gzip -dc "$diag/initramfs.cpio.gz" > "$work/diag.cpio"
(cd "$root" && cpio -id --quiet 'lib/modules/*' < "$work/diag.cpio")
cp "$tests" "$root/check"
mkdir -p "$root/dev" "$root/proc" "$root/sys" "$root/tmp"
cat > "$root/init" <<'GUEST'
#!/bin/busybox sh
set -eu
b=/bin/busybox
$b mount -t sysfs -o ro sysfs /sys
$b mount -t proc -o ro proc /proc
model=$($b tr -d '\000' < /sys/firmware/devicetree/base/model)
[ "$model" = linux,dummy-virt ] && [ "$($b uname -r)" = 5.10.160-q1-ramdiag ] || exit 1
for name in libcomposite usb_f_hid usb_f_mass_storage; do
    $b insmod "/lib/modules/$($b uname -r)/$name.ko"
    [ -d "/sys/module/$name" ]
    $b echo "loaded=$name"
done
[ "$($b cat /sys/class/block/nbd0/dev)" = "43:0" ]
$b ip link set lo up
JETKVM_TEST_DISPOSABLE_NBD=1 /check -test.v -test.timeout=30s -test.run='^TestRemoteNBDReadsLastSector$'
$b echo "Q1 QEMU media PASS"
exec $b sleep 60
GUEST
chmod +x "$root/init"
(cd "$root" && fakeroot sh -ec '
    mknod dev/console c 5 1
    mknod dev/nbd0 b 43 0
    mknod dev/null c 1 3
    mknod dev/urandom c 1 9
    find . -print0 | cpio --null --quiet -o -H newc
') > "$work/test.cpio"
gzip -n "$work/test.cpio"
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
qemu-system-aarch64 -machine "virt,gic-version=3,dumpdtb=$work/virt.dtb" \
    -cpu cortex-a53,aarch64=off -m 256M -nodefaults -nic none -display none -monitor none
fdtput -t s "$work/virt.dtb" / compatible linux,dummy-virt oraybox,q1pro rockchip,rv1106
python3 "$here/test-guest.py" --media "$work/virt.dtb" "$diag/zImage" "$work/test.cpio.gz" "$diag/qemu-media.log"
