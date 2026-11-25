#!/bin/sh
# Build against independently generated symbol versions, then check the stock ABI.
set -eu
if [ "$#" -ne 4 ]; then
    echo "usage: CROSS_COMPILE=<arm-gcc-prefix> $0 KERNEL_DIR STOCK_CONFIG STOCK_BOOT NEW_OUTPUT_DIR" >&2
    exit 2
fi
: "${CROSS_COMPILE:?set the RV1106 buildkit toolchain prefix}"
here=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
kernel=$(realpath "$1")
config=$(realpath "$2")
boot=$(realpath "$3")
mkdir -- "$4"
out=$(realpath "$4")
obj="$out/build"
mkdir "$obj"
cp "$config" "$obj/.config"
"$kernel/scripts/config" --file "$obj/.config" --module BLK_DEV_NBD
# The SDK defaults to a Python wrapper which rejects warnings from all board
# configurations (e.g. an unused RCU helper under the Q1 config).
kmake() {
    make -C "$kernel" O="$obj" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" \
        CC="${CROSS_COMPILE}gcc" LOCALVERSION= quiet=quiet_ "$@"
}
kmake olddefconfig
grep -qx CONFIG_BLK_DEV_NBD=m "$obj/.config"
grep -qx CONFIG_MODVERSIONS=y "$obj/.config"
kmake -j"${JOBS:-4}" vmlinux
kmake -j"${JOBS:-4}" drivers/block/nbd.ko
python3 "$here/check-nbd-abi.py" "$boot" "$obj/drivers/block/nbd.ko"
cp "$obj/drivers/block/nbd.ko" "$out/nbd.ko"
