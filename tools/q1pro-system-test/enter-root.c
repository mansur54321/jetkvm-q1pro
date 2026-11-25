#include <stdio.h>
#include <fcntl.h>
#include <string.h>
#include <sys/mount.h>
#include <unistd.h>

static int mount_disks(void)
{
    /* QEMU virt's MMIO slots can enumerate in reverse command-line order. */
    const char *disks[] = {"/dev/vda", "/dev/vdb"};
    const char *root = NULL, *state = NULL;
    for (unsigned int i = 0; i < 2; i++) {
        int fd = open(disks[i], O_RDONLY);
        char bytes[4];
        if (fd < 0) {
            perror(disks[i]);
            return 1;
        }
        if (pread(fd, bytes, 4, 0) == 4 && !memcmp(bytes, "hsqs", 4))
            root = disks[i];
        if (pread(fd, bytes, 2, 1080) == 2 && !memcmp(bytes, "\x53\xef", 2))
            state = disks[i];
        close(fd);
    }
    if (!root || !state || root == state) {
        fprintf(stderr, "Missing SquashFS or ext4 test disk\n");
        return 1;
    }
    if (mount(root, "/image", "squashfs", MS_RDONLY, NULL) ||
        symlink(state, "/dev/q1-test-state")) {
        perror("mount test disks");
        return 1;
    }
    return 0;
}

int main(int argc, char **argv)
{
    if (argc == 2 && !strcmp(argv[1], "--mount-disks"))
        return mount_disks();
    if (chdir("/image") || mount(".", "/", NULL, MS_MOVE, NULL) ||
        chroot(".") || chdir("/")) {
        perror("enter Q1 filesystem");
        return 1;
    }
    execl("/sbin/init", "init", (char *)NULL);
    perror("/sbin/init");
    return 1;
}
