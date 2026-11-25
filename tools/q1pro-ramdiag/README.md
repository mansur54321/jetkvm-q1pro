# Diagnostic RAM boot for the Q1 Pro

The diagnostic kernel and initramfs are build tools for an ARM laboratory environment. They exercise board drivers and expose device nodes without a persistent filesystem. Physical boot with this bundle remains unverified.

## Build

Use the RV1106 BSP sources, the known factory kernel configuration and the ARM buildkit. Set `CROSS_COMPILE` to the toolchain prefix. On a system with a newer OpenSSL ABI, the host kernel tool build may need a compatible OpenSSL pkg-config path.

~~~sh
tools/q1pro-ramdiag/build.sh /path/to/bsp/kernel /path/to/stock.config \
    /tmp/q1-ramdiag-build bin/q1pro-ramdiag
(cd bin/q1pro-ramdiag && sha256sum -c SHA256SUMS)
~~~

## Exercise the ARM guest

~~~sh
out=bin/q1pro-ramdiag
python3 tools/q1pro-ramdiag/test-guest.py \
    "$out/zImage" "$out/initramfs.cpio.gz" "$out/qemu-bsp.log"
python3 tools/q1pro-ramdiag/test-guest.py --generic-kernel \
    /path/to/vmlinuz-virt "$out/initramfs.cpio.gz" "$out/qemu-generic.log"
~~~

`test-media.sh` accepts the diagnostic output directory, a matching standard Q1 Pro rootfs and an ARM Go test executable built with the Rockchip libraries. Virtual tests reproduce selected driver contracts. Physical video capture, radio operation, boot and recovery require the board.
