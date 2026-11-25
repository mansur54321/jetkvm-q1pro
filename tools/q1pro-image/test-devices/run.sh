#!/bin/sh
# Exercise the actual rcS and device manager from the candidate SquashFS, inside an ARM VM.
set -eu
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
image=$(realpath "${1:?candidate rootfs.squashfs}")
kernel=$(realpath "${2:?QEMU-only Kbuild output directory}")
log=$(realpath -m "${3:?guest serial log}")
work=$(mktemp -d)
pid=
# shellcheck disable=SC2329  # Invoked by the EXIT trap.
cleanup() {
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
    rm -rf "$work"
}
trap 'cleanup' EXIT
fs="$work/fs"
unsquashfs -no-progress -d "$fs" "$image" bin sbin/mdev sbin/insmod sbin/q1-status-led etc/rcS etc/mdev.conf >/dev/null
mkdir -p "$fs/proc" "$fs/sys" "$fs/dev" "$fs/run" "$fs/tmp" "$fs/modules"
for mod in mdev-test/q1_test_nodes drivers/usb/gadget/udc/dummy_hcd drivers/usb/gadget/libcomposite \
           drivers/usb/gadget/function/usb_f_hid sound/drivers/snd-dummy; do
    cp "$kernel/$mod.ko" "$fs/modules/"
done
ln -s bin/busybox "$fs/init"
printf '%s\n' '::sysinit:/etc/rcS' '::once:/q1-checks' > "$fs/etc/inittab"
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o "$fs/q1-checks" "$here/guest.go"
(cd "$fs" && fakeroot sh -ec '
    mknod dev/console c 5 1
    mknod dev/null c 1 3
    find . -print0 | cpio --null -o -H newc --owner=0:0 --quiet
') > "$work/init.cpio"
gzip -1 "$work/init.cpio"
{
    sha256sum "$image" "$fs/bin/busybox" "$kernel/arch/arm/boot/zImage"
    readelf -lW "$fs/bin/busybox" | awk '$1 == "GNU_STACK" && $(NF-1) == "RW" {print; found=1} END {exit !found}'
} > "$log"
qemu-system-aarch64 -machine virt,gic-version=3 -cpu cortex-a53,aarch64=off \
    -m 216M -nodefaults -nic none -display none -monitor none -serial stdio -no-reboot \
    -kernel "$kernel/arch/arm/boot/zImage" -initrd "$work/init.cpio.gz" \
    -append 'console=ttyAMA0 earlycon loglevel=4 rdinit=/init' >> "$log" 2>&1 < /dev/null &
pid=$!
for _ in $(seq 1 45); do
    if grep -q 'Q1 mdev QEMU PASS' "$log"; then cat "$log"; exit 0; fi
    if grep -q 'Kernel panic' "$log" || ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 1
done
cat "$log" >&2
exit 1
