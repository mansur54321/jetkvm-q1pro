#!/bin/sh
set -eu
src=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
sdk=${BUILDKIT_PATH:-/opt/jetkvm-native-buildkit}
target=arm-rockchip830-linux-uclibcgnueabihf
sysroot=$sdk/$target/sysroot
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
"$src/build.sh" "$tmp/bin"
"$sdk/bin/$target-gcc" --sysroot="$sysroot" -std=gnu11 -Wall -Wextra -Werror -static \
    "$src/test-tools-frame.c" -o "$tmp/test-tools-frame"
qemu-arm "$tmp/test-tools-frame" 2>"$tmp/tools-frame-errors"
for args in "--check" "--one" "--check 640x480" "--one 1920x1081"; do
    code=0
    qemu-arm "$tmp/bin/q1pro-tools-frame" $args >"$tmp/cli-output" 2>"$tmp/cli-error" || code=$?
    test "$code" = 2 && test ! -s "$tmp/cli-output"
done


prefix=$tmp/target
mkdir -p "$prefix/proc/1" "$prefix/proc/19"
ln -s "$sysroot/lib" "$prefix/lib"
ln -s "$sysroot/usr" "$prefix/usr"
printf 'init\n' > "$prefix/proc/1/comm"
printf 'video-server\n' > "$prefix/proc/19/comm"
for mode in --capture --check; do
    rc=0
    qemu-arm -L "$prefix" "$tmp/bin/q1pro-capture" "$mode" > "$tmp/out" 2> "$tmp/err" || rc=$?
    test "$rc" = 3
    test ! -s "$tmp/out"
    grep -q 'video-server (PID 19)' "$tmp/err"
done
printf '%s\n' 'ok - busy checks return 3 without loading Rockit or emitting video'

# Make the subdevice path exist only as a regular test file. A real V4L2
# request must fail with ENOTTY here, before the Rockit worker is loaded.
printf 'idle\n' > "$prefix/proc/19/comm"
mkdir "$prefix/dev"
touch "$prefix/dev/v4l-subdev2"
rc=0
qemu-arm -L "$prefix" "$tmp/bin/q1pro-capture" --capture > "$tmp/out" 2> "$tmp/err" || rc=$?
test "$rc" = 4
test ! -s "$tmp/out"
grep -q VIDIOC_QUERY_DV_TIMINGS "$tmp/err"
printf '%s\n' 'ok - invalid input fails before capture with empty stdout'

"$sdk/bin/$target-gcc" --sysroot="$sysroot" -Os -fPIC -shared "$src/test-timing.c" -o "$tmp/timing.so"
qemu-arm -L "$prefix" -E LD_PRELOAD="$tmp/timing.so" "$tmp/bin/q1pro-capture" --check > "$tmp/out" 2> "$tmp/err"
test ! -s "$tmp/out"
grep -q '1600x1200 at 60.00 Hz' "$tmp/err"
qemu-arm -L "$prefix" -E LD_PRELOAD="$tmp/timing.so" -E Q1_TEST_WIDE=1080 "$tmp/bin/q1pro-capture" --check > "$tmp/out" 2> "$tmp/err"
test ! -s "$tmp/out"
grep -q '1920x1080 at 60.00 Hz' "$tmp/err"
rc=0
qemu-arm -L "$prefix" -E LD_PRELOAD="$tmp/timing.so" -E Q1_TEST_WIDE=oversize "$tmp/bin/q1pro-capture" --check > "$tmp/out" 2> "$tmp/err" || rc=$?
test "$rc" -eq 4 && test ! -s "$tmp/out"
grep -q 'Unsupported source timing 1920x1200' "$tmp/err"

printf '%s\n' 'ok - Q1 1920x1080 and 1600x1200 timings accepted, oversized 1920x1200 refused'

mkdir "$prefix/proc/device-tree"
printf 'oraybox,q1pro\0rockchip,rv1106\0' > "$prefix/proc/device-tree/compatible"
"$sdk/bin/$target-gcc" --sysroot="$sysroot" -Os "$src/test-load.c" -ldl -o "$tmp/load"
qemu-arm -L "$prefix" "$tmp/load" "$tmp/bin/q1pro-video.so"
printf '%s\n' 'ok - RV1106 Rockit module and capture entry point load'

"$sdk/bin/$target-gcc" --sysroot="$sysroot" -std=gnu11 -Os -D_GNU_SOURCE \
    -ffunction-sections -fdata-sections -Wl,--gc-sections -static -pthread \
    "$src/test-writer.c" -o "$tmp/test-writer"
ffmpeg -v error -f lavfi -i testsrc2=s=128x96 -frames:v 1 -c:v libx264 -threads 1 \
    -x264-params 'aud=1:repeat-headers=1' -f h264 "$tmp/frame.h264"
qemu-arm "$tmp/test-writer" "$tmp/frame.h264" > "$tmp/clip.h264"
count=$(ffprobe -v error -f h264 -select_streams v:0 -count_frames \
    -show_entries stream=nb_read_frames -of csv=p=0 "$tmp/clip.h264")
test "$count" = 30
printf '%s\n' 'ok - 30 Annex B pictures decode, frame limit and output errors verified'

rm "$prefix/proc/19/comm"
mkfifo "$prefix/proc/19/comm"
rc=0
timeout 20 qemu-arm -L "$prefix" "$tmp/bin/q1pro-capture" --capture > "$tmp/out" 2> "$tmp/err" || rc=$?
test "$rc" = 124
grep -q '15-second deadline' "$tmp/err"
test ! -s "$tmp/out"
printf '%s\n' 'ok - stalled preflight terminates at the internal deadline'
