/* This test runs only inside the isolated QEMU virt machine built by run.py. */
#include <errno.h>
#include <fcntl.h>
#include <mtd/ubi-user.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/stat.h>
#include <unistd.h>

static void fail(const char *label) {
    perror(label);
    exit(1);
}
static int fd_open(const char *path, int mode) {
    int fd = open(path, mode);
    if (fd < 0)
        fail(path);
    return fd;
}
static void read_exact(int fd, void *data, size_t count) {
    char *p = data;
    while (count) {
        ssize_t n = read(fd, p, count);
        if (n <= 0)
            fail("read");
        p += n;
        count -= n;
    }
}
static void create(int fd, int id, int64_t size, const char *name) {
    struct ubi_mkvol_req r = {.vol_id = id,
                              .alignment = 1,
                              .bytes = size,
                              .vol_type = UBI_DYNAMIC_VOLUME};
    r.name_len = strlen(name);
    memcpy(r.name, name, r.name_len + 1);
    if (ioctl(fd, UBI_IOCMKVOL, &r) < 0)
        fail("create simulated volume");
}
static void transfer(const char *source, const char *volume, int update,
                     int interrupt) {
    int src = fd_open(source, O_RDONLY);
    int dst = fd_open(volume, update ? O_RDWR : O_RDONLY);
    struct stat st;
    if (fstat(src, &st) < 0)
        fail("stat");
    int64_t size = st.st_size;
    if (update && ioctl(dst, UBI_IOCVOLUP, &size) < 0)
        fail("begin UBI update");
    int64_t remaining = interrupt ? size / 3 : size;
    while (remaining) {
        char a[16384], b[sizeof a];
        size_t n = remaining < (int64_t)sizeof a ? (size_t)remaining : sizeof a;
        read_exact(src, a, n);
        if (update) {
            if (write(dst, a, n) != (ssize_t)n)
                fail("write volume");
        } else {
            read_exact(dst, b, n);
            if (memcmp(a, b, n)) {
                fprintf(stderr, "volume contents differ\n");
                exit(1);
            }
        }
        remaining -= n;
    }
    close(src);
    if (close(dst) < 0)
        fail("close volume");
}
static unsigned int virtual_mtd(void) {
    FILE *mtd = fopen("/proc/mtd", "r");
    if (!mtd)
        fail("/proc/mtd");
    char line[256], name[128];
    unsigned int index, size, erase;
    while (fgets(line, sizeof(line), mtd)) {
        if (sscanf(line, "mtd%u: %x %x %127s", &index, &size, &erase, name) ==
                4 &&
            !strcmp(name, "\"q1-virtual-erase\"") && size == 83 * 1024 * 1024 &&
            erase == 131072) {
            fclose(mtd);
            return index;
        }
    }
    fprintf(stderr,
            "Expected temporary 83 MiB test MTD with 128 KiB erase blocks\n");
    exit(4);
}
int main(int argc, char **argv) {
    if (argc != 2 || access("/q1-virtual-ubi-test", F_OK) || getppid() != 1)
        return 4;
    char model[17] = {0};
    int dt = fd_open("/proc/device-tree/compatible", O_RDONLY);
    read_exact(dt, model, 16);
    close(dt);
    if (strcmp(model, "linux,dummy-virt"))
        return 4;
    int ctl = fd_open("/dev/ubi_ctrl", O_RDWR);
    struct ubi_attach_req attach = {.ubi_num = 0, .mtd_num = virtual_mtd()};
    if (ioctl(ctl, UBI_IOCATT, &attach) < 0)
        fail("attach virtual block2mtd");
    close(ctl);
    int ubi = fd_open("/dev/ubi0", O_RDWR);
    const char *vol = "/dev/ubi0_0", *user = "/dev/ubi0_1";
    if (!strcmp(argv[1], "update")) {
        create(ubi, 0, 26157056, "rootfs");
        create(ubi, 1, 434LL * 126976, "rootfs_data");
        transfer("/sentinel", user, 1, 0);
        transfer("/old", vol, 1, 0);
        transfer("/old", vol, 0, 0);
        transfer("/new", vol, 1, 0);
        transfer("/new", vol, 0, 0);
        transfer("/sentinel", user, 0, 0);
        puts("Q1TEST update verified");
        transfer("/old", vol, 1, 0);
        transfer("/new", vol, 1, 1); /* deliberately interrupted update */
        puts("Q1TEST interrupted");
    } else if (!strcmp(argv[1], "recover")) {
        char byte;
        int fd = fd_open(vol, O_RDONLY);
        errno = 0;
        ssize_t n = read(fd, &byte, 1);
        if (n >= 0 || errno != EBADF) {
            fprintf(stderr, "expected UBI update marker EBADF, got %d/%zd\n",
                    errno, n);
            return 2;
        }
        close(fd);
        puts("Q1TEST interrupted target marked unreadable after cold boot");
        transfer("/old", vol, 1, 0);
        transfer("/old", vol, 0, 0);
        transfer("/sentinel", user, 0, 0);
        puts("Q1TEST recovery verified");
    } else
        return 4;
    close(ubi);
    return 0;
}
