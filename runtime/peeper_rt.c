#include "peeper_rt.h"

#include <math.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <fcntl.h>
#include <io.h>

// A Windows C runtime writes "\r\n" for each "\n" to a text-mode file;
// a Peeper program writes the bytes it is given, as on every other system.
__attribute__((constructor)) static void peeper_rt_binary_output(void) {
    _setmode(1, _O_BINARY);
    _setmode(2, _O_BINARY);
}
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

void peeper_rt_v1_print_text(const char *text) {
    fputs(text ? text : "null", stdout);
}

// An integer of `count` 64-bit limbs, least significant first, in two's
// complement when `is_signed`.
void peeper_rt_v1_print_int(int32_t is_signed, int32_t count, ...) {
    va_list args;
    va_start(args, count);
    if (count == 1) {
        uint64_t value = va_arg(args, uint64_t);
        va_end(args);
        if (is_signed) {
            printf("%lld", (long long)value);
        } else {
            printf("%llu", (unsigned long long)value);
        }
        return;
    }
    // The limbs, then the nine-digit chunks of the decimal text, in one
    // block; a program that cannot get it stops as a panic does.
    size_t chunk_bytes = ((size_t)count * 3 + 1) * sizeof(uint32_t);
    uint64_t *limbs = malloc((size_t)count * sizeof *limbs + chunk_bytes);
    if (!limbs) {
        fputs("panic: out of memory\n", stderr);
        exit(101);
    }
    for (int32_t i = 0; i < count; i++) {
        limbs[i] = va_arg(args, uint64_t);
    }
    va_end(args);
    int negative = is_signed && limbs[count - 1] >> 63;
    if (negative) {
        uint64_t carry = 1;
        for (int32_t i = 0; i < count; i++) {
            limbs[i] = ~limbs[i] + carry;
            carry = carry && limbs[i] == 0;
        }
    }
    // Nine decimal digits at a time, least significant first: each limb, in
    // halves of 32 bits, divided by 10^9 with the remainder carried down.
    uint32_t *chunks = (uint32_t *)(limbs + count);
    size_t chunk_count = 0;
    int32_t top = count;
    do {
        uint64_t remainder = 0;
        for (int32_t i = top - 1; i >= 0; i--) {
            uint64_t high = remainder << 32 | limbs[i] >> 32;
            remainder = high % 1000000000;
            uint64_t low = remainder << 32 | (limbs[i] & 0xFFFFFFFF);
            remainder = low % 1000000000;
            limbs[i] = (high / 1000000000) << 32 | low / 1000000000;
        }
        chunks[chunk_count++] = (uint32_t)remainder;
        while (top > 0 && limbs[top - 1] == 0) {
            top--;
        }
    } while (top > 0);
    if (negative) {
        putchar('-');
    }
    printf("%u", chunks[chunk_count - 1]);
    for (size_t i = chunk_count - 1; i-- > 0;) {
        printf("%09u", chunks[i]);
    }
    free(limbs);
}

// As Python's `repr`: the fewest digits that read back as the same `f32` or
// `f64` (by `bits`), fixed for an exponent from -4 to 15, else scientific,
// and `.0` on a whole number.
void peeper_rt_v1_print_float(double value, int32_t bits) {
    if (isnan(value)) {
        fputs("nan", stdout);
        return;
    }
    if (isinf(value)) {
        fputs(value < 0 ? "-inf" : "inf", stdout);
        return;
    }
    char text[40];
    int most = bits == 32 ? 9 : 17;
    for (int precision = 1; precision <= most; precision++) {
        snprintf(text, sizeof text, "%.*e", precision - 1, value);
        double read = bits == 32 ? strtof(text, NULL) : strtod(text, NULL);
        if (read == value) {
            break;
        }
    }
    // `text` is `[-]d[.ddd]e±xx`: its digits and exponent.
    char *e = strchr(text, 'e');
    int exponent = atoi(e + 1);
    char digits[20];
    int count = 0;
    for (char *c = text; c < e; c++) {
        if (*c >= '0' && *c <= '9') {
            digits[count++] = *c;
        }
    }
    while (count > 1 && digits[count - 1] == '0') {
        count--;
    }
    if (text[0] == '-') {
        putchar('-');
    }
    if (exponent < -4 || exponent >= 16) {
        putchar(digits[0]);
        if (count > 1) {
            printf(".%.*s", count - 1, digits + 1);
        }
        printf("e%c%02d", exponent < 0 ? '-' : '+', abs(exponent));
    } else if (exponent < 0) {
        fputs("0.", stdout);
        for (int i = 1; i < -exponent; i++) {
            putchar('0');
        }
        printf("%.*s", count, digits);
    } else if (count <= exponent + 1) {
        printf("%.*s", count, digits);
        for (int i = count; i <= exponent; i++) {
            putchar('0');
        }
        fputs(".0", stdout);
    } else {
        printf("%.*s.%.*s", exponent + 1, digits, count - exponent - 1, digits + exponent + 1);
    }
}

void peeper_rt_v1_print_pointer(const void *pointer) {
    printf("0x%llx", (unsigned long long)(uintptr_t)pointer);
}

void peeper_rt_v1_flush(void) {
    fflush(stdout);
}
