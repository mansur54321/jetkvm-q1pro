# Signed Q1 Pro software-volume maintenance

The static ARMv7 helper validates and writes signed software packages for the XRK-Q1Pro factory partition layout. Use the [RAM handoff](HANDOFF.md) before replacing the rootfs of a running JetKVM system.

## Build and sign locally

~~~sh
mkdir -p bin/q1pro-maintenance
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' \
    -o bin/q1pro-maintenance/q1pro-maintenance-armv7 ./tools/q1pro-maintenance
python3 -B tools/q1pro-image/package.py pack bin/q1pro-image "$SIGNING_KEY" "$UPDATE_PACKAGE"
python3 -B tools/q1pro-image/package.py pack-stock "$STOCK_MTD6" "$SIGNING_KEY" "$FACTORY_PACKAGE"
python3 -B tools/q1pro-image/package.py verify "$UPDATE_PACKAGE" "$TRUSTED_PUBLIC_KEY"
~~~

Create your own Ed25519 key. Keep `SIGNING_KEY` and `STOCK_MTD6` private. The updater reads the trusted public key from a separate file. The stock package builder extracts only the known software-volume payload from your local MTD6 backup.

| Command | Operation |
| --- | --- |
| `verify PACKAGE PUBLIC_KEY` | Checks signature, board and kernel identifiers, UBI volume identity, package bounds, superblock and content digest |
| `check` | Checks the board, booted root, MTD and UBI layout |
| `backup NEW_FILE` | Reads the entire 26,157,056-byte software volume and reports SHA-256 |
| `install PACKAGE PUBLIC_KEY` | Writes software volume 0 from a compatible RAM root and verifies the result by reading it back |

The install digest covers the signed image length. A raw volume backup covers the full volume.

## Virtual UBI validation

~~~sh
go test -race ./tools/q1pro-maintenance
python3 -B tools/q1pro-maintenance/test-guest.py \
    --kernel "$QEMU_Q1_KERNEL" --stock-mtd6 "$STOCK_MTD6" --release bin/q1pro-image
~~~

The ARM guest uses a disk image and a QEMU test kernel. See the [virtual device-node test](../q1pro-image/test-devices/README.md) for the kernel and dependencies.
