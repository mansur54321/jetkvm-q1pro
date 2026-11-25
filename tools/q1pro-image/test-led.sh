#!/bin/sh
# Exercise the board script with real veth carriers and a private LED-class fixture.
set -eu
base=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/leds"
bwrap --unshare-user --uid 0 --gid 0 --unshare-pid --unshare-net --cap-add CAP_NET_ADMIN --cap-add CAP_SYS_ADMIN \
    --die-with-parent --ro-bind / / --dev /dev --proc /proc --tmpfs /sys --tmpfs /run --tmpfs /tmp \
    --bind "$work/leds" /run/leds --ro-bind "$base/status-led.sh" /run/status-led -- /bin/sh -eu <<'TEST'
mount -t sysfs -o ro,nosuid,nodev,noexec sysfs /sys
mount --bind /run/leds /sys/class/leds
ip link add eth0 type veth peer name ethpeer
ip link add wlan0 type veth peer name wifipeer
ip link set eth0 up
ip link set wlan0 up
led=/sys/class/leds
for color in red green blue; do
    mkdir "$led/status:$color"
    echo 255 >"$led/status:$color/max_brightness"
    for attr in trigger brightness delay_on delay_off; do echo 0 >"$led/status:$color/$attr"; done
done
expect() {
    label=$1; colors=$2; on=$3; off=$4
    for _ in $(seq 1 65); do
        matches=0
        for color in red green blue; do
            dir="$led/status:$color"
            level=0; trigger=none
            case " $colors " in *" $color "*) level=255; [ "$on" = 0 ] || trigger=timer;; esac
            [ "$(cat "$dir/brightness")" = "$level" ] && [ "$(cat "$dir/trigger")" = "$trigger" ] || continue
            if [ "$trigger" = timer ]; then
                [ "$(cat "$dir/delay_on")" = "$on" ] && [ "$(cat "$dir/delay_off")" = "$off" ] || continue
            fi
            matches=$((matches+1))
        done
        if [ "$matches" = 3 ]; then echo "PASS $label"; return; fi
        sleep 0.1
    done
    echo "Timed out: $label" >&2
    return 1
}
sh /run/status-led boot
expect boot red 0 0
sh /run/status-led &
monitor=$!
trap 'kill "$monitor" 2>/dev/null || true' EXIT
touch /run/jetkvm-ready
expect no-carrier red 300 300

# GPIO LEDs may appear after the early boot invocation.
for name in eth:link eth:act; do
    mkdir "$led/$name"
    for attr in trigger device_name link tx rx; do echo 0 >"$led/$name/$attr"; done
done
ip link set ethpeer up
expect ethernet-address-pending 'red green' 0 0
for name in link act; do
    dir="$led/eth:$name"
    [ "$(cat "$dir/trigger")" = netdev ] && [ "$(cat "$dir/device_name")" = eth0 ]
    if [ "$name" = link ]; then carrier=1; activity=0; else carrier=0; activity=1; fi
    [ "$(cat "$dir/link")" = "$carrier" ] && [ "$(cat "$dir/tx")" = "$activity" ] && [ "$(cat "$dir/rx")" = "$activity" ]
done
echo 'PASS RJ45 netdev link and independent RX/TX configuration'
ip addr add 192.0.2.8/24 dev eth0
expect ethernet green 0 0

mkdir /run/wpa_supplicant
echo SCANNING >/run/wpa-test-state
python3 - <<'PY' &
import socket
from pathlib import Path
s=socket.socket(socket.AF_UNIX,socket.SOCK_DGRAM)
s.bind('/run/wpa_supplicant/wlan0')
while True:
    request,peer=s.recvfrom(4096)
    if request == b'STATUS':
        s.sendto(('wpa_state='+Path('/run/wpa-test-state').read_text().strip()+'\n').encode(),peer)
    else:
        s.sendto(b'OK\n',peer)
PY
supplicant=$!
trap 'kill "$monitor" "$supplicant" 2>/dev/null || true' EXIT
ip link set wifipeer up
ip addr add 198.51.100.4/24 dev wlan0
expect ethernet-preferred green 0 0

exec 8>/run/jetkvm-control-active
flock -x 8
expect ethernet-control-session green 1200 150
ip link set ethpeer down
expect wifi-control-session blue 1200 150
flock -u 8
exec 8>&-
expect wifi-stale-session-marker blue 0 0
ip addr del 198.51.100.4/24 dev wlan0
expect wifi-address-pending 'red green' 0 0
ip link set wifipeer down
expect supplicant-scanning blue 150 350
echo DISCONNECTED >/run/wpa-test-state
expect no-associated-network red 300 300
ip link set ethpeer up
expect ethernet-reconnect green 0 0
exec 7>/run/q1-ble-state
flock -x 7
echo 1 >/run/q1-ble-state
expect ble-authorization blue 150 150
echo P >/run/q1-ble-state
expect ble-pairing-confirmation 'green blue' 150 150
echo 2 >/run/q1-ble-state
expect ble-authorized blue 0 0
flock -u 7
exec 7>&-
expect after-ble-closed green 0 0
TEST
