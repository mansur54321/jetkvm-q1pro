#!/bin/sh
set -eu
: "${BUILDKIT_PATH:=/opt/jetkvm-native-buildkit}"
archive=$(realpath "${1:?path to dropbear-2026.94.tar.bz2}")
out=$(realpath -m "${2:?output binary path}")
printf '%s  %s\n' e098034a843699200c8c977a991fff73159735bf795d5f72ef672c41a6b1ae81 "$archive" | sha256sum -c -
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tar -xf "$archive" -C "$work"
cd "$work/dropbear-2026.94"
export SOURCE_DATE_EPOCH=1
target=arm-rockchip830-linux-uclibcgnueabihf
export PATH="$BUILDKIT_PATH/bin:$PATH"
printf '%s\n' '#define DROPBEAR_SVR_PASSWORD_AUTH 0' >localoptions.h
./configure --host="$target" --enable-static --enable-bundled-libtom --disable-zlib \
    --disable-shadow --disable-utmp --disable-utmpx --disable-wtmp --disable-wtmpx --disable-lastlog >/dev/null
make -j4 MULTI=1 PROGRAMS="dropbear dropbearkey" >/dev/null
"$target-strip" dropbearmulti
install -m 0755 dropbearmulti "$out"
