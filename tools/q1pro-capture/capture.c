/* Share the exact queue and encoder implementation. The one-shot capture fixes
 * its input timing for the duration of a clip, so it skips the event watcher. */
#include "../../internal/native/cgo/video.c"
#include "capture.h"

static int stream_fd;
static atomic_uint frames;
static atomic_int output_error;
static bool sps, pps, idr;
enum { frame_limit = 30 };

void video_send_format_report(void) {}
void video_report_format(bool ready, const char *error, uint16_t width, uint16_t height, double fps) {}

void log_message(int level, const char *file, const char *func, int line, const char *message)
{
    fprintf(stderr, "%s: %s\n", func, message);
}

int video_send_frame(const uint8_t *frame, ssize_t length, uint64_t pts)
{
    if (atomic_load(&output_error) || atomic_load(&frames) >= frame_limit)
        return 0;
    bool start = false, picture = false;
    if (frame) {
        for (ssize_t i = 0; i + 3 < length; i++) {
            if (frame[i] || frame[i + 1] || frame[i + 2] != 1)
                continue;
            uint8_t type = frame[i + 3] & 31;
            start = true;
            sps |= type == 7;
            pps |= type == 8;
            idr |= type == 5;
            picture |= type == 1 || type == 5;
        }
    }
    if (!start) {
        atomic_store(&output_error, EPROTO);
        return -1;
    }
    while (length > 0) {
        ssize_t written = write(stream_fd, frame, length);
        if (written < 0 && errno == EINTR)
            continue;
        if (written <= 0) {
            atomic_store(&output_error, written < 0 ? errno : EIO);
            return -1;
        }
        frame += written;
        length -= written;
    }
    if (picture)
        atomic_fetch_add(&frames, 1);
    return 0;
}

__attribute__((visibility("default")))
int q1pro_capture(int output, uint32_t width, uint32_t height, uint32_t fps,
                  const volatile sig_atomic_t *interrupted)
{
    stream_fd = output;
    detected_width = width;
    detected_height = height;
    detected_fps = fps;
    detected_signal = true;
    if (RK_MPI_SYS_Init() != RK_SUCCESS) {
        fprintf(stderr, "Cannot initialize Rockit\n");
        return 1;
    }
    int result = 1;
    if (buf_init(true) != RK_SUCCESS) {
        fprintf(stderr, "Cannot allocate the capture DMA pool\n");
        goto exit_system;
    }
    video_start_streaming();
    uint64_t until = get_us() + 7000000;
    while (!*interrupted && !atomic_load(&output_error) &&
           atomic_load(&frames) < frame_limit && get_us() < until)
        usleep(20000);
    video_stop_streaming();
    if (atomic_load(&output_error))
        fprintf(stderr, "Cannot write H.264: %s\n", strerror(atomic_load(&output_error)));
    fprintf(stderr, "%u picture buffers, SPS=%d PPS=%d IDR=%d\n",
            atomic_load(&frames), sps, pps, idr);
    if (!*interrupted && !atomic_load(&output_error) &&
        atomic_load(&frames) == frame_limit && sps && pps && idr)
        result = 0;
    if (RK_MPI_MB_DestroyPool(memPool) != RK_SUCCESS)
        result = 1;
exit_system:
    if (RK_MPI_SYS_Exit() != RK_SUCCESS)
        result = 1;
    return result;
}
