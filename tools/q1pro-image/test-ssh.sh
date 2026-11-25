#!/bin/sh
# Test both authentication outcomes in a private network namespace with disposable keys.
set -eu
image=$(realpath "${1:?standard rootfs SquashFS}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unsquashfs -quiet -no-progress -d "$work/sys" "$image" usr/sbin/dropbearmulti etc/passwd etc/group etc/shells
# The kernel's unprivileged user namespace denies setgroups(2), which sshd
# needs after a valid login. Use an ephemeral, network-isolated container.
docker run --rm -i --pull=never --read-only --network none --cap-drop ALL \
    --cap-add SETUID --cap-add SETGID --cap-add DAC_OVERRIDE --tmpfs /run --tmpfs /tmp \
    --tmpfs /userdata --mount type=bind,src=/usr,dst=/usr,readonly \
    --mount "type=bind,src=$work/sys,dst=/fixture,readonly" \
    --mount "type=bind,src=$work/sys/etc/passwd,dst=/etc/passwd,readonly" \
    --mount "type=bind,src=$work/sys/etc/group,dst=/etc/group,readonly" \
    --mount "type=bind,src=$work/sys/etc/shells,dst=/etc/shells,readonly" \
    --entrypoint /bin/sh "${Q1_TEST_IMAGE:-python:3.13-slim}" -eu <<'TEST'
umask 077
cd /tmp
db=/fixture/usr/sbin/dropbearmulti
mkdir -m 0700 /userdata/dropbear
mkdir -m 0700 /userdata/dropbear/.ssh
ssh-keygen -q -t ed25519 -N '' -f client
ssh-keygen -q -t ed25519 -N '' -f other
cp client.pub /userdata/dropbear/.ssh/authorized_keys
chmod 600 /userdata/dropbear/.ssh/authorized_keys
qemu-arm -0 dropbearkey "$db" -t ed25519 -f host >generated
awk '/^ssh-ed25519 /{print "[127.0.0.1]:2222", $1, $2}' generated >known_hosts
qemu-arm -0 dropbear "$db" -F -E -r host -P /run/dropbear.pid -p 127.0.0.1:2222 >server.log 2>&1 &
daemon=$!
trap 'ret=$?; if [ "$ret" -ne 0 ]; then cat server.log >&2; fi; kill "$daemon" 2>/dev/null || true' EXIT
for tick in $(seq 1 50); do
    [ -s /run/dropbear.pid ] && break
    kill -0 "$daemon" || { cat server.log; exit 1; }
    sleep 0.1
done
connect() {
    ssh -F /dev/null -p 2222 -o ConnectTimeout=4 -o IdentitiesOnly=yes -o BatchMode=yes \
        -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/tmp/known_hosts "$@" root@127.0.0.1
}
if connect -i other -N >wrong.log 2>&1; then
    echo "unregistered key was accepted" >&2; exit 1
fi
grep -q "Permission denied (publickey)" wrong.log
connect -i client -M -S /run/proof.sock -fN
ssh -F /dev/null -S /run/proof.sock -O check root@127.0.0.1
ssh -F /dev/null -S /run/proof.sock -O exit root@127.0.0.1
printf '%s\n' 'QEMU ARM Dropbear accepted only the registered disposable key.'
TEST
