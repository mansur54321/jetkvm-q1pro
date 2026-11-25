#!/usr/bin/env bash
# Offline Linux dependencies: ffmpeg with libx264, Chromium with H.264, bwrap,
# iproute2, Python 3 and a native Go/C toolchain with cached Go modules.
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
src="$root/tools/tests/webrtc/testdata"
case ${1:-} in
  "") recovery=0;;
  --clock-recovery) recovery=1;;
  *) echo "usage: $0 [--clock-recovery]" >&2; exit 2;;
esac
work=$(mktemp -d /tmp/q1pro-webrtc.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT

cp "$src/receiver.html" "$work/"
mkfifo "$work/pcm"
cat > "$work/asound.conf" <<EOF
pcm.hw {
    @args [ CARD DEV ]
    @args.CARD { type string }
    @args.DEV { type integer }
    type file
    slave.pcm { type null }
    file "/dev/null"
    infile "$work/pcm"
    format raw
}
pcm.default { type null }
EOF
ffmpeg -nostdin -hide_banner -loglevel error -f lavfi -i testsrc2=size=320x240:rate=15 \
  -frames:v 45 -c:v libx264 -threads 2 -pix_fmt yuv420p -profile:v baseline -level:v 3.1 \
  -tune zerolatency -preset ultrafast -x264-params 'aud=1:repeat-headers=1:keyint=15:scenecut=0' \
  -f h264 "$work/test.h264"
if [ "$recovery" = 1 ]; then
  cc -std=gnu11 -fPIC -shared -Wall -Wextra -Werror -O2 "$src/clock_fixture.c" \
    -Wl,--no-as-needed -lasound -ldl -o "$work/libq1clocktest.so"
  cp "$src/clock_source.py" "$work/"
fi
python3 - "$root" "$work" "$recovery" > "$work/overlay.json" <<'PYTHON'
import json,sys
from pathlib import Path
root, work = map(Path,sys.argv[1:3])
overlay = {str(root/"q1_browser_overlay_test.go"): str(root/"tools/tests/webrtc/testdata/browser_test.go")}
if sys.argv[3] == "1":
    src=root/"internal/audio/alsa_capture_cgo.go"
    target=work/"alsa_capture_cgo.go"
    text=src.read_text()
    original='dlopen("libasound.so.2"'
    assert text.count(original)==1
    target.write_text(text.replace(original,f'dlopen("{work}/libq1clocktest.so"'))
    overlay[str(src)]=str(target)
print(json.dumps({"Replace":overlay}))
PYTHON
(cd "$root" && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=1 \
  go test -overlay "$work/overlay.json" -vet=off -c -o "$work/jetkvm.test" .)

mkdir -m 700 "$work/home" "$work/runtime"
# shellcheck disable=SC2016  # Variables in the quoted script belong to the guest.
bwrap --unshare-user --uid 0 --gid 0 --unshare-pid --unshare-net --die-with-parent --cap-add CAP_NET_ADMIN \
  --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --bind "$work" "$work" \
  --chdir "$work" --clearenv --setenv PATH "$PATH" --setenv HOME "$work/home" \
  --setenv Q1_AUDIO_RECOVERY "$recovery" --setenv ALSA_CONFIG_PATH "$work/asound.conf" --setenv XDG_RUNTIME_DIR "$work/runtime" --setenv Q1_WEB_TEST "$work" -- /bin/sh -ec '
    # Pion requires a non-loopback host candidate. Both peers share this namespace.
    ip link add q1rtc0 type dummy
    ip addr add 192.0.2.1/32 dev q1rtc0
    ip link set q1rtc0 up
    if [ "$Q1_AUDIO_RECOVERY" = 1 ]; then
      python3 -B "$Q1_WEB_TEST/clock_source.py" &
    else
      ffmpeg -nostdin -hide_banner -loglevel error -re -f lavfi -i sine=frequency=997:sample_rate=48000 \
        -ac 2 -f s16le -y "$Q1_WEB_TEST/pcm" 2>"$Q1_WEB_TEST/tone.log" &
    fi
    exec "$Q1_WEB_TEST/jetkvm.test" -test.run "^TestQ1LocalSyntheticWebRTCInChromium$" -test.v -test.timeout 45s
  '
