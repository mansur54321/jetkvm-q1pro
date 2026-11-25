#include <dlfcn.h>
#include <stdio.h>

int main(int argc, char **argv)
{
    if (argc != 2)
        return 1;
    void *module = dlopen(argv[1], RTLD_NOW);
    if (!module || !dlsym(module, "q1pro_capture")) {
        fprintf(stderr, "%s\n", dlerror());
        return 1;
    }
    return 0;
}
