#!/bin/sh
# JetKVM calls this after toggling developer mode.
set -eu
umask 077
exec 9>/run/jetkvm-ssh.lock
flock -x 9
pidfile=/run/jetkvm-ssh.pid
if [ ! -f /userdata/jetkvm/devmode.enable ]; then
    if [ -s "$pidfile" ]; then
        start-stop-daemon -K -q -p "$pidfile" -x /usr/sbin/dropbearmulti || true
    fi
    exit 0
fi
home=/userdata/dropbear
mkdir -p "$home/.ssh"
chmod 700 "$home" "$home/.ssh"
key=$home/host_ed25519
if [ ! -s "$key" ]; then
    # A stopped generation leaves only this temporary key, replaced on retry.
    if [ -e "$key.new" ]; then rm -f "$key.new"; fi
    dropbearkey -t ed25519 -f "$key.new" >/dev/null
    mv "$key.new" "$key"
fi
# The daemon closes the lock descriptor on launch. Password auth is compiled out.
start-stop-daemon -S -q -o -p "$pidfile" -x /usr/sbin/dropbear -- -E -r "$key" -P "$pidfile" 9>&- >/dev/console 2>&1
