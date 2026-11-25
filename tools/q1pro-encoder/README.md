# Q1 Pro H.264 encoder smoke test

This test targets the public Rockchip MPP packet API. For that ABI it submits eight moving 320 × 240 YUYV frames to its own encoder context and writes the H.264 Annex B stream to stdout. Diagnostics go to stderr and a watchdog bounds the run to 15 seconds.

Q1 Pro firmware 1.0.0_87160a ships an extended `/dev/vcodec` API. Its 132-byte `MppApi` includes a packet release function and expects a caller-owned output stream descriptor. With this firmware the test exits before opening the encoder. JetKVM buildkit v0.2.8 provides the extended headers and its `librockit.so` matches the stock library byte for byte (SHA-256 `3f92f8c41ffe9ad72e407b68750906fcff89ea06758f14a3fc2a3d87061e3d0f`). This makes Rockit a candidate for a separate, coordinated test when the stock encoder is idle.

Rockchip's MPI guide for RV1106/Linux 5.10 describes a single-process SYS lifetime and documents inter-process sharing for the MB memory module. The SDK's multi-channel encoder example runs its VENC channels under the same SYS instance. A second VENC channel number therefore does not establish that an unrelated process can safely encode alongside the active factory service. The Q1's 1080p capture showed no available CMA in the latest measurement. [Rockchip MPI guide, §4.1](https://cdn.static.spotpear.cn/uploads/picture/learn/risc-v/luck-fox-pico/Document/Rockchip_Developer_Guide_MPI_FAQ.pdf#page=16) · [SDK encoder example](https://github.com/jetkvm/rv1106-system/blob/ccd1c03732055518f85d7ccb1d3200056aad21ff/media/rockit/rockit/mpi/example/mod/test_mpi_venc.cpp#L608)

Build on the host with the RV11xx ARM hard-float uClibc toolchain, the headers from [Rockchip MPP](https://github.com/rockchip-linux/mpp/tree/9b56348cc61393c84662e6d6dd78feae67773a36/inc) and the actual librockchip_mpp ELF from the device firmware. Set the three paths for your checkout.

```sh
"$CC" -std=c11 -Os -Wall -Wextra -Werror -I"$MPP_INCLUDE" \
    tools/q1pro-encoder/main.c "$MPP_LIBRARY" -o q1pro-encoder
```

On stock firmware 1.0.0_87160a, the extracted file is named librockchip_mpp.so.0 and its SONAME is librockchip_mpp.so.1. Make it available under its SONAME on the runtime library path. Verify the captured file on the host.

```sh
ffprobe -v error -count_frames -select_streams v:0 \
    -show_entries stream=codec_name,width,height,nb_read_frames synthetic.h264
ffmpeg -v error -xerror -f h264 -i synthetic.h264 -f framemd5 -
```

Expect H.264, 320 × 240, eight decoded frames with changing checksums.
