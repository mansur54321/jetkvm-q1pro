# Device nodes on kernels without devtmpfs

The stock kernel disables devtmpfs. The startup script mounts a writable tmpfs on `/dev` and starts BusyBox mdev. The mdev daemon subscribes to add/remove events before the first scan. After loading its board modules, `rcS` scans again before starting JetKVM.

## ARM QEMU test

Set `CROSS_COMPILE` to the full ARM toolchain prefix. Build a test kernel in a separate output directory from a compatible RV1106 BSP and the factory config.

~~~sh
tools/q1pro-image/test-devices/build-kernel.sh \
    /path/to/bsp/sysdrv/source/kernel /path/to/stock.config /tmp/q1-systest
tools/q1pro-image/test-devices/run.sh \
    bin/q1pro-image/q1pro-rootfs.squashfs /tmp/q1-systest /tmp/q1-mdev.log
~~~

The guest checks initial device numbers, permissions and PTY access. It inserts and removes a dummy ALSA PCM, tests nested V4L2, `mpi/` and `bsg/` node names, then binds and unbinds a virtual USB HID gadget twice. Those virtual devices exercise mdev and its event ordering. They simulate neither the Rockchip video pipeline nor the real USB transport.
