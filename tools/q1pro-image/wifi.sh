#!/bin/sh
# BusyBox supervises Wi-Fi separately from Ethernet and the app.
set -eu
export PATH=/bin:/sbin:/usr/bin:/usr/sbin
while [ ! -e /run/jetkvm-ready ]; do sleep 2; done
umask 077
conf=/userdata/jetkvm/wifi.conf
if [ ! -f "$conf" ]; then
    { echo 'ctrl_interface=/run/wpa_supplicant'; echo 'update_config=1'; } >"$conf.new"
    mv "$conf.new" "$conf"
fi
chmod 600 "$conf"
mkdir -p /run/wpa_supplicant
modules=/lib/modules/$(uname -r)
while read -r mod; do
    if [ ! -d "/sys/module/$mod" ]; then
        if ! insmod "$modules/$mod.ko"; then
            echo "Wi-Fi module failed: $mod" >&2
            sleep 10
            exit 1
        fi
    fi
done </etc/jetkvm-wifi-modules
while [ ! -e /sys/class/net/wlan0 ]; do sleep 2; done
ip link set wlan0 up
# Delay a retry if the interface disappears while wpa_supplicant is running.
/usr/sbin/wpa_supplicant -Dnl80211 -i wlan0 -c "$conf" || true
sleep 5
