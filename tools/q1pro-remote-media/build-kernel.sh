#!/bin/sh
# Build a separate Q1 BSP for testing removable media; no firmware partition is written.
set -eu
[ "$#" = 3 ] || { echo 'usage: build-kernel.sh BSP_TREE STOCK_CONFIG NEW_BUILD_DIR' >&2; exit 2; }
kernel=$(realpath "$1")
base=$(realpath "$2")
mkdir -p "$3"
out=$(realpath "$3")
[ "$out" != "$kernel" ]
: "${CROSS_COMPILE:?Q1 ARM toolchain prefix required}"
cp "$base" "$out/.config"
"$kernel/scripts/config" --file "$out/.config" \
    --set-str LOCALVERSION '-q1-media-lab' -d LOCALVERSION_AUTO \
    -e ARCH_VIRT -e SERIAL_AMBA_PL011 -e SERIAL_AMBA_PL011_CONSOLE \
    -e BLK_DEV_INITRD -e RD_GZIP -d INITRD_ASYNC -e DEVTMPFS \
    -e BLK_DEV_NBD -e SCSI -e BLK_DEV_SD -e BLK_DEV_SR -e CHR_DEV_SG \
    -e USB -e USB_EHCI_HCD -e USB_EHCI_HCD_PLATFORM -e USB_STORAGE -e USB_GADGET -e USB_LIBCOMPOSITE -e USB_CONFIGFS \
    -e USB_CONFIGFS_F_HID -e USB_CONFIGFS_MASS_STORAGE -e USB_CONFIGFS_F_FS \
    -e USB_DUMMY_HCD -e VIRTIO_MMIO -e VIRTIO_NET
build() {
    make -s -C "$kernel" O="$out" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" \
        CC="${CROSS_COMPILE}gcc" LOCALVERSION= "$@"
}
build olddefconfig
for option in BLK_DEV_NBD USB_STORAGE BLK_DEV_SR CHR_DEV_SG USB_DUMMY_HCD USB_CONFIGFS_F_FS USB_F_FS USB_CONFIGFS_F_HID USB_CONFIGFS_MASS_STORAGE VIDEO_LT7911D; do
    grep -qx "CONFIG_$option=y" "$out/.config" || { echo "missing CONFIG_$option" >&2; exit 1; }
done
build -j"${JOBS:-6}" zImage
build -j"${JOBS:-6}" modules
