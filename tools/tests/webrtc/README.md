# Local WebRTC regression tests

Run `tools/tests/webrtc/run.sh` from the repository root. The test starts JetKVM and Chromium in an isolated network namespace. It feeds moving H.264 video and a 48 kHz stereo signal from an ALSA file PCM. Chromium checks decoded frames and measures the G.722 tone with Web Audio.

For the audio clock test, run `tools/tests/webrtc/run.sh --clock-recovery`. The source sends 997 Hz, pauses for six seconds, then sends 1511 Hz. A test-only ALSA shim generates a capture wait timeout and one `-EPIPE`. Opening the PCM, recovery, the PREPARED-to-RUNNING transition and frame reads use the system ALSA library.

The browser must receive both tones and observe an RTP gap of at least 4.5 seconds in one session. Tests use a clocked silent output and isolated browser state. Install the standard test dependencies and host ALSA development files.
