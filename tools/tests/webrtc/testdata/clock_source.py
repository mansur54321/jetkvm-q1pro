"""Feed the real ALSA file plugin a timed stereo signal before and after a stopped clock."""

from array import array
import math
import os
import sys
from pathlib import Path
import time

root = Path(os.environ["Q1_WEB_TEST"])
# Keeping the FIFO open lets the real PCM close and reopen during a clock loss.
fd = os.open(root / "pcm", os.O_RDWR | os.O_NONBLOCK)


def tone(hz):
    frame = array("h")
    for i in range(48000):
        sample = int(9000 * math.sin(2 * math.pi * hz * i / 48000))
        frame.extend((sample, sample))
    if sys.byteorder != "little":
        frame.byteswap()
    return frame.tobytes()


initial, resumed = tone(997), tone(1511)
start = None
offset = 0
deadline = time.monotonic()
while True:
    now = time.monotonic()
    if start is None and (root / "pause-clock").exists():
        start = now
    elapsed = now - start if start is not None else -1
    # Finish any read already in flight when the fixture starts withholding periods.
    if not (0.12 < elapsed < 6):
        samples = initial if elapsed < 6 else resumed
        try:
            if os.write(fd, samples[offset : offset + 3840]) == 3840:
                offset = (offset + 3840) % len(samples)
        except BlockingIOError:
            pass
    deadline = max(deadline + 0.020, now)
    time.sleep(max(0, deadline - time.monotonic()))
