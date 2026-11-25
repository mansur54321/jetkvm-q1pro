# Tailscale on Q1 Pro

Open **Settings → Network**, upload the pinned ARM archive, select **Enable Tailscale** and follow the displayed sign-in link. Reach the Q1 Pro by its Tailscale IPv4 address or MagicDNS name. For Headscale, enter its control server before enabling. [Official ARM archive](https://pkgs.tailscale.com/stable/tailscale_1.104.7_arm.tgz) · [Publisher checksum](https://pkgs.tailscale.com/stable/tailscale_1.104.7_arm.tgz.sha256)

Tailscale is an optional add-on. Allow at least 37 MiB of free user-volume space. The device verifies the uploaded archive and its extracted daemon against the pinned release hashes. It stores its identity under `/userdata/jetkvm/tailscale/state/`. Disabling the add-on stops the process while keeping its identity for the next connection.

The daemon uses userspace networking and a local socket. The browser connection uses the Tailscale IPv4 address for its WebRTC ICE path. [Tailscale userspace mode](https://tailscale.com/docs/concepts/userspace-networking)

~~~sh
tools/q1pro-tailscale/prepare.sh
go test -race ./internal/q1tailscale ./internal/tailscale
python3 -B tools/q1pro-tailscale/test.py /path/to/q1pro-rootfs.squashfs \
  bin/q1pro-tailscale/tailscale_1.104.7_arm.tgz
~~~

The ARM test adds Chromium, Playwright, FFmpeg with libvpx support and locally installed Tailscale tools to the [QEMU setup](../q1pro-system-test/README.md). It runs a private control server and relay, installs the add-on from the browser, connects through the tailnet, checks ICE/RPC and decodes synthetic video.
