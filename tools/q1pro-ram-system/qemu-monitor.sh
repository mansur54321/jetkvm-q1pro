#!/qemu/busybox sh
printed=
while :; do
    /qemu/busybox awk '/^MemTotal:/ {total=$2} /^MemAvailable:/ {print "Q1_MEMORY total=" total " available=" $2}' /qemu-proc/meminfo
    if [ -z "$printed" ] && /qemu/busybox grep -q ' /userdata ' /qemu-proc/1/mountinfo; then
        /qemu/busybox awk '$5 == "/" || $5 == "/mnt/data" || $5 == "/userdata" {print "Q1_FS " $0}' /qemu-proc/1/mountinfo
        printed=yes
    fi
    /qemu/busybox sleep 1
done
