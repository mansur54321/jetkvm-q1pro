#define _GNU_SOURCE
#include "capture.h"
#include "../../internal/native/cgo/capture_limits.h"
#include <ctype.h>
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <linux/videodev2.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

static volatile sig_atomic_t interrupted;

static void interrupt_capture(int sig)
{
    interrupted = sig;
}

static void deadline(int sig)
{
    (void)sig;
    static const char message[] = "Capture exceeded its 15-second deadline\n";
    write(STDERR_FILENO, message, sizeof(message) - 1);
    _exit(124);
}

/* A preflight snapshot. Keep the service manager from restarting video-server
 * during capture on a system where it is normally supervised. */
static int check_owner(void)
{
    DIR *proc = opendir("/proc");
    if (!proc) {
        perror("/proc");
        return 1;
    }
    bool saw_init = false;
    int result = 0;
    for (;;) {
        errno = 0;
        struct dirent *entry = readdir(proc);
        if (!entry) {
            if (errno) {
                perror("readdir /proc");
                result = 1;
            }
            break;
        }
        if (!isdigit((unsigned char)entry->d_name[0]))
            continue;
        char path[PATH_MAX], comm[64];
        snprintf(path, sizeof(path), "/proc/%s/comm", entry->d_name);
        FILE *file = fopen(path, "r");
        if (!file) {
            if (errno == ENOENT || errno == ESRCH)
                continue;
            perror(path);
            result = 1;
            break;
        }
        bool ok = fgets(comm, sizeof(comm), file) != NULL;
        int read_error = ferror(file) ? errno : 0;
        fclose(file);
        if (!ok) {
            if (!read_error || read_error == ESRCH || read_error == ENOENT)
                continue;
            fprintf(stderr, "%s: %s\n", path, strerror(read_error));
            result = 1;
            break;
        }
        if (!strcmp(entry->d_name, "1"))
            saw_init = true;
        comm[strcspn(comm, "\n")] = 0;
        bool other_capture = !strcmp(comm, "q1pro-capture") && strtol(entry->d_name, NULL, 10) != getpid();
        if (!strcmp(comm, "video-server") || !strcmp(comm, "jetkvm_app") || other_capture) {
            fprintf(stderr, "Video service %s (PID %s) is running; capture is busy\n",
                    comm, entry->d_name);
            result = 3;
            break;
        }
    }
    closedir(proc);
    if (!result && !saw_init) {
        fprintf(stderr, "Cannot verify the process list in /proc\n");
        return 1;
    }
    return result;
}

static int input_format(uint32_t *width, uint32_t *height, uint32_t *fps)
{
    int fd = open("/dev/v4l-subdev2", O_RDONLY | O_NONBLOCK);
    if (fd < 0) {
        perror("open /dev/v4l-subdev2");
        return 4;
    }
    struct v4l2_dv_timings timing = {0};
    if (ioctl(fd, VIDIOC_QUERY_DV_TIMINGS, &timing) < 0) {
        perror("VIDIOC_QUERY_DV_TIMINGS");
        close(fd);
        return 4;
    }
    close(fd);
    struct v4l2_bt_timings *bt = &timing.bt;
    uint64_t columns = (uint64_t)bt->width + bt->hfrontporch + bt->hsync + bt->hbackporch;
    uint64_t rows = (uint64_t)bt->height + bt->vfrontporch + bt->vsync + bt->vbackporch;
    double hz = columns && rows ? (double)bt->pixelclock / columns / rows : 0;
    if (timing.type != V4L2_DV_BT_656_1120 || bt->interlaced ||
        !q1_capture_timing_fits(bt->width, bt->height) ||
        hz < 1 || hz > 240) {
        fprintf(stderr, "Unsupported source timing %ux%u at %.2f Hz\n", bt->width, bt->height, hz);
        return 4;
    }
    *width = bt->width;
    *height = bt->height;
    *fps = (uint32_t)(hz + 0.5);
    fprintf(stderr, "Input %ux%u at %.2f Hz\n", *width, *height, hz);
    return 0;
}

int main(int argc, char **argv)
{
    bool check_only = argc == 2 && !strcmp(argv[1], "--check");
    if (!(check_only || (argc == 2 && !strcmp(argv[1], "--capture")))) {
        fprintf(stderr, "Usage: %s --check | --capture > clip.h264\n", argv[0]);
        return 2;
    }
    signal(SIGALRM, deadline);
    alarm(15);
    int result = check_owner();
    if (result)
        return result;
    uint32_t width, height, fps;
    if ((result = input_format(&width, &height, &fps)) || check_only)
        return result;
    if (isatty(STDOUT_FILENO)) {
        fprintf(stderr, "Redirect stdout to receive the H.264 stream\n");
        return 2;
    }

    char module[PATH_MAX];
    ssize_t size = readlink("/proc/self/exe", module, sizeof(module) - 1);
    if (size < 1 || (size_t)size == sizeof(module) - 1) {
        fprintf(stderr, "Cannot resolve executable path\n");
        return 1;
    }
    module[size] = 0;
    char *name = strrchr(module, '/');
    if (!name || (size_t)(name + 1 - module) + sizeof("q1pro-video.so") > sizeof(module))
        return 1;
    strcpy(name + 1, "q1pro-video.so");

    int output = dup(STDOUT_FILENO);
    if (output < 0 || dup2(STDERR_FILENO, STDOUT_FILENO) < 0) {
        perror("redirect SDK diagnostics");
        return 1;
    }
    signal(SIGPIPE, SIG_IGN);
    signal(SIGTERM, interrupt_capture);
    signal(SIGINT, interrupt_capture);
    void *handle = dlopen(module, RTLD_NOW | RTLD_LOCAL);
    if (!handle) {
        fprintf(stderr, "%s\n", dlerror());
        return 1;
    }
    capture_fn *capture = (capture_fn *)dlsym(handle, "q1pro_capture");
    if (!capture) {
        fprintf(stderr, "%s\n", dlerror());
        return 1;
    }
    if (!(result = check_owner()) && !interrupted)
        result = capture(output, width, height, fps, &interrupted);
    close(output);
    return interrupted ? 128 + interrupted : result;
}
