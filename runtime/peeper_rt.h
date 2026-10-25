#ifndef PEEPER_RT_H
#define PEEPER_RT_H

#include <stddef.h>
#include <stdint.h>

void *peeper_rt_v1_alloc(size_t size);
void peeper_rt_v1_free(void *ptr);
int32_t peeper_rt_v1_printf(const char *format, ...);
int32_t peeper_rt_v1_write(int32_t fd, const void *buffer, int32_t length);
int32_t peeper_rt_v1_read(int32_t fd, void *buffer, int32_t length);
void peeper_rt_v1_exit(int32_t code);

// What `print` compiles to: each value written to stdout's buffer, then a
// flush, so the output keeps its order with `peeper_rt_v1_write`.
void peeper_rt_v1_print_text(const char *text);
void peeper_rt_v1_print_int(int32_t is_signed, int32_t count, ...);
void peeper_rt_v1_print_float(double value, int32_t bits);
void peeper_rt_v1_print_pointer(const void *pointer);
void peeper_rt_v1_flush(void);

#endif
