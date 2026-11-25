#define _GNU_SOURCE
#include <assert.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <poll.h>

static int fake_ioctl(int fd, unsigned long cmd, ...);
static int fake_poll(struct pollfd *fds, nfds_t count, int ms);
#define ioctl fake_ioctl
#define poll fake_poll
#define main capture_cli
#include "tools-frame.c"
#undef main
#undef ioctl
#undef poll

enum scenario { CAPTURE, TIMEOUT, WRONG_FORMAT, OTHER_RESOLUTION, FORMAT_CHANGE, PADDED, MULTIPLANE, WRONG_FOURCC, BUSY, SHORT_FRAME, NO_COPY, STOP_FAILED };
static enum scenario scenario;
static const struct frame_mode *expected_mode, *other_mode;
static unsigned int started, stopped, dequeued, queued, format_writes, allocated;
enum graph_case { LINKED, DISABLED, OTHER_NODE, OTHER_ENTITY, REVERSED, STALE };
static enum graph_case topology_case;

static void fake_graph(struct media_v2_topology *g)
{
    g->num_entities = 1;
    g->num_interfaces = 1;
    g->num_links = 1;
    g->topology_version = topology_case == STALE && g->ptr_entities ? 11 : 10;
    if (!g->ptr_entities) return;
    struct media_v2_entity *entity = (void *)(uintptr_t)g->ptr_entities;
    struct media_v2_interface *iface = (void *)(uintptr_t)g->ptr_interfaces;
    struct media_v2_link *link = (void *)(uintptr_t)g->ptr_links;
    *entity = (struct media_v2_entity){ .id = 33, .function = MEDIA_ENT_F_IO_V4L };
    strcpy(entity->name, topology_case == OTHER_ENTITY ? "stream_cif_mipi_id0" : "rkcif_tools_id0");
    *iface = (struct media_v2_interface){ .id = 50331683, .intf_type = MEDIA_INTF_T_V4L_VIDEO,
        .devnode = { .major = 81, .minor = topology_case == OTHER_NODE ? 0 : 8 } };
    *link = (struct media_v2_link){ .source_id = iface->id, .sink_id = entity->id,
        .flags = MEDIA_LNK_FL_INTERFACE_LINK | (topology_case == DISABLED ? 0 : MEDIA_LNK_FL_ENABLED) };
    if (topology_case == REVERSED) {
        link->source_id = entity->id;
        link->sink_id = iface->id;
    }
}

static int fake_ioctl(int fd, unsigned long cmd, ...)
{
    va_list args;
    va_start(args, cmd);
    void *arg = va_arg(args, void *);
    va_end(args);
    struct v4l2_format *fmt = arg;
    struct v4l2_buffer *buf = arg;
    struct v4l2_requestbuffers *req = arg;
    switch (cmd) {
    case MEDIA_IOC_G_TOPOLOGY:
        fake_graph(arg);
        break;
    case VIDIOC_TRY_FMT: {
        const struct frame_mode *input = scenario == OTHER_RESOLUTION ? other_mode : expected_mode;
        fmt->fmt.pix_mp.width = input->width;
        fmt->fmt.pix_mp.height = input->height;
        fmt->fmt.pix_mp.pixelformat = scenario == WRONG_FOURCC ? V4L2_PIX_FMT_NV12 : V4L2_PIX_FMT_NV16;
        fmt->fmt.pix_mp.num_planes = scenario == MULTIPLANE ? 2 : 1;
        fmt->fmt.pix_mp.plane_fmt[0].bytesperline = input->width + (scenario == PADDED ? 32 : 0);
        fmt->fmt.pix_mp.plane_fmt[0].sizeimage = frame_bytes(input) + (scenario == WRONG_FORMAT);
        break;
    }
    case VIDIOC_S_FMT:
        assert(supported(fmt, expected_mode));
        if (scenario == FORMAT_CHANGE)
            fmt->fmt.pix_mp.width = other_mode->width;
        ++format_writes;
        if (scenario == BUSY) {
            errno = EBUSY;
            return -1;
        }
        break;
    case VIDIOC_REQBUFS:
        assert(req->count == 1 && req->memory == V4L2_MEMORY_MMAP);
        ++allocated;
        break;
    case VIDIOC_QUERYBUF:
        buf->m.planes[0].length = frame_bytes(expected_mode);
        break;
    case VIDIOC_QBUF:
        assert(buf->index == 0);
        ++queued;
        break;
    case VIDIOC_STREAMON:
        assert(queued == 1);
        ++started;
        break;
    case VIDIOC_DQBUF:
        assert(started == 1 && stopped == 0);
        ++dequeued;
        if (scenario != NO_COPY) {
            unsigned char value = 0x93;
            assert(pwrite(fd, &value, sizeof(value), 32) == sizeof(value));
        }
        buf->m.planes[0].bytesused = frame_bytes(expected_mode) - (scenario == SHORT_FRAME);
        break;
    case VIDIOC_STREAMOFF:
        assert(started == 1);
        ++stopped;
        if (scenario == STOP_FAILED) {
            errno = EIO;
            return -1;
        }
        break;
    default:
        assert(0 && "unexpected ioctl on test node");
    }
    return 0;
}

static int fake_poll(struct pollfd *fds, nfds_t count, int ms)
{
    assert(started == 1 && stopped == 0 && count == 1 && ms == 1200);
    if (scenario == TIMEOUT)
        return 0;
    fds[0].revents = POLLIN;
    return 1;
}

int main(void)
{
    for (topology_case = LINKED; topology_case <= STALE; ++topology_case)
        assert(tools_link(0, makedev(81, 8)) == (topology_case == LINKED));
    puts("ok - enabled media link and fresh topology required, wrong node/entity/direction rejected");
    FILE *backing = tmpfile();
    assert(backing);
    for (unsigned int i = 0; i < 2; ++i) {
        expected_mode = &modes[i];
        other_mode = &modes[1-i];
        size_t bytes = frame_bytes(expected_mode);
        assert(bytes == (expected_mode->width == 1280 ? 1843200U : 4147200U));
        assert(ftruncate(fileno(backing), bytes) == 0);
        unsigned char *frame = malloc(bytes);
        assert(frame);
        for (scenario = CAPTURE; scenario <= STOP_FAILED; ++scenario) {
            started = stopped = dequeued = queued = format_writes = allocated = 0;
            int result = capture_one(fileno(backing), frame, expected_mode);
            assert((result == 0) == (scenario == CAPTURE));
            if (scenario == CAPTURE)
                assert(frame[32] == 0x93 && frame[0] == 0xa5 && frame[bytes - 1] == 0xa5);
            assert(started == stopped && dequeued <= 1 && queued <= 1 && allocated <= 1);
            if (scenario >= WRONG_FORMAT && scenario <= BUSY) {
                assert(allocated == 0 && started == 0);
                assert(format_writes == (scenario == FORMAT_CHANGE || scenario == BUSY));
            }
            if (scenario == TIMEOUT)
                assert(stopped == 1 && dequeued == 0);
        }
        printf("ok - %s strict NV16 %zu bytes, other mode and changed S_FMT rejected before allocation\n",
               expected_mode->name, bytes);
        free(frame);
    }
    fclose(backing);
    puts("ok - tools node: one buffer, one frame, tools-only format, timeout/streamoff, unchanged/short frame and busy/error rejection");
    return 0;
}
