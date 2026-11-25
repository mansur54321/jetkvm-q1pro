#!/bin/sh
set -eu
if [ "$#" -ne 3 ]; then
    echo "usage: $0 KERNEL_DIR FACTORY_CONFIG BUILD_DIR" >&2
    exit 2
fi
kernel=$(realpath "$1")
stock=$(realpath "$2")
mkdir -p "$3"
build=$(realpath "$3")
[ "$build" != "$kernel" ]
cp "$stock" "$build/.config"
"$kernel/scripts/config" --file "$build/.config" \
    --set-str LOCALVERSION '-q1-ramdiag' -d LOCALVERSION_AUTO \
    -e ARCH_VIRT -e SERIAL_AMBA_PL011 -e SERIAL_AMBA_PL011_CONSOLE \
    -e BLK_DEV_INITRD -e RD_GZIP -d INITRD_ASYNC -e BLK_DEV_NBD \
    -e USB_GADGET -e USB_DWC3 -e USB_DWC3_DUAL_ROLE -e USB_DWC3_OF_SIMPLE \
    -e USB_XHCI_HCD -e USB_XHCI_PLATFORM -m USB_CONFIGFS \
    -e USB_CONFIGFS_F_HID -e USB_CONFIGFS_MASS_STORAGE -d USB_CONFIGFS_F_FS \
    -d MTD -d MMC_BLOCK -d PSTORE -d IP_PNP -d UEVENT_HELPER -d WATCHDOG \
    --set-val PANIC_TIMEOUT 0
make -s -C "$kernel" O="$build" ARCH=arm CROSS_COMPILE="${CROSS_COMPILE:?}" \
    CC="${CROSS_COMPILE}gcc" LOCALVERSION= olddefconfig
for name in ARCH_ROCKCHIP ARCH_VIRT SERIAL_AMBA_PL011_CONSOLE BLK_DEV_INITRD \
            RD_GZIP BLK_DEV_NBD USB_GADGET USB_DWC3 USB_DWC3_DUAL_ROLE \
            USB_DWC3_OF_SIMPLE PHY_ROCKCHIP_INNO_USB2 USB_CONFIGFS_MASS_STORAGE USB_CONFIGFS_F_HID; do
    grep -Fqx "CONFIG_$name=y" "$build/.config"
done
for name in MTD MMC_BLOCK PSTORE IP_PNP WATCHDOG UEVENT_HELPER INITRD_ASYNC; do
    if grep -Eq "^CONFIG_$name=[ym]$" "$build/.config"; then
        echo "Unexpected CONFIG_$name in diagnostic kernel" >&2
        exit 1
    fi
done
