# Appended only to the QEMU test copy of rcS after the original RAM initialization.
q1_maintenance_test() {
    grep -qw q1maintenance_test=1 /proc/cmdline || return 1
    [ "$(cat /sys/block/vda/size)" = 169984 ] && [ "$(cat /sys/block/vda/ro)" = 0 ] || return 1
    [ "$(cat /sys/block/vdb/ro)" = 1 ] || return 1
    mkdir -p /run/q1-maintenance-fixtures || return 1
    mount -t ext4 -o ro /dev/vdb /run/q1-maintenance-fixtures || return 1
    cd /run/q1-maintenance-fixtures || return 1
    ./guest.test -test.run='^TestVirtualUBIMaintenance$' -test.v -test.timeout=3m
}
if q1_maintenance_test; then echo Q1_MAINTENANCE_GUEST_PASS; else echo Q1_MAINTENANCE_GUEST_FAIL; fi
sync
poweroff -f
