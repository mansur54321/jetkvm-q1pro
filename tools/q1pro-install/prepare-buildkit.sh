#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "Usage: $0 OUTPUT_DIRECTORY" >&2
    exit 2
fi

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
mkdir -p -- "$1"
out=$(realpath "$1")
archives="$out/archives"
mkdir -p -- "$archives" "$out/bin" "$out/wifi"

work=$(mktemp -d "$out/.prepare.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

download() {
    name=$1
    url=$2
    checksum=$3
    if [ ! -f "$archives/$name" ]; then
        curl --fail --location --retry 2 --proto '=https' --proto-redir '=https' \
            --output "$work/$name" "$url"
        printf '%s  %s\n' "$checksum" "$work/$name" | sha256sum --check --status
        mv -- "$work/$name" "$archives/$name"
    fi
    printf '%s  %s\n' "$checksum" "$archives/$name" | sha256sum --check
}

download kvm-native-buildkit.tar.zst \
    https://github.com/jetkvm/rv1106-system/releases/download/release/v0.2.8/kvm-native-buildkit.tar.zst \
    5176fb424d7b51bfa136af7d0afb76fd4c22957dead21c22194a78531969123c
download busybox-1.37.0.tar.bz2 \
    https://busybox.net/downloads/busybox-1.37.0.tar.bz2 \
    3311dff32e746499f4df0d5df04d7eb396382d7e108bb9250e7b519b837043a4
download dropbear-2026.94.tar.bz2 \
    https://dropbear.nl/mirror/releases/dropbear-2026.94.tar.bz2 \
    e098034a843699200c8c977a991fff73159735bf795d5f72ef672c41a6b1ae81
download libnl-3.12.0.tar.gz \
    https://github.com/thom311/libnl/releases/download/libnl3_12_0/libnl-3.12.0.tar.gz \
    fc51ca7196f1a3f5fdf6ffd3864b50f4f9c02333be28be4eeca057e103c0dd18
download wpa_supplicant-2.12.tar.gz \
    https://w1.fi/releases/wpa_supplicant-2.12.tar.gz \
    08e23937e16d0155e55cab2b51f51fbe10d80a1aa91c4e15442645059b737ef6
download openssl-3.6.5.tar.gz \
    https://github.com/openssl/openssl/releases/download/openssl-3.6.5/openssl-3.6.5.tar.gz \
    a2157c2830efdec3788939b00c9b0638306d3f0bbb76dc4832ee503bb397df98

if [ ! -e "$out/sdk" ]; then
    mkdir -- "$work/sdk"
    tar -I 'zstd --long=31' --extract --file "$archives/kvm-native-buildkit.tar.zst" \
        --directory "$work/sdk" --no-same-owner
    mv -- "$work/sdk" "$out/sdk"
fi

export BUILDKIT_PATH="$out/sdk"
test -x "$BUILDKIT_PATH/bin/arm-rockchip830-linux-uclibcgnueabihf-gcc"
"$here/../q1pro-image/build-busybox.sh" "$archives/busybox-1.37.0.tar.bz2" "$work/busybox-arm"
"$here/../q1pro-image/build-dropbear.sh" "$archives/dropbear-2026.94.tar.bz2" "$work/dropbearmulti-arm"
"$here/../q1pro-image/build-wifi.sh" "$archives" "$work/wifi"
mv -- "$work/busybox-arm" "$work/dropbearmulti-arm" "$out/bin/"
mv -- "$work/wifi/wpa_cli" "$work/wifi/wpa_supplicant" "$out/wifi/"
printf 'Build inputs are ready in %s\n' "$out"
