#!/bin/sh
# Exercise static Ethernet and wireless DHCP using isolated veth pairs.
set -eu
cd "$(dirname "$0")/../.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go test -race -c -o "$work/nmlite.test" ./pkg/nmlite
touch "$work/hostname" "$work/hosts" "$work/resolv.conf"
bwrap --unshare-user --uid 0 --gid 0 --unshare-uts --hostname q1-test \
    --unshare-net --unshare-pid --die-with-parent --cap-add CAP_NET_ADMIN --cap-add CAP_NET_RAW \
    --cap-add CAP_NET_BIND_SERVICE --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp \
    --ro-bind "$work/nmlite.test" /tmp/nmlite.test --bind "$work/hostname" /etc/hostname \
    --bind "$work/hosts" /etc/hosts --bind "$work/resolv.conf" /etc/resolv.conf \
    --setenv JETKVM_ISOLATED_NETWORK q1-test -- /tmp/nmlite.test -test.v -test.run '^TestIsolatedDualInterface$'
