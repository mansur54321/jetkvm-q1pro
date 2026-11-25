#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/utsname.h>
#include <unistd.h>

static int read_text(const char *path, char *value, size_t size)
{
    int fd = open(path, O_RDONLY);
    if (fd < 0)
        return -1;
    ssize_t length = read(fd, value, size - 1);
    close(fd);
    if (length <= 0)
        return -1;
    while (length && (value[length - 1] == '\n' || value[length - 1] == '\0'))
        length--;
    for (ssize_t i = 0; i < length; i++) {
        if (value[i] == '\0')
            value[i] = ' ';
    }
    value[length] = '\0';
    return 0;
}

static void print_attr(const char *label, const char *path)
{
    char value[512];
    printf("%s=%s\n", label, read_text(path, value, sizeof(value)) == 0 ? value : "unavailable");
}

/* Use sysfs attributes shared by the BSP and the generic guest kernel. */
static void list_class(const char *path, const char *label, const char *attr)
{
    DIR *dir = opendir(path);
    if (!dir) {
        printf("%s=%s\n", label, errno == ENOENT ? "absent" : "unavailable");
        return;
    }
    unsigned count = 0;
    struct dirent *entry;
    while ((entry = readdir(dir))) {
        if (entry->d_name[0] == '.')
            continue;
        char filename[PATH_MAX], value[128];
        snprintf(filename, sizeof(filename), "%s/%s/%s", path, entry->d_name, attr);
        printf("%s=%s %s=%s\n", label, entry->d_name, attr,
               read_text(filename, value, sizeof(value)) == 0 ? value : "unavailable");
        count++;
    }
    closedir(dir);
    if (!count)
        printf("%s=none\n", label);
}

int main(void)
{
    struct utsname system;
    setvbuf(stdout, NULL, _IONBF, 0);
    puts("Q1 Pro RAM diagnostic");
    if (uname(&system) == 0)
        printf("kernel=%s %s\n", system.release, system.machine);

    unsigned long flags = MS_RDONLY | MS_NOSUID | MS_NODEV | MS_NOEXEC;
    if (mount("proc", "/proc", "proc", flags, NULL) < 0)
        perror("mount /proc");
    if (mount("sysfs", "/sys", "sysfs", flags, NULL) < 0)
        perror("mount /sys");
    print_attr("board", "/sys/firmware/devicetree/base/model");
    print_attr("compatible", "/sys/firmware/devicetree/base/compatible");
    list_class("/sys/class/net", "net", "operstate");
    list_class("/sys/class/udc", "udc", "state");
    printf("dwc3.driver=%s\n",
           access("/sys/bus/platform/drivers/dwc3", F_OK) == 0 ? "registered" : "absent");
    print_attr("nbd0.sectors", "/sys/class/block/nbd0/size");
    puts("q1-ramdiag: idle");
    for (;;)
        pause();
}
