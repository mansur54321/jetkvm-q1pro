#include <assert.h>
#include <errno.h>
#include <linux/videodev2.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/select.h>
#include <unistd.h>
#include <rk_debug.h>
#include <rk_mpi_mb.h>
#include <rk_mpi_mmz.h>
#include <rk_mpi_venc.h>
#include "ctrl.h"

extern uint32_t detected_width, detected_height, detected_fps;
extern bool detected_signal, streaming_flag, streaming_stopped;
extern void *run_video_stream(void *arg);
extern void *run_detect_format(void *arg);
extern int video_init(float factor);

static bool pool_available, system_ready, control_open;
static uint32_t expected_pool_bytes;
static uint32_t width = 1280, height = 720;
enum { CONTROL_FD = 18 };

RK_S32 RK_MPI_SYS_Init(void) { assert(!system_ready); system_ready = true; return RK_SUCCESS; }
RK_S32 RK_MPI_SYS_Exit(void) { assert(system_ready); system_ready = false; return RK_SUCCESS; }
MB_POOL RK_MPI_MB_CreatePool(MB_POOL_CONFIG_S *cfg)
{
    assert(system_ready && cfg->u64MBSize == expected_pool_bytes && cfg->u32MBCnt == 3);
    assert(cfg->enAllocType == MB_ALLOC_TYPE_DMA && cfg->bPreAlloc == RK_TRUE);
    return pool_available ? 1 : MB_INVALID_POOLID;
}
RK_S32 RK_MPI_MB_DestroyPool(MB_POOL pool) { assert(pool == 1); return RK_SUCCESS; }
void video_report_format(bool ready, const char *error, uint16_t w, uint16_t h, double fps) {}


enum scenario { BUSY_CHANNEL, RECEIVE_ERROR, THREAD_ERROR, PARTIAL_DMA, OVERSIZED_DMA, SUCCESS, ASYNC_ENCODER, BAD_CAPTURE };
static enum scenario scenario;
enum { DMA_COUNT = 3, CAPTURE_FD = 17 };
static int blocks[DMA_COUNT];
static pthread_mutex_t async_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t async_cond = PTHREAD_COND_INITIALIZER;
static bool allow_encode, encoded, delivered, captured;
static RK_U64 sent_pts;
static unsigned char outbytes[] = {0,0,1,0x65,0x80};
static struct {
    int opened, closed, allocated, released, creates, receives, threads, frames;
    bool owned, receiving, reader, capture, queued;
    bool live[DMA_COUNT];
} state;

int test_open(const char *path, int flags, ...)
{
    if (!strcmp(path, "/dev/v4l-subdev2")) {
        assert(!control_open);
        control_open = true;
        return CONTROL_FD;
    }
    assert(strcmp(path, "/dev/video0") == 0 && state.opened == 0);
    state.opened++;
    return CAPTURE_FD;
}

int test_close(int fd)
{
    if (fd == CONTROL_FD) {
        assert(control_open);
        control_open = false;
        return 0;
    }
    if (fd != CAPTURE_FD) return close(fd); /* real completion eventfd */
    assert(state.closed == 0);
    state.closed++;
    streaming_flag = false;  /* End the stream loop after one capture attempt. */
    return 0;
}

int test_usleep(useconds_t delay) { return 0; }

int test_select(int n, fd_set *readfds, fd_set *writefds, fd_set *exceptfds,
                struct timeval *timeout)
{
    assert(n == CAPTURE_FD + 1 && FD_ISSET(CAPTURE_FD, readfds) && state.capture);
    if (scenario == ASYNC_ENCODER && captured) {
        pthread_mutex_lock(&async_lock);
        allow_encode = true;
        pthread_cond_signal(&async_cond);
        pthread_mutex_unlock(&async_lock);
        FD_CLR(CAPTURE_FD, readfds);
        return select(n, readfds, NULL, NULL, timeout);
    }
    FD_ZERO(readfds);
    FD_SET(CAPTURE_FD, readfds);
    return 1;
}

int test_ioctl(int fd, unsigned long request, ...)
{
    assert(fd == CAPTURE_FD);
    va_list args;
    va_start(args, request);
    switch (request) {
    case VIDIOC_S_FMT: {
        struct v4l2_format *fmt = va_arg(args, struct v4l2_format *);
        assert(fmt->fmt.pix_mp.pixelformat == V4L2_PIX_FMT_YUYV);
        fmt->fmt.pix_mp.num_planes = 1;
        fmt->fmt.pix_mp.plane_fmt[0].bytesperline = width * 2;
        fmt->fmt.pix_mp.plane_fmt[0].sizeimage = scenario == OVERSIZED_DMA ? expected_pool_bytes + 1 : width * height * 2;
        break;
    }
    case VIDIOC_REQBUFS: {
        struct v4l2_requestbuffers *req = va_arg(args, struct v4l2_requestbuffers *);
        assert(req->memory == V4L2_MEMORY_DMABUF);
        assert(req->count == (state.queued ? 0 : DMA_COUNT));
        state.queued = req->count != 0;
        break;
    }
    case VIDIOC_QUERYBUF: {
        struct v4l2_buffer *buf = va_arg(args, struct v4l2_buffer *);
        assert(state.queued && buf->index < DMA_COUNT);
        buf->m.planes[0].length = width * height * 2;
        break;
    }
    case VIDIOC_QBUF: {
        struct v4l2_buffer *buf = va_arg(args, struct v4l2_buffer *);
        assert(state.queued && buf->m.planes[0].m.fd == 60 + (int)buf->index);
        if (state.capture && (scenario == ASYNC_ENCODER || scenario == BAD_CAPTURE)) {
            pthread_mutex_lock(&async_lock);
            assert(scenario == BAD_CAPTURE || encoded);
            pthread_mutex_unlock(&async_lock);
            streaming_flag = false;
        }
        break;
    }
    case VIDIOC_STREAMON:
        assert(state.queued && !state.capture);
        state.capture = true;
        break;
    case VIDIOC_STREAMOFF:
        assert(state.capture);
        state.capture = false;
        break;
    case VIDIOC_DQBUF: {
        struct v4l2_buffer *buf = va_arg(args, struct v4l2_buffer *);
        assert(state.capture);
        buf->index = 1;
        captured = true;
        buf->m.planes[0].m.fd = 99;  /* Requeue must use the slot's imported fd. */
        buf->m.planes[0].bytesused = width * height * 2;
        if (scenario == BAD_CAPTURE) {
            buf->flags = V4L2_BUF_FLAG_ERROR;
            buf->m.planes[0].bytesused = 0;
        }
        break;
    }
    default:
        abort();
    }
    va_end(args);
    return 0;
}

MB_BLK RK_MPI_MB_GetMB(MB_POOL pool, RK_U64 size, RK_BOOL block)
{
    assert(pool == 1 && size == expected_pool_bytes && block == RK_FALSE);
    int index = state.allocated;
    assert(index < DMA_COUNT);
    if (scenario == PARTIAL_DMA && index == 2)
        return NULL;
    state.live[index] = true;
    state.allocated++;
    return &blocks[index];
}

RK_S32 RK_MPI_MB_Handle2Fd(MB_BLK block)
{
    for (int i = 0; i < DMA_COUNT; i++)
        if (state.live[i] && block == &blocks[i])
            return 60 + i;
    abort();
}

MB_BLK RK_MPI_MMZ_Fd2Handle(RK_S32 fd)
{
    int index = fd - 60;
    assert(index >= 0 && index < DMA_COUNT && state.live[index]);
    return &blocks[index];
}

RK_S32 RK_MPI_MB_ReleaseMB(MB_BLK block)
{
    for (int i = 0; i < DMA_COUNT; i++) {
        if (state.live[i] && block == &blocks[i]) {
            state.live[i] = false;
            state.released++;
            return RK_SUCCESS;
        }
    }
    abort();
}

RK_S32 RK_MPI_VENC_CreateChn(VENC_CHN ch, const VENC_CHN_ATTR_S *attr)
{
    assert(ch == 0 && state.capture && !state.owned);
    state.creates++;
    if (scenario == BUSY_CHANNEL)
        return RK_ERR_VENC_EXIST;
    state.owned = true;
    return RK_SUCCESS;
}

RK_S32 RK_MPI_VENC_StartRecvFrame(VENC_CHN ch, const VENC_RECV_PIC_PARAM_S *params)
{
    assert(ch == 0 && state.owned && !state.receiving);
    state.receives++;
    if (scenario == RECEIVE_ERROR)
        return RK_FAILURE;
    state.receiving = true;
    return RK_SUCCESS;
}

int test_pthread_create(pthread_t *thread, const pthread_attr_t *attr,
                        void *(*routine)(void *), void *arg)
{
    if (routine == run_detect_format) {
        *thread = (pthread_t)2;
        return 0;
    }
    assert(state.receiving && !state.reader);
    state.threads++;
    if (scenario == THREAD_ERROR)
        return EAGAIN;
    state.reader = true;
    if (scenario == ASYNC_ENCODER) return pthread_create(thread, attr, routine, arg);
    *thread = (pthread_t)1;
    return 0;
}

int test_pthread_detach(pthread_t thread) { assert(thread == (pthread_t)2); return 0; }

int test_pthread_join(pthread_t thread, void **result)
{
    assert(state.reader);
    if (scenario == ASYNC_ENCODER) {
        int ret = pthread_join(thread, result);
        state.reader = false;
        return ret;
    }
    assert(thread == (pthread_t)1);
    state.reader = false;
    return 0;
}

RK_S32 RK_MPI_VENC_SendFrame(VENC_CHN ch, const VIDEO_FRAME_INFO_S *frame, RK_S32 ms)
{
    assert(ch == 0 && state.receiving && state.reader && state.live[1]);
    assert(frame->stVFrame.pMbBlk == &blocks[1]);
    state.frames++;
    if (scenario == ASYNC_ENCODER) {
        pthread_mutex_lock(&async_lock);
        sent_pts = frame->stVFrame.u64PTS;
        pthread_mutex_unlock(&async_lock);
    } else streaming_flag = false;
    return RK_SUCCESS;
}

RK_S32 RK_MPI_VENC_StopRecvFrame(VENC_CHN ch)
{
    assert(ch == 0 && state.owned && state.receiving);
    state.receiving = false;
    return RK_SUCCESS;
}

RK_S32 RK_MPI_VENC_DestroyChn(VENC_CHN ch)
{
    assert(ch == 0 && state.owned && !state.receiving && !state.reader);
    state.owned = false;
    return RK_SUCCESS;
}

void video_send_format_report(void) {}
void RK_LOG(RK_S32 level, RK_S32 mod, const char *fmt, const char *func, RK_U32 line, ...) {}
RK_S32 RK_MPI_VENC_GetH264Vui(VENC_CHN ch, VENC_H264_VUI_S *vui) { return RK_FAILURE; }
RK_S32 RK_MPI_VENC_GetH265Vui(VENC_CHN ch, VENC_H265_VUI_S *vui) { return RK_FAILURE; }
RK_S32 RK_MPI_VENC_SetH264Vui(VENC_CHN ch, const VENC_H264_VUI_S *vui) { abort(); }
RK_S32 RK_MPI_VENC_SetH265Vui(VENC_CHN ch, const VENC_H265_VUI_S *vui) { abort(); }
RK_S32 RK_MPI_VENC_GetStream(VENC_CHN ch, VENC_STREAM_S *stream, RK_S32 ms) {
    assert(scenario == ASYNC_ENCODER);
    pthread_mutex_lock(&async_lock);
    while (!allow_encode) pthread_cond_wait(&async_cond, &async_lock);
    if (encoded) {
        pthread_mutex_unlock(&async_lock);
        usleep(1000);
        return RK_ERR_VENC_BUF_EMPTY;
    }
    assert(sent_pts);
    *stream->pstPack = (VENC_PACK_S){.pMbBlk=outbytes, .u32Offset=2746496, .u32Len=5, .u64PTS=sent_pts, .bFrameEnd=RK_TRUE};
    encoded = true;
    pthread_mutex_unlock(&async_lock);
    return RK_SUCCESS;
}
RK_S32 RK_MPI_VENC_ReleaseStream(VENC_CHN ch, VENC_STREAM_S *stream) {
    assert(stream->pstPack->pMbBlk == outbytes && delivered);
    return RK_SUCCESS;
}
RK_VOID *RK_MPI_MB_Handle2VirAddr(MB_BLK block) { assert(block == outbytes); return outbytes; }
RK_U64 RK_MPI_MB_GetSize(MB_BLK block) { assert(block == outbytes); return sizeof(outbytes); }
int video_send_frame(const uint8_t *frame, ssize_t len, uint64_t pts) {
    assert(scenario == ASYNC_ENCODER && frame == outbytes && len == 5 && pts == sent_pts);
    delivered = true;
    return 0;
}

static void check(enum scenario test, const char *name, int allocated,
                  int creates, int receives, int threads, int frames)
{
    scenario = test;
    allow_encode = encoded = delivered = captured = false;
    sent_pts = 0;
    memset(&state, 0, sizeof(state));
    detected_signal = streaming_flag = true;
    detected_width = width;
    detected_height = height;
    detected_fps = 30;
    alarm(5);
    run_video_stream(NULL);
    alarm(0);
    assert(streaming_stopped && state.opened == 1 && state.closed == 1);
    assert(!state.owned && !state.receiving && !state.reader && !state.capture && !state.queued);
    assert(state.allocated == allocated && state.released == allocated);
    assert(state.creates == creates && state.receives == receives);
    assert(state.threads == threads && state.frames == frames);
    printf("ok - %s\n", name);
}

int main(int argc, char **argv)
{
    assert(argc == 2);
    bool q1 = !strcmp(argv[1], "q1");
    expected_pool_bytes = q1 ? 4177920 : 6220800;
    assert(video_init(1.0f) != RK_SUCCESS && !system_ready && !control_open);
    pool_available = true;
    assert(video_init(1.0f) == RK_SUCCESS && system_ready && control_open);
    check(BUSY_CHANNEL, "busy channel stays untouched", 3, 1, 0, 0, 0);
    check(RECEIVE_ERROR, "receive failure releases only the owned channel", 3, 1, 1, 0, 0);
    check(THREAD_ERROR, "reader creation failure stops reception and releases channel", 3, 1, 1, 1, 0);
    check(PARTIAL_DMA, "partial allocation releases exactly two DMA blocks", 2, 0, 0, 0, 0);
    check(SUCCESS, "one frame is sent and all resources are released", 3, 1, 1, 1, 1);
    if (q1) {
        check(ASYNC_ENCODER, "Q1 waits for encoder completion and sends the mapped MB payload despite a large ring offset", 3, 1, 1, 1, 1);
        assert(encoded && delivered);
        check(BAD_CAPTURE, "Q1 skips a flagged capture buffer without submitting it to VENC", 3, 1, 1, 1, 0);
        width = 1920; height = 1080;
        check(SUCCESS, "Q1 1920x1080 uses the aligned DMA pool", 3, 1, 1, 1, 1);
        check(OVERSIZED_DMA, "Q1 rejects an oversized driver image before allocating DMA", 0, 0, 0, 0, 0);
        width = 1600; height = 1200;
        check(SUCCESS, "Q1 1600x1200 uses the same DMA pool", 3, 1, 1, 1, 1);
    }
    return 0;
}
