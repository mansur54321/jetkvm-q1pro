#!/bin/sh
# Boot a QEMU machine with an emulated host and gadget on one USB bus.
set -eu
release=${1:?installed x86_64 Linux release with vmlinuz and dummy_hcd}
kernel=/usr/lib/modules/$release/vmlinuz
test -r "$kernel"
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/root/modules" "$work/root/tmp"
cd "$root"
CGO_ENABLED=0 go test -c -o "$work/root/check" ./internal/usbgadget
CGO_ENABLED=0 go build -o "$work/root/init" ./tools/q1pro-usb/guest
for name in dummy_hcd libcomposite usb_f_mass_storage usb_f_hid usbhid hid_generic usb_storage sd_mod sr_mod; do
    modprobe -S "$release" --show-depends "$name"
done >"$work/deps"
awk '$1=="insmod" && !seen[$2]++ {print $2}' "$work/deps" | while read -r module; do
    dst="/modules/$(basename "$module" .zst)"
    case "$module" in
        *.ko.zst) zstd -dc "$module" >"$work/root$dst";;
        *.ko) cp "$module" "$work/root$dst";;
        *) echo "unsupported module format: $module" >&2; exit 1;;
    esac
    echo "$dst" >>"$work/root/modules/order"
done
(cd "$work/root" && find . -print0 | cpio --null -o -H newc --quiet | gzip -1 >"$work/initrd.gz")
timeout 70 qemu-system-x86_64 -machine accel=kvm:tcg -m 400M -smp 2 -nic none -nodefaults -nographic \
    -monitor none -serial stdio -no-reboot -kernel "$kernel" -initrd "$work/initrd.gz" \
    -append "console=ttyS0 loglevel=3 panic=-1 jetkvm.test=dummy_hcd" >"$work/log" 2>&1 || true
cat "$work/log"
grep -q '^Q1 DUMMY HCD PASS' "$work/log"
