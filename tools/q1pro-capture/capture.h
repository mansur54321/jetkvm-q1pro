#ifndef Q1PRO_CAPTURE_H
#define Q1PRO_CAPTURE_H

#include <signal.h>
#include <stdint.h>

typedef int capture_fn(int output, uint32_t width, uint32_t height, uint32_t fps,
                       const volatile sig_atomic_t *interrupted);

#endif
