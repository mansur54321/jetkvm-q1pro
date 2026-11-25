#!/bin/sh
set -eu
PATH=/bin:/sbin:/usr/bin:/usr/sbin
export PATH

configure_port_led() {
    led=/sys/class/leds/eth:$1
    [ -w "$led/trigger" ] || return 1
    echo netdev >"$led/trigger" || return 1
    echo eth0 >"$led/device_name" || return 1
    echo "$2" >"$led/link" || return 1
    echo "$3" >"$led/rx" || return 1
    echo "$3" >"$led/tx" || return 1
}

show_state() {
    case "$1" in
        boot) colors=red; on=0; off=0;;
        offline) colors=red; on=300; off=300;;
        address) colors='red green'; on=0; off=0;;
        wifi-search) colors=blue; on=150; off=350;;
        ble-wait) colors=blue; on=150; off=150;;
        ble-pair) colors="green blue"; on=150; off=150;;
        ble-accepted) colors=blue; on=0; off=0;;
        ethernet) colors=green; on=0; off=0;;
        wifi) colors=blue; on=0; off=0;;
        ethernet-active) colors=green; on=1200; off=150;;
        wifi-active) colors=blue; on=1200; off=150;;
        *) return 1;;
    esac
    for color in red green blue; do
        led=/sys/class/leds/status:$color
        [ -w "$led/trigger" ] && [ -w "$led/brightness" ] || return 1
        echo none >"$led/trigger" || return 1
        case " $colors " in
            *" $color "*)
                IFS= read -r level <"$led/max_brightness" || return 1
                echo "$level" >"$led/brightness" || return 1
                if [ "$on" != 0 ]; then
                    echo timer >"$led/trigger" || return 1
                    echo "$on" >"$led/delay_on" || return 1
                    echo "$off" >"$led/delay_off" || return 1
                fi;;
            *) echo 0 >"$led/brightness" || return 1;;
        esac
    done
}

has_carrier() {
    carrier=/sys/class/net/$1/carrier
    [ -r "$carrier" ] && IFS= read -r link <"$carrier" && [ "$link" = 1 ]
}

has_address() {
    ip addr show dev "$1" scope global 2>/dev/null |
        awk '($1 == "inet" || $1 == "inet6") && !/ tentative| dadfailed/ {found=1} END {exit !found}'
}

state() {
    if [ ! -e /run/jetkvm-ready ]; then echo boot; return; fi
    marker=/run/q1-ble-state
    if [ -f "$marker" ] && { ! flock -s -n 9; } 2>/dev/null 9<"$marker"; then
        case "$(cat "$marker")" in
            1) echo ble-wait; return;;
            P) echo ble-pair; return;;
            2|3|4) echo ble-accepted; return;;
        esac
    fi
    linked=0
    for iface in eth0 wlan0; do
        has_carrier "$iface" 2>/dev/null || continue
        linked=1
        has_address "$iface" || continue
        if [ "$iface" = eth0 ]; then transport=ethernet; else transport=wifi; fi
        marker=/run/jetkvm-control-active
        if [ -f "$marker" ] && { ! flock -s -n 9; } 2>/dev/null 9<"$marker"; then
            echo "$transport-active"
        else
            echo "$transport"
        fi
        return
    done
    if [ "$linked" = 1 ]; then echo address; return; fi
    if [ -S /run/wpa_supplicant/wlan0 ]; then
        if wpa_cli -p /run/wpa_supplicant -i wlan0 status 2>/dev/null |
                grep -Eq '^wpa_state=(SCANNING|AUTHENTICATING|ASSOCIATING|ASSOCIATED|4WAY_HANDSHAKE|GROUP_HANDSHAKE)$'; then
            echo wifi-search
            return
        fi
    fi
    echo offline
}

case "${1:-monitor}" in
    boot)
        configure_port_led link 1 0 || :
        configure_port_led act 0 1 || :
        show_state boot
        exit $?;;
    monitor) ;;
    *) exit 2;;
esac
previous=
link_ready=0
act_ready=0
while :; do
    if [ "$link_ready" = 0 ] && configure_port_led link 1 0; then link_ready=1; fi
    if [ "$act_ready" = 0 ] && configure_port_led act 0 1; then act_ready=1; fi
    current=$(state)
    if [ "$current" != "$previous" ] && show_state "$current"; then previous=$current; fi
    sleep 3
done
