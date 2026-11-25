#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
sdk=${BUILDKIT_PATH:-/opt/jetkvm-native-buildkit}
target=arm-rockchip830-linux-uclibcgnueabihf
cc=$sdk/bin/$target-gcc
sysroot=$sdk/$target/sysroot
src=$root/tools/q1pro-capture
out=${1:-$root/bin/q1pro-capture}
mkdir -p "$out"
"$cc" --sysroot="$sysroot" -Os -s -std=gnu11 -Wall -Wextra \
    "$src/main.c" -ldl -o "$out/q1pro-capture"
"$cc" --sysroot="$sysroot" -static -Os -s -std=gnu11 -Wall -Wextra -Werror \
    "$src/tools-frame.c" -o "$out/q1pro-tools-frame"
"$cc" --sysroot="$sysroot" -Os -s -std=gnu11 -D_GNU_SOURCE -DLOG_LEVEL=LEVEL_WARN \
    -fPIC -shared -fvisibility=hidden -ffunction-sections -fdata-sections -Wl,--gc-sections,-z,defs \
    "$src/capture.c" -L"$sysroot/usr/lib" -lrockit -lrockchip_mpp -lrga -pthread -lm \
    -o "$out/q1pro-video.so"
