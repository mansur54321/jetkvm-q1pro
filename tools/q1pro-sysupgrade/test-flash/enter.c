#include <unistd.h>
#include <stdio.h>
int main(void) {
    if (chdir("/factory") || chroot(".") || chdir("/")) {perror("enter factory root"); return 1;}
    execl("/bin/sh","sh","/tmp/tests/phase.sh",(char*)0);
    perror("enter stock shell"); return 2;
}
