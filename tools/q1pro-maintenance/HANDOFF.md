# Update an installed Q1 Pro without recreating the user volume

This procedure updates a running compatible JetKVM installation. It writes UBI volume 0, leaving the user volume in place. Back up your settings separately before proceeding. A power failure during the write can make factory recovery necessary.

## Prepare

From a root shell on the Q1 Pro, stage the static ARM `q1-handoff` helper, a signed new release, the signed currently installed release, and your trusted public key in a private directory on tmpfs. See the [maintenance tools](README.md) for signing and compilation.

~~~sh
./q1-handoff handoff-prepare new-release.tar current-release.tar release-public.pem
~~~

The helper verifies the signatures and compares the supplied current release with the installed volume. It also checks the board, kernel, read-only UBI root, BusyBox PID 1 and storage geometry. It stages a disposable RAM root and prints `PREPARED` and the new digest. Network and KVM remain available through this stage. To stop before the write, run `./q1-handoff handoff-discard`.

## Activate

~~~sh
./q1-handoff handoff-activate NEW_RELEASE_SHA256
~~~

Use the exact new digest printed during preparation. Activation stops SSH and the KVM. BusyBox transfers PID 1 to the RAM helper, which holds the board watchdog open, unmounts the old volumes, writes software volume 0, verifies the result and reboots. Wait for the new system to return in the browser.

The helper attempts to restore the prior signed volume if the write or verification fails while the RAM environment is live. A later boot failure may require a factory recovery. [Recovery notes](../q1pro-image-recovery.md)

## ARM virtual test

Build a QEMU-compatible test kernel with `build-handoff-test-kernel.sh`. Its SFOO fixture reads the same bytes that the UBI update wrote.

~~~sh
python3 -B tools/q1pro-maintenance/test-handoff.py \
    --kernel /path/to/qemu/build/arch/arm/boot/zImage \
    --r3 /path/to/r3-build --r4 /path/to/r4-build \
    --update /path/to/r4.tar --previous /path/to/r3.tar \
    --key /path/to/release-public.pem
~~~

The test stresses watchdog keepalive, interrupted writes, signed rollback and persistence of a synthetic user volume. A run with the standard firmware versions also verifies that a local login persists.
