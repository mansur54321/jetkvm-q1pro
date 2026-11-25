#include "capture.c"

int main(int argc, char **argv)
{
    if (argc != 2)
        return 1;
    FILE *source = fopen(argv[1], "rb");
    assert(source);
    uint8_t packet[32 * 1024];
    size_t length = fread(packet, 1, sizeof(packet), source);
    assert(length && feof(source));
    fclose(source);
    stream_fd = STDOUT_FILENO;
    for (unsigned i = 0; i < frame_limit + 2; i++)
        assert(!video_send_frame(packet, (ssize_t)length, i));
    assert(atomic_load(&frames) == frame_limit && sps && pps && idr);
    atomic_store(&frames, 0);
    assert(video_send_frame((uint8_t *)"invalid", 7, 0) < 0);
    assert(atomic_load(&output_error) == EPROTO);
    atomic_store(&output_error, 0);
    stream_fd = -1;
    assert(video_send_frame(packet, (ssize_t)length, 0) < 0);
    assert(atomic_load(&output_error) == EBADF && !atomic_load(&frames));
    return 0;
}
