#!/bin/sh
# Invoked once per physical provisioning window by the Q1 application.
set -eu
export PATH=/bin:/sbin:/usr/bin:/usr/sbin
export DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/q1-ble/bus
umask 077
[ "$(cat /etc/jetkvm-sku)" = oraybox-q1pro ]
run=/run/q1-ble
mkdir -p "$run/bonds"
exec 8>"$run/stack.lock"
flock -n 8 || exit 1
if [ ! -s "$run/machine-id" ]; then tr -d '-' </proc/sys/kernel/random/uuid >"$run/machine-id"; fi
[ -c /dev/ttyS5 ]
mods=/lib/modules/$(uname -r)
for mod in aic8800_bsp bluetooth hci_uart aic8800_btlpm; do
    if [ ! -d "/sys/module/$mod" ]; then insmod "$mods/$mod.ko" || [ -d "/sys/module/$mod" ]; fi
done
blocked=
attach= bus= bluez=
cleanup() {
    trap - TERM INT EXIT
    for pid in $bluez $attach $bus; do kill "$pid" 2>/dev/null || true; done
    for pid in $bluez $attach $bus; do wait "$pid" 2>/dev/null || true; done
    if [ -n "$blocked" ]; then echo 1 >"$blocked/soft" || true; fi
}
trap cleanup EXIT
trap 'exit 0' TERM INT
radio=
for rf in /sys/class/rfkill/rfkill*; do
    [ -r "$rf/type" ] && [ "$(cat "$rf/type")" = bluetooth ] || continue
    [ "$(cat "$rf/name")" = bluetooth ] || continue
    [ -z "$radio" ] || exit 1
    radio=$rf
done
if [ -n "$radio" ] && [ "$(cat "$radio/soft")" = 1 ]; then
    blocked=$radio
    echo 0 >"$radio/soft"
fi
/usr/bin/dbus-daemon --config-file=/etc/q1-ble-bus.conf --nofork --nopidfile &
bus=$!
for i in 1 2 3 4 5; do [ ! -S "$run/bus" ] || break; sleep 1; done
[ -S "$run/bus" ] && kill -0 "$bus"
# Q1 UCI defaults select UART5 with H4, hardware flow control and this initial speed.
/usr/bin/hciattach -n -s 1500000 /dev/ttyS5 any 1500000 flow nosleep &
attach=$!
/usr/bin/bluetoothd -n -f /etc/q1-ble-bluez.conf -P battery &
bluez=$!
while kill -0 "$attach" && kill -0 "$bus" && kill -0 "$bluez"; do sleep 1; done
exit 1
