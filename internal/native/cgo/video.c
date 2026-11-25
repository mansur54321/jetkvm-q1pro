#define _POSIX_C_SOURCE 200809L
#include <unistd.h>
#include <time.h>
#include <stdio.h>
#include <rk_type.h>
#include <rk_mpi_venc.h>
#include <rk_mpi_sys.h>
#include <string.h>
#include <rk_debug.h>
#include <malloc.h>
#include <stdbool.h>
#include <rk_mpi_mb.h>
#include <fcntl.h>
#include <linux/videodev2.h>
#include <sys/ioctl.h>
#include <sys/eventfd.h>
#include <limits.h>
#include <errno.h>
#include <unistd.h>
#include <stdatomic.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <pthread.h>
#include <assert.h>
#include <sys/un.h>
#include <sys/socket.h>
#include "video.h"
#include "capture_limits.h"
#include "ctrl.h"
#include "log.h"

#define VIDEO_DEV "/dev/video0"
#define SUB_DEV "/dev/v4l-subdev2"
#define SLEEP_MODE_FILE "/sys/devices/platform/ff470000.i2c/i2c-4/4-000f/sleep_mode"

#define RK_ALIGN(x, a) (((x) + (a)-1) & ~((a)-1))
#define RK_ALIGN_2(x) RK_ALIGN(x, 2)
#define RK_ALIGN_16(x) RK_ALIGN(x, 16)
#define RK_ALIGN_32(x) RK_ALIGN(x, 32)

int sub_dev_fd = -1;
#define VENC_CHANNEL 0
MB_POOL memPool = MB_INVALID_POOLID;

bool sleep_mode_available = false;
bool should_exit = false;
float quality_factor = 1.0f;
int codec_type = 0;

static void *venc_read_stream(void *arg);
static bool use_q1_capture_pool;

RK_U64 get_us()
{
    struct timespec time = {0, 0};
    clock_gettime(CLOCK_MONOTONIC, &time);
    return (RK_U64)time.tv_sec * 1000000 + (RK_U64)time.tv_nsec / 1000; /* microseconds */
}

static void ensure_sleep_mode_disabled()
{
    if (!sleep_mode_available)
    {
        return;
    }

    int fd = open(SLEEP_MODE_FILE, O_RDWR);
    if (fd < 0)
    {
        log_error("Failed to open sleep mode file: %s", strerror(errno));
        return;
    }
    lseek(fd, 0, SEEK_SET);
    char buffer[1];
    read(fd, buffer, 1);
    if (buffer[0] == '0') {
        close(fd);
        return;
    }
    log_warn("HDMI sleep mode is not disabled, disabling it");
    lseek(fd, 0, SEEK_SET);
    write(fd, "0", 1);
    close(fd);

    usleep(1000); // give some time to the system to disable the sleep mode
    return;
}

static void detect_sleep_mode()
{
    if (access(SLEEP_MODE_FILE, F_OK) != 0) {
        sleep_mode_available = false;
        return;
    }
    sleep_mode_available = true;
    ensure_sleep_mode_disabled();
}

double calculate_bitrate(float bitrate_factor, int width, int height)
{
    const int32_t base_bitrate_high = 4000;
    const int32_t base_bitrate_low = 512;

    double pixels = (double)width * height;
    double ref_pixels = 1920.0 * 1080.0;

    double scale_factor = pixels / ref_pixels;

    int32_t base_bitrate = base_bitrate_low + (int32_t)((base_bitrate_high - base_bitrate_low) * bitrate_factor);

    int32_t bitrate = (int32_t)(base_bitrate * scale_factor);

    const int32_t min_bitrate = 200;
    if (bitrate < min_bitrate)
    {
        bitrate = min_bitrate;
    }

    return bitrate;
}

static bool is_hd_video(RK_U32 height)
{
    return height > 576;
}

static COLOR_GAMUT_E color_gamut_for_height(RK_U32 height)
{
    return is_hd_video(height) ? COLOR_GAMUT_BT709 : COLOR_GAMUT_BT601;
}

static void set_v4l2_yuv_colorimetry(struct v4l2_pix_format_mplane *pix, RK_U32 height)
{
    if (is_hd_video(height))
    {
        pix->colorspace = V4L2_COLORSPACE_REC709;
        pix->ycbcr_enc = V4L2_YCBCR_ENC_709;
    }
    else
    {
        pix->colorspace = V4L2_COLORSPACE_SMPTE170M;
        pix->ycbcr_enc = V4L2_YCBCR_ENC_601;
    }

    pix->quantization = V4L2_QUANTIZATION_LIM_RANGE;
    pix->xfer_func = V4L2_XFER_FUNC_709;
}

static void configure_vui_video_signal(VENC_VUI_VIDEO_SIGNAL_S *video_signal, RK_U32 height)
{
    memset(video_signal, 0, sizeof(VENC_VUI_VIDEO_SIGNAL_S));
    video_signal->video_signal_type_present_flag = 1;
    video_signal->video_format = 5; // unspecified
    video_signal->video_full_range_flag = 0; // YUV video levels: black=16, white=235
    video_signal->colour_description_present_flag = 1;

    if (is_hd_video(height))
    {
        video_signal->colour_primaries = 1; // BT.709
        video_signal->transfer_characteristics = 1; // BT.709
        video_signal->matrix_coefficients = 1; // BT.709
    }
    else
    {
        video_signal->colour_primaries = 6; // SMPTE 170M
        video_signal->transfer_characteristics = 6; // SMPTE 170M
        video_signal->matrix_coefficients = 6; // SMPTE 170M
    }
}

static void populate_venc_attr(VENC_CHN_ATTR_S *stAttr, RK_U32 bitrate, RK_U32 max_bitrate, RK_U32 width, RK_U32 height, RK_U32 stride_pixels, RK_U32 fps)
{
    memset(stAttr, 0, sizeof(VENC_CHN_ATTR_S));

    RK_U32 min_bitrate = bitrate / 2;
    if (min_bitrate < 2) min_bitrate = 2;

    // GOP scales with framerate so IDR cadence stays ~0.5s regardless of source
    // refresh — keeps WebRTC recovery latency bounded at 60 Hz and 120 Hz alike.
    RK_U32 gop = fps > 0 ? fps / 2 : 30;
    if (gop < 1) gop = 1;

    if (codec_type == 1) {
        // H.265 (HEVC)
        stAttr->stRcAttr.enRcMode = VENC_RC_MODE_H265VBR;
        stAttr->stRcAttr.stH265Vbr.u32BitRate = bitrate;
        stAttr->stRcAttr.stH265Vbr.u32MaxBitRate = max_bitrate;
        stAttr->stRcAttr.stH265Vbr.u32MinBitRate = min_bitrate;
        stAttr->stRcAttr.stH265Vbr.u32Gop = gop;
        stAttr->stRcAttr.stH265Vbr.u32StatTime = 2;
        stAttr->stRcAttr.stH265Vbr.u32SrcFrameRateNum = fps;
        stAttr->stRcAttr.stH265Vbr.u32SrcFrameRateDen = 1;
        stAttr->stRcAttr.stH265Vbr.fr32DstFrameRateNum = fps;
        stAttr->stRcAttr.stH265Vbr.fr32DstFrameRateDen = 1;
        stAttr->stVencAttr.enType = RK_VIDEO_ID_HEVC;
        stAttr->stVencAttr.u32Profile = H265E_PROFILE_MAIN;
    } else {
        // H.264 (AVC)
        stAttr->stRcAttr.enRcMode = VENC_RC_MODE_H264VBR;
        stAttr->stRcAttr.stH264Vbr.u32BitRate = bitrate;
        stAttr->stRcAttr.stH264Vbr.u32MaxBitRate = max_bitrate;
        stAttr->stRcAttr.stH264Vbr.u32MinBitRate = min_bitrate;
        stAttr->stRcAttr.stH264Vbr.u32Gop = gop;
        stAttr->stRcAttr.stH264Vbr.u32StatTime = 2;
        stAttr->stRcAttr.stH264Vbr.u32SrcFrameRateNum = fps;
        stAttr->stRcAttr.stH264Vbr.u32SrcFrameRateDen = 1;
        stAttr->stRcAttr.stH264Vbr.fr32DstFrameRateNum = fps;
        stAttr->stRcAttr.stH264Vbr.fr32DstFrameRateDen = 1;
        stAttr->stVencAttr.enType = RK_VIDEO_ID_AVC;
        stAttr->stVencAttr.u32Profile = H264E_PROFILE_HIGH;
    }

    stAttr->stVencAttr.enPixelFormat = RK_FMT_YUV422_YUYV;
    stAttr->stVencAttr.u32PicWidth = width;
    stAttr->stVencAttr.u32PicHeight = height;
    stAttr->stVencAttr.u32VirWidth = stride_pixels;
    stAttr->stVencAttr.u32VirHeight = RK_ALIGN_16(height);
    if (use_q1_capture_pool)
        stAttr->stVencAttr.bByFrame = RK_TRUE;
    stAttr->stVencAttr.u32StreamBufCnt = 3;
    stAttr->stVencAttr.u32BufSize = width * height * 3 / 2;
    stAttr->stVencAttr.enMirror = MIRROR_NONE;
}

static void venc_configure_limited_range_vui(RK_U32 height)
{
    int32_t ret;

    if (codec_type == 1)
    {
        VENC_H265_VUI_S stH265Vui;
        memset(&stH265Vui, 0, sizeof(VENC_H265_VUI_S));
        ret = RK_MPI_VENC_GetH265Vui(VENC_CHANNEL, &stH265Vui);
        if (ret != RK_SUCCESS)
        {
            log_warn("RK_MPI_VENC_GetH265Vui failed: %#x", ret);
            return;
        }
        configure_vui_video_signal(&stH265Vui.stVuiVideoSignal, height);
        ret = RK_MPI_VENC_SetH265Vui(VENC_CHANNEL, &stH265Vui);
        if (ret != RK_SUCCESS)
        {
            log_warn("RK_MPI_VENC_SetH265Vui failed: %#x", ret);
            return;
        }
    }
    else
    {
        VENC_H264_VUI_S stH264Vui;
        memset(&stH264Vui, 0, sizeof(VENC_H264_VUI_S));
        ret = RK_MPI_VENC_GetH264Vui(VENC_CHANNEL, &stH264Vui);
        if (ret != RK_SUCCESS)
        {
            log_warn("RK_MPI_VENC_GetH264Vui failed: %#x", ret);
            return;
        }
        configure_vui_video_signal(&stH264Vui.stVuiVideoSignal, height);
        ret = RK_MPI_VENC_SetH264Vui(VENC_CHANNEL, &stH264Vui);
        if (ret != RK_SUCCESS)
        {
            log_warn("RK_MPI_VENC_SetH264Vui failed: %#x", ret);
            return;
        }
    }

    log_info("configured VENC VUI as limited-range %s", is_hd_video(height) ? "BT.709" : "BT.601");
}

static pthread_t venc_read_thread;
static atomic_bool venc_running;
static int32_t venc_start(int32_t bitrate, int32_t max_bitrate, int32_t width, int32_t height, uint32_t stride_pixels, int32_t fps)
{
    int32_t ret;
    VENC_CHN_ATTR_S stAttr;
    populate_venc_attr(&stAttr, bitrate, max_bitrate, width, height, stride_pixels, fps);

    ret = RK_MPI_VENC_CreateChn(VENC_CHANNEL, &stAttr);
    if (ret != RK_SUCCESS)
    {
        RK_LOGE("error RK_MPI_VENC_CreateChn, %d", ret);
        return ret;
    }

    venc_configure_limited_range_vui(height);

    VENC_RECV_PIC_PARAM_S stRecvParam;
    memset(&stRecvParam, 0, sizeof(VENC_RECV_PIC_PARAM_S));
    stRecvParam.s32RecvPicNum = -1;
    ret = RK_MPI_VENC_StartRecvFrame(VENC_CHANNEL, &stRecvParam);
    if (ret != RK_SUCCESS)
    {
        RK_LOGE("error RK_MPI_VENC_StartRecvFrame, %d", ret);
        goto destroy_channel;
    }

    venc_running = true;
    int thread_error = pthread_create(&venc_read_thread, NULL, venc_read_stream, NULL);
    if (thread_error != 0)
    {
        RK_LOGE("Failed to create venc_read_thread: %s", strerror(thread_error));
        venc_running = false;
        if (RK_MPI_VENC_StopRecvFrame(VENC_CHANNEL) != RK_SUCCESS)
            log_error("Failed to stop VENC after thread creation error");
        ret = RK_FAILURE;
        goto destroy_channel;
    }

    return RK_SUCCESS;

destroy_channel:
    if (RK_MPI_VENC_DestroyChn(VENC_CHANNEL) != RK_SUCCESS)
        log_error("Failed to destroy VENC after initialization error");
    return ret;
}

static int32_t venc_stop()
{
    venc_running = false;
    pthread_join(venc_read_thread, NULL);

    int32_t ret = RK_MPI_VENC_StopRecvFrame(VENC_CHANNEL);
    if (ret != RK_SUCCESS)
    {
        RK_LOGE("Failed to stop receiving frames for VENC_CHANNEL, error code: %d", ret);
    }

    int32_t destroy_ret = RK_MPI_VENC_DestroyChn(VENC_CHANNEL);
    if (destroy_ret != RK_SUCCESS)
    {
        RK_LOGE("Failed to destroy VENC_CHANNEL, error code: %d", destroy_ret);
        return destroy_ret;
    }

    return ret;
}

struct buffer
{
    struct v4l2_plane plane_buffer;
    MB_BLK mb_blk;
};

enum { input_buffer_count = 3 };
static pthread_mutex_t capture_ack_lock = PTHREAD_MUTEX_INITIALIZER;
static int capture_ack_fd = -1;
static struct {
    uint64_t pts;
    bool encoded;
} capture_pending[input_buffer_count];

// Only the capture thread queues V4L2 buffers. The VENC thread notifies it
// after it receives the encoded frame with that input's PTS.
static void acknowledge_capture(uint64_t pts)
{
    if (!pts || capture_ack_fd < 0)
        return;
    pthread_mutex_lock(&capture_ack_lock);
    for (unsigned i = 0; i < input_buffer_count; i++)
    {
        if (capture_pending[i].pts == pts && !capture_pending[i].encoded)
        {
            capture_pending[i].encoded = true;
            while (eventfd_write(capture_ack_fd, 1) < 0 && errno == EINTR) {}
            break;
        }
    }
    pthread_mutex_unlock(&capture_ack_lock);
}

static int queue_capture(int fd, unsigned index, struct buffer *buffers)
{
    struct v4l2_buffer buf = {
        .type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE,
        .memory = V4L2_MEMORY_DMABUF,
        .index = index,
        .length = 1,
        .m.planes = &buffers[index].plane_buffer,
    };
    if (ioctl(fd, VIDIOC_QBUF, &buf) < 0)
    {
        log_error("VIDIOC_QBUF %u failed: %s", index, strerror(errno));
        return -1;
    }
    return 0;
}

static int reclaim_encoded(int fd, struct buffer *buffers)
{
    uint64_t now = get_us();
    unsigned ready = 0;
    int pending = 0;
    pthread_mutex_lock(&capture_ack_lock);
    for (unsigned i = 0; i < input_buffer_count; i++)
    {
        if (!capture_pending[i].pts)
            continue;
        if (capture_pending[i].encoded)
        {
            capture_pending[i].pts = 0;
            capture_pending[i].encoded = false;
            ready |= 1U << i;
        }
        else if (now - capture_pending[i].pts > 2000000)
        {
            log_error("VENC did not finish capture slot %u within two seconds", i);
            pending = -1;
            break;
        }
        else
            pending++;
    }
    pthread_mutex_unlock(&capture_ack_lock);
    for (unsigned i = 0; i < input_buffer_count; i++)
        if ((ready & (1U << i)) && queue_capture(fd, i, buffers) < 0)
            return -1;
    return pending;
}


// The size of one capture buffer in the pool. A frame that needs more is not
// captured: a request to the pool for a larger block does not return.
static uint32_t capture_block_bytes(void)
{
    return use_q1_capture_pool ? Q1_CAPTURE_BLOCK_BYTES : 1920 * 1080 * 3;
}

static bool has_q1_capture_sku(void)
{
    FILE *file = fopen("/etc/jetkvm-sku", "r");
    if (!file)
        return false;
    char sku[32] = {0};
    if (fgets(sku, sizeof(sku), file))
        sku[strcspn(sku, "\r\n")] = 0;
    fclose(file);
    return strcmp(sku, "oraybox-q1pro") == 0;
}

static int32_t buf_init(bool q1_pro)
{
    use_q1_capture_pool = q1_pro;
    MB_POOL_CONFIG_S stMbPoolCfg;
    memset(&stMbPoolCfg, 0, sizeof(MB_POOL_CONFIG_S));
    stMbPoolCfg.u64MBSize = capture_block_bytes();
    stMbPoolCfg.u32MBCnt = input_buffer_count;
    stMbPoolCfg.enAllocType = MB_ALLOC_TYPE_DMA;
    stMbPoolCfg.bPreAlloc = RK_TRUE;
    memPool = RK_MPI_MB_CreatePool(&stMbPoolCfg);
    if (memPool == MB_INVALID_POOLID)
    {
        return -1;
    }
    log_info("created memory pool");

    return RK_SUCCESS;
}

int video_init(float factor)
{
    sub_dev_fd = open(SUB_DEV, O_RDWR);
    if (sub_dev_fd < 0)
    {
        int err = errno;
        log_error("failed to open control sub device %s: %s", SUB_DEV, strerror(err));
        return err;
    }
    detect_sleep_mode();
    quality_factor = factor > 0 ? factor : 1.0f;
    int32_t ret = RK_MPI_SYS_Init();
    if (ret != RK_SUCCESS)
    {
        log_error("RK_MPI_SYS_Init failed");
        goto close_device;
    }
    ret = buf_init(has_q1_capture_sku());
    if (ret != RK_SUCCESS)
    {
        log_error("buf_init failed with error: %d", ret);
        goto exit_mpi;
    }
    pthread_t format_thread;
    ret = pthread_create(&format_thread, NULL, run_detect_format, NULL);
    if (!ret)
    {
        pthread_detach(format_thread);
        return RK_SUCCESS;
    }
    log_error("failed to create format detector: %s", strerror(ret));
    RK_MPI_MB_DestroyPool(memPool);
    memPool = MB_INVALID_POOLID;
exit_mpi:
    RK_MPI_SYS_Exit();
close_device:
    close(sub_dev_fd);
    sub_dev_fd = -1;
    return ret;
}

// static int32_t venc_set_param(int32_t bitrate, int32_t max_bitrate, int32_t width, int32_t height)
// {

//     VENC_CHN_ATTR_S stAttr;
//     populate_venc_attr(&stAttr, bitrate, max_bitrate, width, height);
//     VENC_CHN_PARAM_S stParam;
//     memset(&stParam, 0, sizeof(VENC_CHN_PARAM_S));

//     RK_MPI_VENC_StopRecvFrame(VENC_CHANNEL);

//     int32_t ret = RK_MPI_VENC_SetChnParam(VENC_CHANNEL, &stAttr);
//     if (ret < 0)
//     {
//         RK_LOGE("error RK_MPI_VENC_SetChnParam, %d", ret);
//         return ret;
//     }
//     VENC_RECV_PIC_PARAM_S stRecvParam;
//     memset(&stRecvParam, 0, sizeof(VENC_RECV_PIC_PARAM_S));
//     stRecvParam.s32RecvPicNum = -1;
//     ret = RK_MPI_VENC_StartRecvFrame(VENC_CHANNEL, &stRecvParam);
//     if (ret < 0)
//     {
//         RK_LOGE("error RK_MPI_VENC_StartRecvFrame, %d", ret);
//         return ret;
//     }

//     return RK_SUCCESS;
// }

/**
 * @brief Continuously reads encoded video streams and sends them over unix socket.
 *
 * @param arg Unused parameter (void pointer for thread compatibility)
 * @return NULL Always returns NULL
 */
static void *venc_read_stream(void *arg)
{
    (void)arg;
    int loopCount = 0;
    int s32Ret;

    VENC_PACK_S pack = {0};
    VENC_STREAM_S stFrame = {.pstPack = &pack};
    while (venc_running)
    {
        log_trace("RK_MPI_VENC_GetStream");
        s32Ret = RK_MPI_VENC_GetStream(VENC_CHANNEL, &stFrame, 200); // blocks max 200ms
        if (s32Ret == RK_SUCCESS)
        {
            RK_U64 nowUs = get_us();
            log_trace("chn:0, loopCount:%d enc->seq:%d wd:%d pts=%llu delay=%lldus",
                   loopCount, stFrame.u32Seq, stFrame.pstPack->u32Len,
                   stFrame.pstPack->u64PTS, nowUs - stFrame.pstPack->u64PTS);
            VENC_PACK_S *packet = stFrame.pstPack;
            if (use_q1_capture_pool && packet->bFrameEnd)
                acknowledge_capture(packet->u64PTS);
            uint8_t *base = RK_MPI_MB_Handle2VirAddr(packet->pMbBlk);
            uint64_t capacity = RK_MPI_MB_GetSize(packet->pMbBlk);
            // Rockit maps this packet's MB at its payload. u32Offset is its
            // position in the encoder ring, not an offset into the mapped MB.
            if (base && capacity != UINT64_MAX && packet->u32Len <= capacity &&
                packet->u32Len <= INT_MAX)
                video_send_frame(base, packet->u32Len, packet->u64PTS);
            else
                log_error("Invalid mapped VENC payload: ring offset %u length %u capacity %llu",
                          packet->u32Offset, packet->u32Len, (unsigned long long)capacity);

            s32Ret = RK_MPI_VENC_ReleaseStream(VENC_CHANNEL, &stFrame);
            if (s32Ret != RK_SUCCESS)
            {
                log_error("RK_MPI_VENC_ReleaseStream fail %x", s32Ret);
            }
            loopCount++;
        }
        else
        {
            if (s32Ret == RK_ERR_VENC_BUF_EMPTY)
            {
                continue;
            }
            log_error("RK_MPI_VENC_GetStream fail %x", s32Ret);
            break;
        }
    }
    log_info("exiting venc_read_stream");
    return NULL;
}

uint32_t detected_width, detected_height;
// detected_fps is the rounded source vrefresh from the latest dv-timings query.
// 60 is a safe default until the first SOURCE_CHANGE event fires.
uint32_t detected_fps = 60;
bool detected_signal = false, streaming_flag = false;

bool streaming_stopped = true;
pthread_mutex_t streaming_stopped_mutex = PTHREAD_MUTEX_INITIALIZER;

pthread_t *streaming_thread = NULL;
pthread_mutex_t streaming_mutex = PTHREAD_MUTEX_INITIALIZER;
static pthread_mutex_t video_lifecycle_mutex = PTHREAD_MUTEX_INITIALIZER;

bool get_streaming_flag()
{
    log_info("getting streaming flag");
    pthread_mutex_lock(&streaming_mutex);
    bool flag = streaming_flag;
    pthread_mutex_unlock(&streaming_mutex);
    return flag;
}

void set_streaming_flag(bool flag)
{
    log_info("setting streaming flag to %d", flag);

    pthread_mutex_lock(&streaming_mutex);
    streaming_flag = flag;
    pthread_mutex_unlock(&streaming_mutex);

    video_send_format_report();
}

void set_streaming_stopped(bool stopped)
{
    pthread_mutex_lock(&streaming_stopped_mutex);
    streaming_stopped = stopped;
    pthread_mutex_unlock(&streaming_stopped_mutex);

    video_send_format_report();
}

bool get_streaming_stopped()
{
    pthread_mutex_lock(&streaming_stopped_mutex);
    bool stopped = streaming_stopped;
    pthread_mutex_unlock(&streaming_stopped_mutex);
    return stopped;
}

void write_buffer_to_file(const uint8_t *buffer, size_t length, const char *filename)
{
    FILE *file = fopen(filename, "wb");
    fwrite(buffer, 1, length, file);
    fclose(file);
}

void *run_video_stream(void *arg)
{
    enum v4l2_buf_type type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;

    const uint32_t input_buffer_size = capture_block_bytes();
    log_info("running video stream");

    set_streaming_stopped(false);
    while (streaming_flag)
    {
        if (detected_signal == false)
        {
            usleep(10000); // Reduced to 10ms for better responsiveness to streaming_flag changes
            continue;
        }

        int video_dev_fd = open(VIDEO_DEV, O_RDWR);
        if (video_dev_fd < 0)
        {
            log_error("failed to open video capture device %s: %s", VIDEO_DEV, strerror(errno));
            usleep(1000000);
            continue;
        }
        log_info("opened video capture device %s", VIDEO_DEV);

        uint32_t width = detected_width;
        uint32_t height = detected_height;
        uint32_t fps = detected_fps;
        struct v4l2_format fmt;
        memset(&fmt, 0, sizeof(struct v4l2_format));
        fmt.type = type;
        fmt.fmt.pix_mp.width = width;
        fmt.fmt.pix_mp.height = height;
        fmt.fmt.pix_mp.pixelformat = V4L2_PIX_FMT_YUYV;
        fmt.fmt.pix_mp.field = V4L2_FIELD_ANY;
        set_v4l2_yuv_colorimetry(&fmt.fmt.pix_mp, height);

        if (ioctl(video_dev_fd, VIDIOC_S_FMT, &fmt) < 0)
        {
            log_error("Set format fail: %s", strerror(errno));
            usleep(100000); // Sleep for 100 milliseconds
            close(video_dev_fd);
            continue;
        }
        log_info("capture colorimetry: colorspace=%u ycbcr_enc=%u quantization=%u xfer=%u",
                 fmt.fmt.pix_mp.colorspace, fmt.fmt.pix_mp.ycbcr_enc,
                 fmt.fmt.pix_mp.quantization, fmt.fmt.pix_mp.xfer_func);

        const struct v4l2_pix_format_mplane *capture = &fmt.fmt.pix_mp;
        const struct v4l2_plane_pix_format *plane = &capture->plane_fmt[0];
        const uint32_t stride_pixels = plane->bytesperline / 2;
        if (capture->pixelformat != V4L2_PIX_FMT_YUYV || capture->num_planes != 1 ||
            !width || !height || capture->width != width || capture->height != height ||
            plane->bytesperline % 32 != 0 || stride_pixels < width ||
            (uint64_t)plane->bytesperline * height > plane->sizeimage ||
            plane->sizeimage > input_buffer_size ||
            (uint64_t)plane->bytesperline * RK_ALIGN_16((uint64_t)height) > input_buffer_size)
        {
            log_error("Cannot encode capture format %#x %ux%u, %u planes, %u bytes/line, %u bytes; "
                      "expected single-plane YUYV %ux%u with a 16-pixel aligned stride, buffer limit %u",
                      capture->pixelformat, capture->width, capture->height, capture->num_planes,
                      plane->bytesperline, plane->sizeimage, width, height, input_buffer_size);
            close(video_dev_fd);
            usleep(1000000);
            continue;
        }

        struct v4l2_buffer buf;

        struct v4l2_requestbuffers req = {0};
        req.count = input_buffer_count;
        req.type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
        req.memory = V4L2_MEMORY_DMABUF;

        if (ioctl(video_dev_fd, VIDIOC_REQBUFS, &req) < 0)
        {
            log_error("VIDIOC_REQBUFS failed: %s", strerror(errno));
            close(video_dev_fd);
            usleep(1000000);
            continue;
        }
        log_info("VIDIOC_REQBUFS successful");

        struct buffer buffers[3] = {};
        bool capture_started = false;
        bool encoder_started = false;
        log_info("allocated buffers");

        int queried = 0;
        for (; queried < input_buffer_count; queried++)
        {
            struct v4l2_plane *planes_buffer = &buffers[queried].plane_buffer;
            memset(planes_buffer, 0, sizeof(struct v4l2_plane));

            memset(&buf, 0, sizeof(buf));
            buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
            buf.memory = V4L2_MEMORY_DMABUF;
            buf.m.planes = planes_buffer;
            buf.length = 1;
            buf.index = queried;

            if (-1 == ioctl(video_dev_fd, VIDIOC_QUERYBUF, &buf))
            {
                log_error("VIDIOC_QUERYBUF failed: %s", strerror(errno));
                break;
            }
            if (planes_buffer->length < plane->sizeimage || planes_buffer->length > input_buffer_size)
            {
                log_error("Capture buffer %d requires %u bytes, expected %u to %u",
                          queried, planes_buffer->length, plane->sizeimage, input_buffer_size);
                break;
            }
            log_info("VIDIOC_QUERYBUF successful for buffer %d", queried);

            log_info("plane: length = %d", planes_buffer->length);
            log_info("plane: offset = %d", planes_buffer->m.mem_offset);
        }
        if (queried != input_buffer_count)
        {
            close(video_dev_fd);
            usleep(1000000);
            continue;
        }
        for (int i = 0; i < input_buffer_count; i++)
        {
            struct v4l2_plane *planes_buffer = &buffers[i].plane_buffer;
            MB_BLK blk = RK_MPI_MB_GetMB(memPool, input_buffer_size, RK_FALSE);
            if (blk == NULL)
            {
                log_error("get mb blk failed!");
                goto cleanup;
            }
            log_info("Got memory block for buffer %d", i);

            buffers[i].mb_blk = blk;

            RK_S32 buf_fd = (RK_MPI_MB_Handle2Fd(blk));
            if (buf_fd < 0)
            {
                log_error("RK_MPI_MB_Handle2Fd failed!");
                goto cleanup;
            }
            log_info("Converted memory block to file descriptor for buffer %d", i);
            planes_buffer->m.fd = buf_fd;
        }

        for (int i = 0; i < input_buffer_count; ++i)
        {
            if (queue_capture(video_dev_fd, i, buffers) < 0)
                goto cleanup;
            log_info("VIDIOC_QBUF successful for buffer %d", i);
        }

        if (ioctl(video_dev_fd, VIDIOC_STREAMON, &type) < 0)
        {
            log_error("VIDIOC_STREAMON failed: %s", strerror(errno));
            goto cleanup;
        }
        capture_started = true;

        struct v4l2_plane tmp_plane = {0};

        // Set VENC parameters
        int32_t bitrate = calculate_bitrate(quality_factor, width, height);
        if (use_q1_capture_pool)
        {
            memset(capture_pending, 0, sizeof(capture_pending));
            capture_ack_fd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
            if (capture_ack_fd < 0 || capture_ack_fd >= FD_SETSIZE)
            {
                log_error("Cannot watch VENC completions");
                goto cleanup;
            }
        }
        RK_S32 ret = venc_start(bitrate, bitrate * 3 / 2, width, height, stride_pixels, fps);
        if (ret != RK_SUCCESS)
        {
            log_error("Set VENC parameters failed with %#x", ret);
            goto cleanup;
        }
        encoder_started = true;

        fd_set fds;
        struct timeval tv;
        int r;
        uint32_t num = 0;
        VIDEO_FRAME_INFO_S stFrame;
        memset(&stFrame, 0, sizeof(VIDEO_FRAME_INFO_S));
        stFrame.stVFrame.u32Width = width;
        stFrame.stVFrame.u32Height = height;
        stFrame.stVFrame.u32VirWidth = stride_pixels;
        stFrame.stVFrame.u32VirHeight = RK_ALIGN_16(height);
        stFrame.stVFrame.enField = VIDEO_FIELD_FRAME;
        stFrame.stVFrame.enPixelFormat = RK_FMT_YUV422_YUYV;
        stFrame.stVFrame.enVideoFormat = VIDEO_FORMAT_LINEAR;
        stFrame.stVFrame.enCompressMode = COMPRESS_MODE_NONE;
        stFrame.stVFrame.enDynamicRange = DYNAMIC_RANGE_SDR8;
        stFrame.stVFrame.enColorGamut = color_gamut_for_height(height);

        while (streaming_flag)
        {
            int pending = use_q1_capture_pool ? reclaim_encoded(video_dev_fd, buffers) : 0;
            if (pending < 0 || !streaming_flag)
                break;
            FD_ZERO(&fds);
            if (pending < input_buffer_count)
                FD_SET(video_dev_fd, &fds);
            if (capture_ack_fd >= 0)
                FD_SET(capture_ack_fd, &fds);
            tv.tv_sec = 1;
            tv.tv_usec = 0;

            int maxfd = video_dev_fd > capture_ack_fd ? video_dev_fd : capture_ack_fd;
            r = select(maxfd + 1, &fds, NULL, NULL, &tv);
            if (r == 0)
            {
                if (pending)
                    continue;
                log_info("select timeout");
                ensure_sleep_mode_disabled();
                break;
            }
            if (r == -1)
            {
                if (errno == EINTR)
                {
                    continue;
                }
                log_error("select in video streaming");
                break;
            }
            if (capture_ack_fd >= 0 && FD_ISSET(capture_ack_fd, &fds))
            {
                eventfd_t completions;
                while (eventfd_read(capture_ack_fd, &completions) < 0 && errno == EINTR) {}
            }
            if (!FD_ISSET(video_dev_fd, &fds))
                continue;
            memset(&buf, 0, sizeof(buf));
            buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
            buf.memory = V4L2_MEMORY_DMABUF;
            buf.m.planes = &tmp_plane;
            buf.length = 1;
            if (ioctl(video_dev_fd, VIDIOC_DQBUF, &buf) < 0)
            {
                log_error("VIDIOC_DQBUF failed: %s", strerror(errno));
                break;
            }
            log_trace("got frame, bytesused = %d", tmp_plane.bytesused);
            if (buf.index >= input_buffer_count)
            {
                log_error("Unexpected dequeued buffer index %u", buf.index);
                break;
            }
            if (use_q1_capture_pool && ((buf.flags & V4L2_BUF_FLAG_ERROR) || tmp_plane.data_offset ||
                tmp_plane.bytesused < (uint64_t)plane->bytesperline * height))
            {
                if (queue_capture(video_dev_fd, buf.index, buffers) < 0)
                    break;
                continue;
            }
            stFrame.stVFrame.pMbBlk = buffers[buf.index].mb_blk;
            stFrame.stVFrame.u32TimeRef = num;
            stFrame.stVFrame.u64PTS = get_us();
            if (use_q1_capture_pool)
            {
                pthread_mutex_lock(&capture_ack_lock);
                capture_pending[buf.index].pts = stFrame.stVFrame.u64PTS;
                capture_pending[buf.index].encoded = false;
                pthread_mutex_unlock(&capture_ack_lock);
            }
            bool retried = false;
            RK_S32 send_result;
        retry_send_frame:
            send_result = RK_MPI_VENC_SendFrame(VENC_CHANNEL, &stFrame, 2000);
            if (send_result != RK_SUCCESS)
            {
                if (retried == true)
                {
                    log_error("RK_MPI_VENC_SendFrame retry failed");
                }
                else
                {
                    log_error("RK_MPI_VENC_SendFrame failed,retrying");
                    retried = true;
                    usleep(1000llu);
                    goto retry_send_frame;
                }
            }

            num++;

            if (use_q1_capture_pool && send_result == RK_SUCCESS)
                continue;
            if (use_q1_capture_pool)
            {
                pthread_mutex_lock(&capture_ack_lock);
                capture_pending[buf.index].pts = 0;
                pthread_mutex_unlock(&capture_ack_lock);
            }
            if (queue_capture(video_dev_fd, buf.index, buffers) < 0)
                break;
        }
    cleanup:
        log_info("cleaning up video capture device %s", VIDEO_DEV);
        if (capture_started && ioctl(video_dev_fd, VIDIOC_STREAMOFF, &type) < 0)
        {
            log_error("VIDIOC_STREAMOFF failed: %s", strerror(errno));
        }

        // Explicitly free V4L2 buffer queue
        struct v4l2_requestbuffers req_free;
        memset(&req_free, 0, sizeof(req_free));
        req_free.count = 0;
        req_free.type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
        req_free.memory = V4L2_MEMORY_DMABUF;

        if (ioctl(video_dev_fd, VIDIOC_REQBUFS, &req_free) < 0)
        {
            log_error("Failed to free V4L2 buffers: %s", strerror(errno));
        }

        close(video_dev_fd);
        if (encoder_started)
            venc_stop();
        if (capture_ack_fd >= 0)
        {
            close(capture_ack_fd);
            capture_ack_fd = -1;
        }

        for (int i = 0; i < input_buffer_count; i++)
        {
            if (buffers[i].mb_blk != NULL)
            {
                RK_MPI_MB_ReleaseMB((buffers + i)->mb_blk);
            }
        }

        if (!encoder_started)
            usleep(1000000);
    }

    log_info("video stream thread exiting");

    set_streaming_stopped(true);

    return NULL;
}

void video_shutdown()
{
    if (should_exit == true)
    {
        log_info("shutting down in progress already");
        return;
    }
    video_stop_streaming();

    should_exit = true;
    if (sub_dev_fd > 0)
    {
        shutdown(sub_dev_fd, SHUT_RDWR);
        log_info("Closed sub_dev_fd");
    }

    if (memPool != MB_INVALID_POOLID)
    {
        RK_MPI_MB_DestroyPool(memPool);
    }
    log_info("Destroyed memory pool");

    pthread_mutex_destroy(&streaming_mutex);
    log_info("Destroyed streaming mutex");
}

static void video_start_streaming_locked()
{
    log_info("starting video streaming");
    if (streaming_thread != NULL)
    {
        bool stopped = get_streaming_stopped();
        if (stopped == true) {
            log_error("video streaming already stopped but streaming_thread is not NULL");
            assert(stopped == true);
        }
        log_warn("video streaming already started");
        return;
    }

    pthread_t *new_thread = malloc(sizeof(pthread_t));
    if (new_thread == NULL)
    {
        log_error("Failed to allocate memory for streaming thread");
        return;
    }

    set_streaming_flag(true);
    int result = pthread_create(new_thread, NULL, run_video_stream, NULL);
    if (result != 0)
    {
        log_error("Failed to create streaming thread: %s", strerror(result));
        set_streaming_flag(false);
        free(new_thread);
        return;
    }

    // Only set streaming_thread after successful creation
    streaming_thread = new_thread;
}

void video_start_streaming()
{
    pthread_mutex_lock(&video_lifecycle_mutex);
    video_start_streaming_locked();
    pthread_mutex_unlock(&video_lifecycle_mutex);
}

bool wait_for_streaming_stopped()
{
    int attempts = 0;
    while (attempts < 30) {
        if (get_streaming_stopped() == true) {
            log_info("video streaming stopped after %d attempts", attempts);
            return true;
        }
        usleep(100000); // 100ms
        attempts++;
    }
    log_error("video streaming did not stop after 3s");
    return false;
}

static void video_stop_streaming_locked()
{
    if (streaming_thread == NULL) {
        log_info("video streaming already stopped");
        return;
    }

    log_info("stopping video streaming");
    set_streaming_flag(false);

    log_info("waiting for video streaming thread to exit");
    wait_for_streaming_stopped();

    pthread_join(*streaming_thread, NULL);
    free(streaming_thread);
    streaming_thread = NULL;

    log_info("video streaming stopped");
}

void video_stop_streaming()
{
    pthread_mutex_lock(&video_lifecycle_mutex);
    video_stop_streaming_locked();
    pthread_mutex_unlock(&video_lifecycle_mutex);
}

uint8_t video_get_streaming_status() {
    // streaming flag can be false when stopping streaming
    if (get_streaming_flag() == true) return 1;
    if (get_streaming_stopped() == false) return 2;
    return 0;
}

void video_restart_streaming()
{
    pthread_mutex_lock(&video_lifecycle_mutex);
    uint8_t streaming_status = video_get_streaming_status();
    // 0 = stopped, 1 = running, 2 = stopping

    if (streaming_status == 0)
    {
        log_info("will not restart video streaming because it's stopped");
        pthread_mutex_unlock(&video_lifecycle_mutex);
        return;
    }

    if (streaming_status == 1 || streaming_status == 2) {
        video_stop_streaming_locked();
    }

    if (wait_for_streaming_stopped()) {
        video_start_streaming_locked();
    }
    pthread_mutex_unlock(&video_lifecycle_mutex);
}

void *run_detect_format(void *arg)
{
    struct v4l2_event_subscription sub;
    struct v4l2_event ev;
    struct v4l2_dv_timings dv_timings;

    memset(&sub, 0, sizeof(sub));
    sub.type = V4L2_EVENT_SOURCE_CHANGE;
    if (ioctl(sub_dev_fd, VIDIOC_SUBSCRIBE_EVENT, &sub) == -1)
    {
        log_error("cannot subscribe to event");
        goto exit;
    }

    while (!should_exit)
    {
        ensure_sleep_mode_disabled();

        memset(&dv_timings, 0, sizeof(dv_timings));
        if (ioctl(sub_dev_fd, VIDIOC_QUERY_DV_TIMINGS, &dv_timings) != 0)
        {
            detected_signal = false;
            if (errno == ENOLINK)
            {
                // No timings could be detected because no signal was found.
                log_info("HDMI status: no signal");
                video_report_format(false, "no_signal", 0, 0, 0);
            }
            else if (errno == ENOLCK)
            {
                // The signal was unstable and the hardware could not lock on to it.
                log_info("HDMI status: no lock");
                video_report_format(false, "no_lock", 0, 0, 0);
            }
            else if (errno == ERANGE)
            {
                // Timings were found, but they are out of range of the hardware capabilities.
                log_warn("HDMI status: out of range");
                video_report_format(false, "out_of_range", 0, 0, 0);
            }
            else
            {
                log_error("error VIDIOC_QUERY_DV_TIMINGS: %s", strerror(errno));
                sleep(1);
                continue;
            }
        }
        else if (use_q1_capture_pool ? !q1_capture_timing_fits(dv_timings.bt.width, dv_timings.bt.height) :
                 (uint64_t)dv_timings.bt.width * dv_timings.bt.height * 2 > capture_block_bytes())
        {
            // A YUYV frame of this size, at 2 bytes for each pixel, does not
            // fit a capture buffer.
            log_warn("HDMI status: %ux%u is out of range", dv_timings.bt.width, dv_timings.bt.height);
            bool had_signal = detected_signal;
            detected_signal = false;
            video_report_format(false, "out_of_range", 0, 0, 0);
            if (had_signal)
            {
                // The capture of the previous format stops.
                video_restart_streaming();
            }
        }
        else
        {
            log_info("Active width: %d", dv_timings.bt.width);
            log_info("Active height: %d", dv_timings.bt.height);
            double frames_per_second = (double)dv_timings.bt.pixelclock /
                                       ((dv_timings.bt.height + dv_timings.bt.vfrontporch + dv_timings.bt.vsync +
                                         dv_timings.bt.vbackporch) *
                                        (dv_timings.bt.width + dv_timings.bt.hfrontporch + dv_timings.bt.hsync +
                                         dv_timings.bt.hbackporch));
            log_info("Frames per second: %.2f fps", frames_per_second);

            // Round to nearest integer for the encoder rate control. CVT-RB
            // sources land a touch under (e.g. 119.91); rounding keeps the
            // encoder sized for the nominal rate.
            uint32_t fps_int = (uint32_t)(frames_per_second + 0.5);
            if (fps_int < 1) fps_int = 1;
            // Tolerate ±1 fps wobble between SOURCE_CHANGE events without
            // tearing down the pipeline.
            bool fps_changed = fps_int > detected_fps + 1 || fps_int + 1 < detected_fps;
            bool should_restart = dv_timings.bt.width != detected_width || dv_timings.bt.height != detected_height || fps_changed || !detected_signal;

            detected_width = dv_timings.bt.width;
            detected_height = dv_timings.bt.height;
            detected_fps = fps_int;
            detected_signal = true;
            video_report_format(true, NULL, detected_width, detected_height, frames_per_second);

            if (should_restart) {
                log_info("restarting video streaming due to format change");
                video_restart_streaming();
            }
        }

        memset(&ev, 0, sizeof(ev));
        if (ioctl(sub_dev_fd, VIDIOC_DQEVENT, &ev) != 0)
        {
            log_error("failed to VIDIOC_DQEVENT: %s", strerror(errno));
            break;
        }
        log_info("New event of type %u", ev.type);
        if (ev.type != V4L2_EVENT_SOURCE_CHANGE)
        {
            continue;
        }
        log_info("source change detected!");
    }
exit:
    close(sub_dev_fd);
    return NULL;
}


void video_set_quality_factor(float factor)
{
    quality_factor = factor;

    // TODO: update venc bitrate without stopping streaming
    video_restart_streaming();
}

float video_get_quality_factor() {
    return quality_factor;
}

void video_set_codec_type(int type) {
    codec_type = type;
}

int video_get_codec_type() {
    return codec_type;
}
