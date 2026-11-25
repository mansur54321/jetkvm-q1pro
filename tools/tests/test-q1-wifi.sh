#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
(cd "$root" && go test -c -o "$work/wifi.test" ./internal/q1wifi)
bwrap --unshare-user --uid 0 --gid 0 --unshare-net --unshare-pid --die-with-parent \
    --cap-add CAP_NET_ADMIN --cap-add CAP_NET_RAW --ro-bind / / --proc /proc --dev /dev \
    --tmpfs /run --tmpfs /tmp --bind "$work" "$work" --chdir "$work" \
    --setenv Q1_WIFI_TEST_DIR "$work" -- /bin/sh -ec '
        ip link add wlan0 type dummy
        ip link set wlan0 up
        umask 077
        printf "ctrl_interface=%s/ctrl\nupdate_config=1\n" "$PWD" > wifi.conf
        wpa_supplicant -Dnone -i wlan0 -c "$PWD/wifi.conf" -B -f "$PWD/wpa.log"
        exec ./wifi.test -test.v -test.timeout=75s
    '
