#!/bin/sh
# Use the SDK tree prepared by ../build-sdk-video.sh; no factory modules are reused.
set -eu
[ "$#" = 5 ] || { echo 'usage: build-kernel.sh PREPARED_SDK_KERNEL SDK_MEDIA_CONFIG AIC_DRIVER_TREE REGDB_CERT_DIR NEW_WORK' >&2; exit 2; }
src=$(realpath "$1"); base=$(realpath "$2"); aic=$(realpath "$3"); keys=$(realpath "$4")
mkdir -- "$5"; dest=$(realpath "$5"); obj="$dest/kernel"
: "${CROSS_COMPILE:?set buildkit ARM toolchain prefix}"
test -f "$src/drivers/rockit-ko/mcu/mcu.S"
test -f "$src/drivers/kmpp/mpp/mpp_service.S"
test -f "$keys/wens.x509"
mkdir -p "$obj" "$dest/aic"
cp "$base" "$obj/.config"
"$src/scripts/config" --file "$obj/.config" --set-str LOCALVERSION -q1-media-ram \
    -d MTD -d MMC_BLOCK -d PSTORE -d WATCHDOG -d IP_PNP -d USB_DUMMY_HCD -d SCSI -d USB_STORAGE -d BLK_DEV_SD -d BLK_DEV_SR -d CHR_DEV_SG \
    -e TUN -e WIREGUARD -e WLAN -e RFKILL -m CFG80211 -e CFG80211_CERTIFICATION_ONUS -e CFG80211_REQUIRE_SIGNED_REGDB -e CFG80211_USE_KERNEL_REGDB_KEYS \
    --set-str CFG80211_EXTRA_REGDB_KEYDIR "$keys"
kmake() { make -s -C "$src" O="$obj" ARCH=arm CROSS_COMPILE="$CROSS_COMPILE" CC="${CROSS_COMPILE}gcc" LOCALVERSION= "$@"; }
kmake olddefconfig
for flag in MTD MMC_BLOCK PSTORE WATCHDOG; do grep -Fqx "# CONFIG_$flag is not set" "$obj/.config"; done
for flag in ROCKCHIP_THUNDER_BOOT VIDEO_LT7911D BLK_DEV_NBD BLK_DEV_LOOP RD_GZIP SQUASHFS_XZ USB_F_FS USB_F_HID USB_F_MASS_STORAGE; do grep -Fqx "CONFIG_$flag=y" "$obj/.config"; done
kmake -j"${JOBS:-6}" zImage modules
rsync -a --exclude='*.ko' --exclude='*.o' --exclude='*.mod*' --exclude='.*.cmd' --exclude=Module.symvers "$aic/" "$dest/aic/"
kmake -j"${JOBS:-6}" M="$dest/aic" CONFIG_PLATFORM_UBUNTU=n CONFIG_PLATFORM_ROCKCHIP=n CONFIG_PLATFORM_ROCKCHIP2=n CONFIG_USE_FW_REQUEST=y modules
