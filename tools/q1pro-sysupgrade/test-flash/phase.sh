#!/bin/sh
# The sourced OpenWrt libraries and expected hashes are provided inside the guest.
# shellcheck disable=SC1091,SC2154
export PATH=/bin:/sbin:/usr/bin:/usr/sbin
phase=$(sed -n 's/.*q1stage=\([^ ]*\).*/\1/p' /proc/cmdline)
fail(){ echo "Q1_E2E FAIL $1"; exit 1; }
mkdir -p /tmp/sysinfo /tmp/state /tmp/lock
. /lib/functions.sh
. /lib/functions/preinit.sh
. /lib/preinit/02_sysinfo
do_sysinfo_generic || fail preinit
[ "$(cat /tmp/sysinfo/board_name)" = oraybox,q1pro ] || fail board
. /lib/upgrade/common.sh
. /lib/upgrade/nand.sh
. /lib/upgrade/platform.sh
[ "$(rk_get_boot_media)" = mtd ] || fail boot-media
[ "$(find_mtd_index boot)" = 0 ] && [ "$(find_mtd_index rootfs)" = 1 ] || fail storage-label
[ "$(cat /sys/class/ubi/ubi0_0/name)" = rootfs ] && [ "$(cat /sys/class/ubi/ubi0_1/name)" = rootfs_data ] || fail volumes
[ "$(cat /sys/class/ubi/ubi0_0/usable_eb_size)" = 126976 ] || fail geometry
readhash(){ sha256sum "$1" | cut -d' ' -f1; }
expect(){ [ "$1" = "$2" ] || fail "$3:$1"; }
check_image(){
    /usr/libexec/validate_firmware_image "$1" >/tmp/validation || fail validator
    jsonfilter -i /tmp/validation -e '@.valid' | grep -qx true || fail validation-status
    jsonfilter -i /tmp/validation -e '@.tests.fwtool_device_match' | grep -qx true || fail device-match
    /sbin/sysupgrade -T "$1" || fail sysupgrade-T
    echo "Q1_E2E $phase: factory validate_firmware_image and sysupgrade -T accepted"
}
. /tmp/tests/expected
boot=$(readhash /dev/mtd0) || fail readboot
soft=$(readhash /dev/ubi0_0) || fail readroot
user=$(readhash /dev/ubi0_1) || fail readuser
n=$(cat /sys/class/ubi/ubi0_1/reserved_ebs)
io=$(cat /sys/class/mtd/mtd1/writesize)
echo "Q1_E2E stage=$phase kernel=$(uname -r) mtd=boot:0,rootfs:1 writesize=$io root_lebs=$(cat /sys/class/ubi/ubi0_0/reserved_ebs) data_lebs=$n boot_sha=$boot root_sha=$soft data_sha=$user"
case "$phase" in
 r3)
    expect "$boot" "$canary_boot" initial-boot
    expect "$soft" "$factory_root" initial-root
    mount -t ubifs -o ro ubi0:rootfs_data /tmp/state || fail initial-ubifs
    [ "$(cat /tmp/state/factory-sentinel)" = "factory user data" ] || fail initial-sentinel
    umount /tmp/state || fail unmount-initial
    check_image /tmp/tests/r3.bin
    echo "Q1_E2E r3: factory data sentinel verified; invoking unchanged platform_do_upgrade"
    platform_do_upgrade /tmp/tests/r3.bin
    fail returned-r3
    ;;
 factory|verify)
    expect "$boot" "$full_boot" full-factory-boot-partition
    expect "$(cat /sys/class/ubi/ubi0_0/reserved_ebs)" 206 root-lebs
    if [ "$phase" = factory ]; then expect "$soft" "$r3_root" r3-root; else expect "$soft" "$factory_root" restored-root; fi
    mount -t ubifs ubi0:rootfs_data /tmp/state || fail recreated-ubifs
    [ ! -e /tmp/state/factory-sentinel ] && [ ! -e /tmp/state/r3-sentinel ] || fail old-sentinel-present
    echo "Q1_E2E $phase: full boot partition matches factory, software matches expected, no old userdata sentinel"
    if [ "$phase" = factory ]; then
       echo "test state created under R3" >/tmp/state/r3-sentinel || fail second-sentinel
       sync
       umount /tmp/state || fail unmount-second
       mount -t ubifs -o ro ubi0:rootfs_data /tmp/state || fail remount-second
       [ "$(cat /tmp/state/r3-sentinel)" = "test state created under R3" ] || fail read-second-sentinel
       umount /tmp/state || fail unmount-read-second
       check_image /tmp/tests/factory.bin
       echo "Q1_E2E factory: fresh second sentinel verified; invoking unchanged platform_do_upgrade"
       platform_do_upgrade /tmp/tests/factory.bin
       fail returned-factory
    fi
    umount /tmp/state
    sync
    echo 'Q1_E2E_VERIFY_PASS'
    /sbin/poweroff -f
    ;;
 *) fail unknown-phase ;;
esac
