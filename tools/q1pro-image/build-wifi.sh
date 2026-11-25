#!/bin/sh
# Build the upstream supplicant, nl80211 transport and private crypto runtime.
set -eu
: "${BUILDKIT_PATH:=/opt/jetkvm-native-buildkit}"
sources=$(realpath "${1:?directory with the three pinned source archives}")
out=$(realpath -m "${2:?output directory}")
mkdir -p "$out"
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$sources"
sha256sum -c <<'SUMS'
fc51ca7196f1a3f5fdf6ffd3864b50f4f9c02333be28be4eeca057e103c0dd18  libnl-3.12.0.tar.gz
08e23937e16d0155e55cab2b51f51fbe10d80a1aa91c4e15442645059b737ef6  wpa_supplicant-2.12.tar.gz
a2157c2830efdec3788939b00c9b0638306d3f0bbb76dc4832ee503bb397df98  openssl-3.6.5.tar.gz
SUMS
for f in libnl-3.12.0 wpa_supplicant-2.12 openssl-3.6.5; do tar xf "$f.tar.gz" -C "$work"; done
target=arm-rockchip830-linux-uclibcgnueabihf
export SOURCE_DATE_EPOCH=1 PATH="$BUILDKIT_PATH/bin:$PATH"
cd "$work/libnl-3.12.0"
./configure --host="$target" --disable-shared --enable-static --enable-cli=no >/dev/null
make -j4 lib/libnl-3.la lib/libnl-genl-3.la >/dev/null
cd "$work/openssl-3.6.5"
./Configure linux-armv4 --cross-compile-prefix="$target-" --prefix=/usr --openssldir=/etc/ssl \
    no-shared no-module no-engine no-tests no-apps no-docs no-asm -Os >/dev/null
make -j4 build_libs >/dev/null
cd "$work/wpa_supplicant-2.12/wpa_supplicant"
cp "$here/wpa.config" .config
export CC="$target-gcc"
export CFLAGS="-Os -I$work/libnl-3.12.0/include -I$work/openssl-3.6.5/include"
export LDFLAGS="-L$work/libnl-3.12.0/lib/.libs -L$work/openssl-3.6.5"
# Suppress build-host pkg-config results; cross headers and archives are explicit.
export PKG_CONFIG_LIBDIR="$work/empty"
make -j4 LIBNL_INC="$work/libnl-3.12.0/include" wpa_supplicant wpa_cli >/dev/null
"$target-strip" wpa_supplicant wpa_cli
install -m 0755 wpa_supplicant wpa_cli "$out"
