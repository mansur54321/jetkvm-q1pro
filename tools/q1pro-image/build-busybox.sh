#!/bin/sh
# Build the PID 1 and command-line utilities for the Q1 Pro rootfs.
set -eu
: "${BUILDKIT_PATH:=/opt/jetkvm-native-buildkit}"
archive=${1:?path to busybox-1.37.0.tar.bz2}
out=${2:?output binary path}
archive=$(realpath "$archive")
out=$(realpath -m "$out")
printf '%s  %s\n' 3311dff32e746499f4df0d5df04d7eb396382d7e108bb9250e7b519b837043a4 "$archive" | sha256sum -c -
build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
tar -xf "$archive" -C "$build"
cd "$build/busybox-1.37.0"
export SOURCE_DATE_EPOCH=1
prefix=$BUILDKIT_PATH/bin/arm-rockchip830-linux-uclibcgnueabihf-
make HOSTCFLAGS=-std=gnu17 allnoconfig >/dev/null
# Only these applets are used for boot, network setup and maintenance.
for opt in BUSYBOX KILL DATE FEATURE_DATE_ISOFMT PRINTF FEATURE_IPV6 LSMOD HEAD TAIL DMESG UPTIME FIND FEATURE_FIND_TYPE FEATURE_FIND_MAXDEPTH WC SORT CUT AWK PS DF FREE PING FEATURE_FANCY_PING FLOCK START_STOP_DAEMON FEATURE_START_STOP_DAEMON_FANCY RM MV SHOW_USAGE STATIC INIT GREP TR FEATURE_USE_INITTAB FEATURE_KILL_REMOVED ASH ASH_INTERNAL_GLOB ASH_TEST SH_IS_ASH TEST ECHO CAT LS LN MKDIR MOUNT FEATURE_MOUNT_FLAGS UMOUNT INSMOD FEATURE_CMDLINE_MODULE_OPTIONS IP FEATURE_IP_LINK FEATURE_IP_ADDRESS FEATURE_IP_ROUTE REBOOT HALT POWEROFF SYNC SLEEP UNAME HOSTNAME CP CHMOD READLINK FEATURE_READLINK_FOLLOW MDEV FEATURE_MDEV_CONF FEATURE_MDEV_DAEMON; do
    sed -i "s/^# CONFIG_${opt} is not set$/CONFIG_${opt}=y/" .config
done
sed -i 's/^CONFIG_SH_IS_NONE=y/# CONFIG_SH_IS_NONE is not set/' .config
sed -i 's/^CONFIG_EXTRA_LDFLAGS=""/CONFIG_EXTRA_LDFLAGS="-Wl,-z,noexecstack"/' .config
make HOSTCFLAGS=-std=gnu17 CROSS_COMPILE="$prefix" oldconfig </dev/null >/dev/null
grep -qx CONFIG_FEATURE_CMDLINE_MODULE_OPTIONS=y .config
grep -qx CONFIG_FEATURE_MDEV_DAEMON=y .config
make -j4 HOSTCFLAGS=-std=gnu17 CROSS_COMPILE="$prefix" SKIP_STRIP=n >/dev/null
readelf -lW busybox | awk '$1 == "GNU_STACK" && $(NF-1) == "RW" { found=1 } END { exit !found }'
install -m 0755 busybox "$out"
