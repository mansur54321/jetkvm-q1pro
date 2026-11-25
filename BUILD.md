# Building the Q1 Pro port

The application includes the JetKVM web client and the native RV1106 video code. The board still runs its original kernel and supplies its original drivers. Building an application and assembling an installation image are separate steps.

## Build the ARM application

Use an x86-64 Linux machine with Go, Node.js 22, npm, GNU Make, a C/C++ compiler, CMake, Git, patch, gzip, Python 3, and Bubblewrap. The module and npm lockfiles pin the application dependencies. The build has been exercised with Go 1.27.4 and Node.js 22.21.1.

From the repository root, prepare the cross-compiler and the root filesystem utilities:

```sh
tools/q1pro-install/prepare-buildkit.sh ./q1-build-inputs
```

Build the web client and compress the embedded assets:

```sh
npm --prefix ui ci
npm --prefix ui run build:device
find static -type f \( -name '*.js' -o -name '*.css' -o -name '*.svg' \) -exec gzip -n -9 -f '{}' +
```

The upstream SDK's CMake toolchain uses `/opt/jetkvm-native-buildkit`. Bubblewrap supplies that directory for the build process. Run it on a Linux host with unprivileged user namespaces enabled.

```sh
bwrap --dev-bind / / --tmpfs /opt \
  --ro-bind "$PWD/q1-build-inputs/sdk" /opt/jetkvm-native-buildkit \
  --chdir "$PWD" \
  make _build_dev_inner BUILDKIT_PATH=/opt/jetkvm-native-buildkit VERSION_DEV=0.5.11-q1pro.4
file bin/jetkvm_app
```

The output should be a Linux ARM32 executable. Give it to the installer as `--app bin/jetkvm_app`. To build the same source as a release, check out its tag. The build embeds the revision and build date. [Install guide](INSTALL.md)

## Test the source

After building the web client, run the Go and browser-independent checks:

```sh
go test ./...
go test -race ./internal/q1vpn ./internal/q1wifi ./tools/q1pro-ble ./tools/q1pro-maintenance
node ui/tests/signaling-lifecycle.mjs
```

Run the native video lifecycle test if the compiled app or video driver integration changes:

```sh
BUILDKIT_PATH="$PWD/q1-build-inputs/sdk" tools/tests/test-video-lifecycle.sh
```

The installer writes a local manifest containing the firmware checksum, software volume checksum, and application revision. Keep it with your original backups. The local test harness exercises the factory validator without writing to the device. [Validator setup](tools/q1pro-sysupgrade/README.md)

## Code and build inputs

The port is based on JetKVM commit `4d9b45d91a1be423c872eaef29a34bd4d6c70c7b`. Follow the repository license and the notices shipped with its dependencies. The buildkit is JetKVM's RV1106 SDK release `v0.2.8`. The native library builds against LVGL `v9.3.0`. The dependency preparation script verifies the exact archives for BusyBox, Dropbear, libnl, wpa_supplicant and OpenSSL before compiling them locally.

The generated installation image contains material taken from the owner's factory backup. Keep that image, the factory rollback package, and any maintenance signing key private. [JetKVM source](https://github.com/jetkvm/kvm) · [RV1106 SDK release](https://github.com/jetkvm/rv1106-system/releases/tag/release/v0.2.8) · [LVGL source](https://github.com/lvgl/lvgl/tree/v9.3.0)
