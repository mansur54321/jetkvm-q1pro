#!/bin/sh
set -eu
if [ "$#" -ne 4 ]; then
    echo "usage: CROSS_COMPILE=... $0 KERNEL_DIR FACTORY_CONFIG BUILD_DIR OUTPUT_DIR" >&2
    exit 2
fi
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
kernel=$(realpath "$1")
mkdir -p "$3" "$4"
build=$(realpath "$3")
out=$(realpath "$4")
: "${CROSS_COMPILE:?ARM Linux cross toolchain prefix}"
export KBUILD_BUILD_TIMESTAMP='2026-05-19 07:02:33 UTC'
export KBUILD_BUILD_USER=jetkvm KBUILD_BUILD_HOST=q1-ramdiag KBUILD_BUILD_VERSION=1
"$here/configure.sh" "$kernel" "$2" "$build"
kmake() {
    make -s -C "$kernel" O="$build" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" \
        CC="${CROSS_COMPILE}gcc" LOCALVERSION= -j"${JOBS:-4}" "$@"
}
kmake zImage
kmake drivers/usb/gadget/libcomposite.ko drivers/usb/gadget/function/usb_f_hid.ko \
    drivers/usb/gadget/function/usb_f_mass_storage.ko
"${CROSS_COMPILE}gcc" -std=gnu11 -Os -Wall -Wextra -Werror -static -Wl,-z,noexecstack -s "$here/init.c" -o "$build/ramdiag-init"
touch -d @0 "$build/ramdiag-init"
release=$(cat "$build/include/config/kernel.release")
mkdir -p "$build/ramdiag-modules"
list="$build/ramdiag-cpio.list"
cat > "$list" <<CPIO
dir /dev 0755 0 0
nod /dev/console 0600 0 0 c 5 1
dir /proc 0555 0 0
dir /sys 0555 0 0
file /init $build/ramdiag-init 0755 0 0
dir /lib 0755 0 0
dir /lib/modules 0755 0 0
dir /lib/modules/$release 0755 0 0
CPIO
for path in "$build"/drivers/usb/gadget/libcomposite.ko \
            "$build"/drivers/usb/gadget/function/usb_f_hid.ko \
            "$build"/drivers/usb/gadget/function/usb_f_mass_storage.ko; do
    name=$(basename "$path")
    module="$build/ramdiag-modules/$name"
    "${CROSS_COMPILE}strip" --strip-debug -o "$module" "$path"
    touch -d @0 "$module"
    echo "file /lib/modules/$release/$name $module 0644 0 0" >> "$list"
done
tmp=$(mktemp -d "$out/.build-XXXXXX")
trap 'rm -rf "$tmp"' EXIT
"$build/usr/gen_init_cpio" -t 0 "$list" > "$tmp/initramfs.cpio"
gzip -n "$tmp/initramfs.cpio"
cp "$build/arch/arm/boot/zImage" "$tmp/zImage"
cp "$build/.config" "$tmp/kernel.config"
python3 "$here/verify.py" "$kernel" "$build" "$tmp" > "$tmp/verification.txt"
cat "$tmp/verification.txt"
(cd "$tmp" && sha256sum zImage initramfs.cpio.gz kernel.config verification.txt > SHA256SUMS)
for file in zImage initramfs.cpio.gz kernel.config verification.txt SHA256SUMS; do
    mv "$tmp/$file" "$out/$file"
done
