#!/bin/sh
# Stage the SDK's own Thunder Boot Rockit/KMPP build in a new directory.
set -eu
[ "$#" = 3 ] || { echo 'usage: build-sdk-video.sh COMPLETE_RV1106_SDK STOCK_CONFIG NEW_WORK_DIRECTORY' >&2; exit 2; }
sdk=$(realpath "$1")
stock=$(realpath "$2")
mkdir -- "$3"
work=$(realpath "$3")
here=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
target="$work/sysdrv"
mkdir -p "$target/source" "$target/drv_ko/rockit" "$target/drv_ko/kmpp"
cp -a --reflink=auto "$sdk/sysdrv/source/kernel" "$target/source/"
cp "$sdk/sysdrv/Makefile.param" "$target/"
cp -a "$sdk/sysdrv/cfg" "$target/"
cp -a "$sdk/sysdrv/drv_ko/rockit/release_rockit-ko_rv1106_arm_asm" "$sdk/sysdrv/drv_ko/rockit/fakecode.c" "$target/drv_ko/rockit/"
cp -a "$sdk/sysdrv/drv_ko/kmpp/release_kmpp_rv1106_arm_asm" "$target/drv_ko/kmpp/"
cat > "$work/sdk.mk" <<'MAKE'
include $(SYSDRV_DIR)/Makefile.param
all:
	$(call MACRO_CHK_DRIVER_MODULE,$(SYSDRV_DIR)/drv_ko/rockit/,rockit-ko)
	$(call MACRO_CHK_DRIVER_MODULE,$(SYSDRV_DIR)/drv_ko/kmpp/,kmpp)
MAKE
: "${CROSS_COMPILE:?Q1 ARM toolchain prefix required}"
PATH="$(dirname "$CROSS_COMPILE"):$PATH" make -s -f "$work/sdk.mk" SYSDRV_DIR="$target" KERNEL_DIR="$target/source/kernel" ARCH=arm CHIP=rv1106 SYSDRV_CROSS=arm-rockchip830-linux-uclibcgnueabihf all
cp "$stock" "$work/media.config"
"$target/source/kernel/scripts/config" --file "$work/media.config" -e ROCKCHIP_THUNDER_BOOT -d ROCKCHIP_THUNDER_BOOT_SERVICE \
    -e VIDEO_ROCKCHIP_CIF -e VIDEO_ROCKCHIP_ISP -e ROCKCHIP_DVBM -e ROCKCHIP_MULTI_RGA -e MEDIA_CEC_SUPPORT -m CFG80211
exec "$here/build-kernel.sh" "$target/source/kernel" "$work/media.config" "$work/build"
