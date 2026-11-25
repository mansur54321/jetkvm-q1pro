#!/bin/sh
# Exercise the actual ARM supplicant and its file backend, without a radio.
set -eu
image=$(realpath "${1:?standard rootfs SquashFS}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unsquashfs -quiet -no-progress -d "$work/sys" "$image" lib usr/bin/wpa_cli usr/sbin/wpa_supplicant
bwrap --unshare-user --uid 0 --gid 0 --cap-add CAP_NET_ADMIN --cap-add CAP_NET_RAW --unshare-pid --unshare-net --die-with-parent --ro-bind / / --dev /dev --proc /proc \
    --tmpfs /tmp --bind "$work" "$work" --chdir "$work" --clearenv --setenv PATH /usr/bin \
    --setenv WORK "$work" -- /bin/sh -eu <<'TEST'
umask 077
ip link add wlan0 type dummy
ip link set wlan0 up
mkdir ctrl
printf '%s\n' "ctrl_interface=$WORK/ctrl" update_config=1 >wifi.conf
qemu-arm -L "$WORK/sys" "$WORK/sys/usr/sbin/wpa_supplicant" -Dnone -i wlan0 -c "$WORK/wifi.conf" >server.log 2>&1 &
daemon=$!
trap 'ret=$?; if [ "$ret" -ne 0 ]; then cat server.log >&2; fi; kill "$daemon" 2>/dev/null || true' EXIT
for tick in $(seq 1 50); do
    [ -S ctrl/wlan0 ] && break
    kill -0 "$daemon"
    sleep 0.1
done
cli() { qemu-arm -L "$WORK/sys" "$WORK/sys/usr/bin/wpa_cli" -p "$WORK/ctrl" -i wlan0 "$@"; }
[ "$(cli ping)" = PONG ]
network=$(cli add_network)
[ "$(cli set_network "$network" ssid '"q1-test-fixture"')" = OK ]
[ "$(cli set_network "$network" key_mgmt NONE)" = OK ]
[ "$(cli save_config)" = OK ]
[ "$(stat -c %a wifi.conf)" = 600 ]
[ "$(cli reconfigure)" = OK ]
cli list_networks | grep -q q1-test-fixture
[ "$(cli remove_network "$network")" = OK ]
[ "$(cli save_config)" = OK ]
if grep -q q1-test-fixture wifi.conf; then exit 1; fi
printf '%s\n' 'QEMU ARM Wi-Fi control socket, config save, reload and removal passed.'
TEST
