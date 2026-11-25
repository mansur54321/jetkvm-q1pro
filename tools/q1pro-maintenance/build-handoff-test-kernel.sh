#!/bin/sh
# Build a private QEMU kernel with the observed Q1 SquashFS layout and a software watchdog.
set -eu
src=$(realpath "${1:?RV1106 BSP sources}")
cfg=$(realpath "${2:?baseline QEMU q1-systest .config}")
dst=$(realpath -m "${3:?new output directory}")
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
: "${CROSS_COMPILE:?Q1 ARM toolchain prefix}"
mkdir "$dst"
cp -a --reflink=auto "$src" "$dst/kernel"
patch --quiet -d "$dst/kernel" -p1 <"$here/testdata/sfoo-kernel.patch"
mkdir "$dst/build"
cp "$cfg" "$dst/build/.config"
"$dst/kernel/scripts/config" --file "$dst/build/.config" -e SOFT_WATCHDOG
make -s -C "$dst/kernel" O="$dst/build" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" CC="${CROSS_COMPILE}gcc" LOCALVERSION= olddefconfig
make -s -C "$dst/kernel" O="$dst/build" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" CC="${CROSS_COMPILE}gcc" LOCALVERSION= -j"${JOBS:-4}" zImage
