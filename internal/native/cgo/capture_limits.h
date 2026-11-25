#ifndef JETKVM_CAPTURE_LIMITS_H
#define JETKVM_CAPTURE_LIMITS_H

#include <stdbool.h>
#include <stdint.h>

/* YUYV uses two bytes per pixel. Rockit requires the virtual height to be a multiple of 16. */
enum {
    Q1_WIDE_WIDTH = 1920, Q1_WIDE_HEIGHT = 1080,
    Q1_TALL_WIDTH = 1600, Q1_TALL_HEIGHT = 1200,
    Q1_CAPTURE_BLOCK_BYTES = Q1_WIDE_WIDTH * ((Q1_WIDE_HEIGHT + 15) & ~15) * 2
};

static inline bool q1_capture_timing_fits(uint32_t width, uint32_t height)
{
    return width && height &&
        ((width <= Q1_WIDE_WIDTH && height <= Q1_WIDE_HEIGHT) || (width <= Q1_TALL_WIDTH && height <= Q1_TALL_HEIGHT));
}

#endif
