# Q1 Pro video capture probe

The ARM probe uses the production V4L2, DMA and Rockit VENC path. Build it with the JetKVM ARM buildkit from the repository root.

~~~sh
export BUILDKIT_PATH="$HOME/.cache/jetkvm-q1pro/buildkit-v0.2.8"
tools/q1pro-capture/build.sh
tools/q1pro-capture/test.sh
~~~

Keep `q1pro-capture` and `q1pro-video.so` together in `bin/q1pro-capture/`. On a separate test board, run `q1pro-capture --check` first. That command reads the source format from `/dev/v4l-subdev2`. Exit 3 indicates an active capture process. Exit 4 indicates a missing source format.

Use an idle video input with a stable video signal. `q1pro-capture --capture` writes up to 30 H.264 Annex B units to stdout, with diagnostics on stderr. Redirect stdout on a workstation when capturing over SSH and use an SSH session without a PTY.

~~~sh
ffprobe -v error -f h264 -count_frames -select_streams v:0 \
  -show_entries stream=codec_name,width,height,nb_read_frames -of json clip.h264
ffmpeg -v error -xerror -f h264 -i clip.h264 -f null -
~~~

The factory system exposed `rkcif_tools_id0` at `/dev/video8`. Check the V4L2 node name before choosing a device for the optional tools-node probe. Capture and encoder experiments can interrupt the live video pipeline. [Production video path](../Q1-Pro.md#hardware-paths)
