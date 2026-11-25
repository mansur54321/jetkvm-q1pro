#include <errno.h>
#include <fcntl.h>
#include <linux/loop.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <sys/mount.h>
#include <unistd.h>

static void stop(const char *what)
{
    perror(what);
    for (;;)
        pause();
}

int main(void)
{
    struct loop_info64 info = { .lo_flags = LO_FLAGS_READ_ONLY | LO_FLAGS_AUTOCLEAR };
    int control = open("/dev/loop-control", O_RDONLY | O_CLOEXEC);
    if (control < 0)
        stop("Q1 RAM: open loop-control");
    int index = ioctl(control, LOOP_CTL_GET_FREE);
    if (index < 0)
        stop("Q1 RAM: find a free loop device");
    if (index != 0) {
        errno = EBUSY;
        stop("Q1 RAM: expected unused loop0 at init");
    }
    close(control);
    int image = open("/root.img", O_RDONLY | O_CLOEXEC);
    int loop = open("/dev/loop0", O_RDONLY | O_CLOEXEC);
    if (image < 0 || loop < 0)
        stop("Q1 RAM: open embedded root or loop0");
    if (ioctl(loop, LOOP_SET_FD, image) || ioctl(loop, LOOP_SET_STATUS64, &info))
        stop("Q1 RAM: configure read-only loop0");
    if (mount("/dev/loop0", "/image", "squashfs", MS_RDONLY, NULL))
        stop("Q1 RAM: mount embedded root");
    close(loop);
    close(image);
    if (mount("/rcS.ram", "/image/etc/rcS", NULL, MS_BIND, NULL))
        stop("Q1 RAM: bind RAM storage rcS");
    if (mount(NULL, "/image/etc/rcS", NULL, MS_BIND | MS_REMOUNT | MS_RDONLY, NULL))
        stop("Q1 RAM: protect rcS");
    if (chdir("/image") || mount(".", "/", NULL, MS_MOVE, NULL) ||
        chroot(".") || chdir("/"))
        stop("Q1 RAM: enter root");
    puts("Q1 RAM: executing release /sbin/init on read-only loop0");
    fflush(stdout);
    execl("/sbin/init", "init", (char *)NULL);
    stop("Q1 RAM: exec release init");
}
