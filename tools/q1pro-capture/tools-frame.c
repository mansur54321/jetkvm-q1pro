#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/media.h>
#include <linux/videodev2.h>
#include <poll.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>
#include <unistd.h>

struct frame_mode {
    const char *name;
    uint32_t width;
    uint32_t height;
};

static const struct frame_mode modes[] = {
    { "1280x720", 1280, 720 },
    { "1920x1080", 1920, 1080 },
};

static size_t frame_bytes(const struct frame_mode *mode)
{
    return (size_t)mode->width * mode->height * 2;
}

static const enum v4l2_buf_type tools_type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;

static int checked_ioctl(int fd, unsigned long req, void *arg, const char *operation)
{
    int ret = ioctl(fd, req, arg);
    if (ret < 0)
        perror(operation);
    return ret;
}

static bool tools_link(int media_fd, dev_t rdev)
{
    struct media_v2_topology graph = {0};
    struct media_v2_entity entities[64];
    struct media_v2_interface interfaces[64];
    struct media_v2_link links[512];
    if (checked_ioctl(media_fd, MEDIA_IOC_G_TOPOLOGY, &graph, "media topology counts") < 0 ||
        graph.num_entities > 64 || graph.num_interfaces > 64 || graph.num_links > 512)
        return false;
    struct media_v2_topology counts = graph;
    graph.ptr_entities = (uintptr_t)entities;
    graph.ptr_interfaces = (uintptr_t)interfaces;
    graph.ptr_links = (uintptr_t)links;
    if (checked_ioctl(media_fd, MEDIA_IOC_G_TOPOLOGY, &graph, "media topology") < 0 ||
        counts.topology_version != graph.topology_version || counts.num_entities != graph.num_entities ||
        counts.num_interfaces != graph.num_interfaces || counts.num_links != graph.num_links)
        return false;
    uint32_t entity_id = 0, iface_id = 0;
    for (uint32_t i = 0; i < graph.num_entities; ++i) {
        if (entities[i].function == MEDIA_ENT_F_IO_V4L &&
            !strncmp(entities[i].name, "rkcif_tools_id0", sizeof(entities[i].name))) {
            if (entity_id) return false;
            entity_id = entities[i].id;
        }
    }
    for (uint32_t i = 0; i < graph.num_interfaces; ++i) {
        if (interfaces[i].intf_type == MEDIA_INTF_T_V4L_VIDEO &&
            interfaces[i].devnode.major == major(rdev) && interfaces[i].devnode.minor == minor(rdev)) {
            if (iface_id) return false;
            iface_id = interfaces[i].id;
        }
    }
    if (!entity_id || !iface_id) return false;
    for (uint32_t i = 0; i < graph.num_links; ++i) {
        if ((links[i].flags & MEDIA_LNK_FL_LINK_TYPE) == MEDIA_LNK_FL_INTERFACE_LINK &&
            (links[i].flags & MEDIA_LNK_FL_ENABLED) && links[i].source_id == iface_id &&
            links[i].sink_id == entity_id)
            return true;
    }
    return false;
}

static int open_tools(bool read_only)
{
    const char *node = "/dev/video8";
    struct stat st, opened;
    if (lstat(node, &st) < 0 || !S_ISCHR(st.st_mode)) {
        fprintf(stderr, "Missing %s character device\n", node);
        return -1;
    }
    int media_fd = open("/dev/media0", O_RDONLY | O_NONBLOCK | O_CLOEXEC | O_NOFOLLOW);
    if (media_fd < 0) {
        perror("/dev/media0");
        return -1;
    }
    bool verified = tools_link(media_fd, st.st_rdev);
    close(media_fd);
    if (!verified) {
        fprintf(stderr, "No enabled link from %u:%u to rkcif_tools_id0 in /dev/media0\n",
                major(st.st_rdev), minor(st.st_rdev));
        return -1;
    }
    int fd = open(node, (read_only ? O_RDONLY : O_RDWR) | O_NONBLOCK | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) {
        perror(node);
        return -1;
    }
    if (fstat(fd, &opened) < 0 || !S_ISCHR(opened.st_mode) || opened.st_rdev != st.st_rdev)
        goto refuse;

    struct v4l2_capability cap = {0};
    if (checked_ioctl(fd, VIDIOC_QUERYCAP, &cap, "tools QUERYCAP") < 0)
        goto refuse;
    uint32_t caps = cap.capabilities & V4L2_CAP_DEVICE_CAPS ? cap.device_caps : cap.capabilities;
    if (strcmp((char *)cap.driver, "rkcif") ||
        strcmp((char *)cap.bus_info, "platform:rkcif-mipi-lvds") ||
        !(caps & V4L2_CAP_VIDEO_CAPTURE_MPLANE) || !(caps & V4L2_CAP_STREAMING))
        goto refuse;
    return fd;
refuse:
    fprintf(stderr, "Cannot verify rkcif_tools_id0 at %s\n", node);
    close(fd);
    return -1;
}

static bool supported(const struct v4l2_format *fmt, const struct frame_mode *mode)
{
    const struct v4l2_pix_format_mplane *pix = &fmt->fmt.pix_mp;
    return pix->width == mode->width && pix->height == mode->height &&
           pix->pixelformat == V4L2_PIX_FMT_NV16 && pix->num_planes == 1 &&
           pix->plane_fmt[0].bytesperline == mode->width &&
           pix->plane_fmt[0].sizeimage == frame_bytes(mode);
}

static int try_format(int fd, struct v4l2_format *fmt, const struct frame_mode *mode)
{
    fmt->type = tools_type;
    fmt->fmt.pix_mp.pixelformat = V4L2_PIX_FMT_NV16;
    fmt->fmt.pix_mp.width = mode->width;
    fmt->fmt.pix_mp.height = mode->height;
    if (checked_ioctl(fd, VIDIOC_TRY_FMT, fmt, "tools TRY_FMT") < 0)
        return 1;
    if (!supported(fmt, mode)) {
        const struct v4l2_pix_format_mplane *actual = &fmt->fmt.pix_mp;
        fprintf(stderr, "Expected NV16 %s stride %u bytes %zu; input returned %ux%u stride %u bytes %u\n",
                mode->name, mode->width, frame_bytes(mode), actual->width, actual->height,
                actual->plane_fmt[0].bytesperline, actual->plane_fmt[0].sizeimage);
        return 1;
    }
    return 0;
}

static int capture_one(int fd, unsigned char *frame, const struct frame_mode *mode)
{
    struct v4l2_format fmt = { .type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE };
    struct v4l2_requestbuffers req = { .count = 1, .type = tools_type, .memory = V4L2_MEMORY_MMAP };
    struct v4l2_plane plane = {0};
    struct v4l2_buffer buf = { .type = tools_type, .memory = V4L2_MEMORY_MMAP, .length = 1, .m.planes = &plane };
    unsigned char *map = MAP_FAILED;
    size_t map_len = 0;
    bool streaming = false;
    int result = 1;
    size_t bytes = frame_bytes(mode);
    if (try_format(fd, &fmt, mode))
        return 1;
    /* Initializes only the tools queue from the primary stream's negotiated format. */
    if (checked_ioctl(fd, VIDIOC_S_FMT, &fmt, "tools S_FMT") < 0)
        return 1;
    if (!supported(&fmt, mode)) {
        fprintf(stderr, "Tools format changed after TRY_FMT; stopping before allocation\n");
        return 1;
    }
    if (checked_ioctl(fd, VIDIOC_REQBUFS, &req, "tools REQBUFS") < 0)
        return 1;
    if (req.count != 1) {
        fprintf(stderr, "Driver allocated %u buffers; expected one\n", req.count);
        goto done;
    }
    if (checked_ioctl(fd, VIDIOC_QUERYBUF, &buf, "tools QUERYBUF") < 0)
        goto done;
    map_len = plane.length;
    long page = sysconf(_SC_PAGESIZE);
    if (buf.length != 1 || page <= 0 || map_len < bytes || map_len - bytes >= (size_t)page) {
        fprintf(stderr, "Unexpected capture buffer length %zu\n", map_len);
        goto done;
    }
    map = mmap(NULL, map_len, PROT_READ | PROT_WRITE, MAP_SHARED, fd, plane.m.mem_offset);
    if (map == MAP_FAILED) {
        perror("mmap tools buffer");
        goto done;
    }
    /* Detect a driver returning this buffer without copying from an unmappable source. */
    memset(map, 0xa5, bytes);
    if (checked_ioctl(fd, VIDIOC_QBUF, &buf, "tools QBUF") < 0 ||
        checked_ioctl(fd, VIDIOC_STREAMON, (void *)&tools_type, "tools STREAMON") < 0)
        goto done;
    streaming = true;

    struct pollfd pfd = { .fd = fd, .events = POLLIN };
    int ready = poll(&pfd, 1, 1200);
    if (ready < 0)
        perror("poll tools frame");
    if (ready != 1 || !(pfd.revents & POLLIN)) {
        fprintf(stderr, "No tools frame within 1200 ms or capture queue error\n");
        goto done;
    }
    if (checked_ioctl(fd, VIDIOC_DQBUF, &buf, "tools DQBUF") < 0)
        goto done;
    if (buf.index != 0 || buf.length != 1 || buf.flags & V4L2_BUF_FLAG_ERROR ||
        plane.data_offset != 0 || plane.bytesused != bytes) {
        fprintf(stderr, "Incomplete tools frame (payload %u bytes, flags 0x%x)\n", plane.bytesused, buf.flags);
        goto done;
    }
    result = 0;
done:
    if (streaming && checked_ioctl(fd, VIDIOC_STREAMOFF, (void *)&tools_type, "tools STREAMOFF") < 0)
        result = 1;
    if (result == 0) {
        size_t i = 0;
        while (i < bytes && map[i] == 0xa5)
            ++i;
        if (i == bytes) {
            fprintf(stderr, "Driver returned the untouched tools buffer\n");
            result = 1;
        } else {
            memcpy(frame, map, bytes);
        }
    }
    if (map != MAP_FAILED && munmap(map, map_len) < 0) {
        perror("munmap tools buffer");
        result = 1;
    }
    /* Closing the only owning fd releases the single vb2 allocation. */
    return result;
}

int main(int argc, char **argv)
{
    const struct frame_mode *mode = NULL;
    if (argc == 3) {
        for (size_t i = 0; i < sizeof(modes) / sizeof(modes[0]); ++i)
            if (!strcmp(argv[2], modes[i].name))
                mode = &modes[i];
    }
    bool check_only = argc == 3 && !strcmp(argv[1], "--check");
    if (!mode || (!check_only && (strcmp(argv[1], "--one") || isatty(STDOUT_FILENO)))) {
        fprintf(stderr, "Usage: %s --check|--one 1280x720|1920x1080 (binary stdout for --one)\n", argv[0]);
        return 2;
    }
    size_t bytes = frame_bytes(mode);
    int fd = open_tools(check_only);
    if (fd < 0)
        return 1;
    if (check_only) {
        struct v4l2_format fmt = {0};
        int ret = try_format(fd, &fmt, mode);
        close(fd);
        if (!ret)
            fprintf(stderr, "Confirmed /dev/media0 -> rkcif_tools_id0 -> /dev/video8, NV16 %s, %zu bytes\n", mode->name, bytes);
        return ret;
    }
    unsigned char *frame = malloc(bytes);
    if (!frame) {
        perror("frame allocation");
        close(fd);
        return 1;
    }
    int ret = capture_one(fd, frame, mode);
    if (close(fd) < 0) {
        perror("close tools node");
        ret = 1;
    }
    if (ret == 0 && (fwrite(frame, 1, bytes, stdout) != bytes || fflush(stdout) == EOF)) {
        perror("write raw frame");
        ret = 1;
    }
    free(frame);
    return ret;
}
