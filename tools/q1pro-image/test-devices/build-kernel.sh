#!/bin/sh
# QEMU-only BSP configuration for testing the stock kernel's userspace interfaces.
set -eu
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
kernel=$(realpath "${1:?RV1106 BSP kernel directory}")
stock=$(realpath "${2:?Q1 stock config}")
mkdir -p "${3:?separate QEMU Kbuild output directory}"
build=$(realpath "$3")
[ "$kernel" != "$build" ]
: "${CROSS_COMPILE:?ARM Linux cross compiler prefix}"
cp "$stock" "$build/.config"
"$kernel/scripts/config" --file "$build/.config" \
    --set-str LOCALVERSION '-q1-systest' -d LOCALVERSION_AUTO \
    -e ARCH_VIRT -e SERIAL_AMBA_PL011 -e SERIAL_AMBA_PL011_CONSOLE \
    -e BLK_DEV_INITRD -e RD_GZIP -d INITRD_ASYNC \
    -d DEVTMPFS -e UEVENT_HELPER --set-str UEVENT_HELPER_PATH '' \
    -e TMPFS -e UNIX98_PTYS -e BLK_DEV_LOOP -e MTD_BLOCK2MTD -e MTD_UBI \
    -e UBIFS_FS -e SQUASHFS -e SQUASHFS_XZ \
    -e VIRTIO_MENU -e VIRTIO_MMIO -e VIRTIO_BLK -e VIRTIO_NET \
    -e USB_GADGET -e USB_XHCI_HCD -e USB_XHCI_PLATFORM -e USB_DWC3 -e USB_DWC3_OF_SIMPLE \
    -m USB_DUMMY_HCD -m USB_CONFIGFS -e USB_CONFIGFS_F_HID -e USB_HID \
    -m SND_DUMMY --set-val PANIC_TIMEOUT 0
kmake() {
    make -s -C "$kernel" O="$build" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" \
        CC="${CROSS_COMPILE}gcc" LOCALVERSION= -j"${JOBS:-4}" "$@"
}
kmake olddefconfig
for name in VIRTIO_MMIO VIRTIO_BLK VIRTIO_NET MTD_BLOCK2MTD UBIFS_FS SQUASHFS_XZ UEVENT_HELPER; do
    grep -qx "CONFIG_$name=y" "$build/.config"
done
for name in USB_DUMMY_HCD SND_DUMMY; do grep -qx "CONFIG_$name=m" "$build/.config"; done
grep -Fxq '# CONFIG_DEVTMPFS is not set' "$build/.config"
grep -Fxq 'CONFIG_UEVENT_HELPER_PATH=""' "$build/.config"
kmake zImage
kmake drivers/usb/gadget/udc/dummy_hcd.ko drivers/usb/gadget/libcomposite.ko \
    drivers/usb/gadget/function/usb_f_hid.ko sound/drivers/snd-dummy.ko
mkdir -p "$build/mdev-test"
cp "$here/module/Makefile" "$here/module/q1_test_nodes.c" "$build/mdev-test/"
kmake M="$build/mdev-test" modules
