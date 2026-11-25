#include <assert.h>
#include <linux/videodev2.h>
#include <stdarg.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>

int ioctl(int fd, unsigned long request, ...)
{
    assert(request == VIDIOC_QUERY_DV_TIMINGS);
    va_list args;
    va_start(args, request);
    struct v4l2_dv_timings *timing = va_arg(args, struct v4l2_dv_timings *);
    va_end(args);
    timing->type = V4L2_DV_BT_656_1120;
    timing->bt.width = 1600;
    timing->bt.height = 1200;
    timing->bt.hfrontporch = 64;
    timing->bt.hsync = 192;
    timing->bt.hbackporch = 304;
    timing->bt.vfrontporch = 1;
    timing->bt.vsync = 3;
    timing->bt.vbackporch = 46;
    const char *wide = getenv("Q1_TEST_WIDE");
    if (wide) {
        timing->bt.width = 1920;
        timing->bt.height = strcmp(wide, "oversize") == 0 ? 1200 : 1080;
    }
    timing->bt.pixelclock = 60ULL * (timing->bt.width + 560) * (timing->bt.height + 50);
    return 0;
}
