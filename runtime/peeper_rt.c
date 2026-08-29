#include "peeper_rt.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>

#ifdef _WIN32
#include <io.h>
#else
#include <unistd.h>
#endif

void *peeper_rt_v1_alloc(size_t size) {
    return malloc(size);
}

void peeper_rt_v1_free(void *ptr) {
    free(ptr);
}

int32_t peeper_rt_v1_printf(const char *format, ...) {
    va_list args;
    va_start(args, format);
    int result = vprintf(format, args);
    va_end(args);
    return (int32_t)result;
}

int32_t peeper_rt_v1_write(int32_t fd, const void *buffer, int32_t length) {
    if (length < 0) {
        return -1;
    }
#ifdef _WIN32
    return (int32_t)_write(fd, buffer, (unsigned int)length);
#else
    return (int32_t)write(fd, buffer, (size_t)length);
#endif
}

int32_t peeper_rt_v1_read(int32_t fd, void *buffer, int32_t length) {
    if (length < 0) {
        return -1;
    }
#ifdef _WIN32
    return (int32_t)_read(fd, buffer, (unsigned int)length);
#else
    return (int32_t)read(fd, buffer, (size_t)length);
#endif
}

void peeper_rt_v1_exit(int32_t code) {
    exit(code);
}
