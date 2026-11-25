#!/bin/sh
# Stage the exact upstream ARM archive accepted by the Q1 settings page.
set -eu
version=1.104.7
name=tailscale_${version}_arm.tgz
dst=${1:-bin/q1pro-tailscale}
mkdir -p "$dst"
work=$(mktemp -d "$dst/.download-XXXXXX")
trap 'rm -rf "$work"' EXIT
if [ "$#" -ge 2 ]; then
    cp "$2" "$work/$name"
else
    curl -fLsS --retry 2 "https://pkgs.tailscale.com/stable/$name" -o "$work/$name"
fi
(cd "$work" && printf '%s  %s\n' 1c753fef45d497491081db37552b7a2a42c7932292743bf6936cb1a42b6bceb0 "$name" | sha256sum -c -)
tar xOzf "$work/$name" "tailscale_${version}_arm/tailscaled" >"$work/tailscaled"
printf '%s  %s\n' 7b6e407353575cb4dcd1d95da7a1b197c9e6c0597b9570007d7564d2929a7ae4 "$work/tailscaled" | sha256sum -c -
chmod 0700 "$work/tailscaled"
test "$(qemu-arm "$work/tailscaled" --version | head -n 1)" = "$version"
if [ -e "$dst/$name" ]; then cmp "$work/$name" "$dst/$name"; else mv "$work/$name" "$dst/$name"; fi
echo "$dst/$name"
