#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
sdk=${BUILDKIT_PATH:-/opt/jetkvm-native-buildkit}
target=arm-rockchip830-linux-uclibcgnueabihf
cc=$sdk/bin/$target-gcc
sysroot=$sdk/$target/sysroot
build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT

"$cc" --sysroot="$sysroot" -std=gnu11 -Os -ffunction-sections -fdata-sections \
    -D_GNU_SOURCE -DLOG_LEVEL=99 -Dopen=test_open -Dclose=test_close \
    -Dioctl=test_ioctl -Dselect=test_select -Dusleep=test_usleep \
    -Dpthread_create=test_pthread_create -Dpthread_join=test_pthread_join -Dpthread_detach=test_pthread_detach \
    -c "$root/internal/native/cgo/video.c" -o "$build/video.o"
"$cc" --sysroot="$sysroot" -std=gnu11 -Os -D_GNU_SOURCE -static -pthread \
    -Wl,--gc-sections -I"$root/internal/native/cgo" \
    "$root/tools/tests/video-lifecycle.c" "$build/video.o" -o "$build/test-video"
mkdir -p "$build/root/etc"
qemu-arm -L "$build/root" "$build/test-video" legacy
printf 'jetkvm-v2\n' > "$build/root/etc/jetkvm-sku"
qemu-arm -L "$build/root" "$build/test-video" legacy
printf 'oraybox-q1pro\n' > "$build/root/etc/jetkvm-sku"
qemu-arm -L "$build/root" "$build/test-video" q1
