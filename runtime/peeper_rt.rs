//! The runtime library every program links with. It stands alone, with no
//! standard library, so one `rustc` call builds it for any target; below it
//! is only what each system offers every program.
#![no_std]

use core::cell::UnsafeCell;
use core::ffi::c_void;
use core::fmt::Write;

unsafe extern "C" {
    fn malloc(size: usize) -> *mut c_void;
    fn free(pointer: *mut c_void);
    fn exit(code: i32) -> !;
    fn abort() -> !;
}

#[cfg(not(windows))]
unsafe extern "C" {
    fn write(fd: i32, buffer: *const c_void, length: usize) -> isize;
    fn read(fd: i32, buffer: *mut c_void, length: usize) -> isize;
}

#[cfg(windows)]
unsafe extern "C" {
    fn _write(fd: i32, buffer: *const c_void, length: u32) -> i32;
    fn _read(fd: i32, buffer: *mut c_void, length: u32) -> i32;
    fn _setmode(fd: i32, mode: i32) -> i32;
}

const STDOUT: i32 = 1;
const STDERR: i32 = 2;
const PANIC_EXIT: i32 = 101;

#[panic_handler]
fn on_panic(_: &core::panic::PanicInfo) -> ! {
    unsafe { abort() }
}

/// The `length` bytes at `buffer` written to `fd`, as far as the system
/// took them, or -1.
#[cfg(not(windows))]
unsafe fn system_write(fd: i32, buffer: *const c_void, length: i32) -> i32 {
    unsafe { write(fd, buffer, length as usize) as i32 }
}

// A Windows C runtime writes "\r\n" for each "\n" to a text-mode file; a
// Peeper program writes the bytes it is given, as on every other system.
#[cfg(windows)]
unsafe fn system_write(fd: i32, buffer: *const c_void, length: i32) -> i32 {
    const BINARY: i32 = 0x8000;
    unsafe {
        if fd == STDOUT || fd == STDERR {
            _setmode(fd, BINARY);
        }
        _write(fd, buffer, length as u32)
    }
}

/// What `print` has written and stdout has not yet. A program has one thread.
struct Output(UnsafeCell<([u8; 8192], usize)>);

unsafe impl Sync for Output {}

static OUTPUT: Output = Output(UnsafeCell::new(([0; 8192], 0)));

fn push(bytes: &[u8]) {
    // Only this file's functions reach it, one at a time.
    let (buffer, used) = unsafe { &mut *OUTPUT.0.get() };
    for chunk in bytes.chunks(buffer.len()) {
        if *used + chunk.len() > buffer.len() {
            flush();
        }
        let (buffer, used) = unsafe { &mut *OUTPUT.0.get() };
        buffer[*used..*used + chunk.len()].copy_from_slice(chunk);
        *used += chunk.len();
    }
}

fn flush() {
    let (buffer, used) = unsafe { &mut *OUTPUT.0.get() };
    let mut written = 0;
    while written < *used {
        let rest = &buffer[written..*used];
        let count = unsafe { system_write(STDOUT, rest.as_ptr().cast(), rest.len() as i32) };
        if count <= 0 {
            break; // the same loss as a C stream whose file is gone
        }
        written += count as usize;
    }
    *used = 0;
}

/// Writes `value` in decimal, at least `width` digits, zeros first.
fn push_decimal(mut value: u64, width: usize) {
    let mut digits = [b'0'; 20];
    let mut at = digits.len();
    while value != 0 {
        at -= 1;
        digits[at] = b'0' + (value % 10) as u8;
        value /= 10;
    }
    push(&digits[at.min(digits.len() - width)..]);
}

#[unsafe(no_mangle)]
pub extern "C" fn peeper_rt_v1_alloc(size: usize) -> *mut c_void {
    unsafe { malloc(size) }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn peeper_rt_v1_free(pointer: *mut c_void) {
    unsafe { free(pointer) }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn peeper_rt_v1_write(fd: i32, buffer: *const c_void, length: i32) -> i32 {
    if length < 0 {
        return -1;
    }
    unsafe { system_write(fd, buffer, length) }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn peeper_rt_v1_read(fd: i32, buffer: *mut c_void, length: i32) -> i32 {
    if length < 0 {
        return -1;
    }
    #[cfg(not(windows))]
    unsafe {
        read(fd, buffer, length as usize) as i32
    }
    #[cfg(windows)]
    unsafe {
        _read(fd, buffer, length as u32)
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn peeper_rt_v1_exit(code: i32) -> ! {
    unsafe { exit(code) }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn peeper_rt_v1_print_text(text: *const u8) {
    if text.is_null() {
        push(b"null");
        return;
    }
    let text = unsafe { core::ffi::CStr::from_ptr(text.cast()) };
    push(text.to_bytes());
}

/// Writes the integer of `count` 64-bit limbs at `limbs`, least significant
/// first, in two's complement when `is_signed`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn peeper_rt_v1_print_int(is_signed: i32, count: i32, limbs: *const u64) {
    let count = count as usize;
    if count == 1 {
        let value = unsafe { limbs.read_unaligned() };
        if is_signed != 0 && (value as i64) < 0 {
            push(b"-");
            push_decimal(value.wrapping_neg(), 1);
        } else {
            push_decimal(value, 1);
        }
        return;
    }
    // The limbs, then the nine-digit chunks of the decimal text, in one
    // block; a program that cannot get it stops as a panic does.
    let chunk_room = count * 3 + 1;
    let bytes = count * size_of::<u64>() + chunk_room * size_of::<u32>();
    let block = unsafe { malloc(bytes) };
    if block.is_null() {
        let message = b"panic: out of memory\n";
        unsafe {
            system_write(STDERR, message.as_ptr().cast(), message.len() as i32);
            exit(PANIC_EXIT)
        }
    }
    let wide = unsafe { core::slice::from_raw_parts_mut(block.cast::<u64>(), count) };
    let chunks =
        unsafe { core::slice::from_raw_parts_mut(wide.as_mut_ptr_range().end.cast(), chunk_room) };
    for (index, limb) in wide.iter_mut().enumerate() {
        *limb = unsafe { limbs.add(index).read_unaligned() };
    }
    let negative = is_signed != 0 && wide[count - 1] >> 63 != 0;
    if negative {
        let mut carry = 1;
        for limb in wide.iter_mut() {
            let (value, overflowed) = (!*limb).overflowing_add(carry);
            *limb = value;
            carry = u64::from(overflowed);
        }
    }
    // Nine decimal digits at a time, least significant first: each limb, in
    // halves of 32 bits, divided by 10^9 with the remainder carried down.
    const CHUNK: u64 = 1_000_000_000;
    let mut chunk_count = 0;
    let mut top = count;
    loop {
        let mut remainder = 0;
        for limb in wide[..top].iter_mut().rev() {
            let high = remainder << 32 | *limb >> 32;
            remainder = high % CHUNK;
            let low = remainder << 32 | (*limb & 0xFFFF_FFFF);
            remainder = low % CHUNK;
            *limb = (high / CHUNK) << 32 | low / CHUNK;
        }
        chunks[chunk_count] = remainder as u32;
        chunk_count += 1;
        while top > 0 && wide[top - 1] == 0 {
            top -= 1;
        }
        if top == 0 {
            break;
        }
    }
    if negative {
        push(b"-");
    }
    push_decimal(u64::from(chunks[chunk_count - 1]), 1);
    for &chunk in chunks[..chunk_count - 1].iter().rev() {
        push_decimal(u64::from(chunk), 9);
    }
    unsafe { free(block) }
}

/// The text of a number, which never fills it.
struct Text {
    bytes: [u8; 48],
    used: usize,
}

impl Write for Text {
    fn write_str(&mut self, text: &str) -> core::fmt::Result {
        self.bytes[self.used..self.used + text.len()].copy_from_slice(text.as_bytes());
        self.used += text.len();
        Ok(())
    }
}

// As Python's `repr`: the fewest digits that read back as the same `f32` or
// `f64` (by `bits`), fixed for an exponent from -4 to 15, else scientific,
// and `.0` on a whole number. Where the last digit is an exact tie, `core`
// rounds up (`224025.625f32` prints `224025.63`), and Python takes the even
// digit (`224025.62`).
#[unsafe(no_mangle)]
pub extern "C" fn peeper_rt_v1_print_float(value: f64, bits: i32) {
    if value.is_nan() {
        push(b"nan");
        return;
    }
    if value.is_infinite() {
        push(if value < 0.0 { b"-inf" } else { b"inf" });
        return;
    }
    let mut text = Text {
        bytes: [0; 48],
        used: 0,
    };
    let formatted = if bits == 32 {
        write!(text, "{:e}", value as f32)
    } else {
        write!(text, "{value:e}")
    };
    if formatted.is_err() {
        unsafe { abort() }
    }
    // `text` is `[-]d[.ddd]e[-]x`: its digits and exponent.
    let text = &text.bytes[..text.used];
    let (sign, text) = match text.split_first() {
        Some((b'-', rest)) => (true, rest),
        _ => (false, text),
    };
    let split = text
        .iter()
        .position(|&byte| byte == b'e')
        .unwrap_or(text.len());
    let (mantissa, exponent_text) = (&text[..split], &text[split + 1..]);
    let mut digits = [0u8; 20];
    let mut count = 0;
    for &byte in mantissa.iter().filter(|&&byte| byte != b'.') {
        digits[count] = byte;
        count += 1;
    }
    let digits = &digits[..count];
    let exponent_digits = exponent_text.strip_prefix(b"-").unwrap_or(exponent_text);
    let magnitude = exponent_digits
        .iter()
        .fold(0i32, |sum, &byte| sum * 10 + i32::from(byte - b'0'));
    let exponent = if exponent_text.len() != exponent_digits.len() {
        -magnitude
    } else {
        magnitude
    };
    if sign {
        push(b"-");
    }
    if !(-4..16).contains(&exponent) {
        push(&digits[..1]);
        if count > 1 {
            push(b".");
            push(&digits[1..]);
        }
        push(if exponent < 0 { b"e-" } else { b"e+" });
        push_decimal(u64::from(exponent.unsigned_abs()), 2);
    } else if exponent < 0 {
        push(b"0.");
        for _ in 1..-exponent {
            push(b"0");
        }
        push(digits);
    } else if count <= exponent as usize + 1 {
        push(digits);
        for _ in count..=exponent as usize {
            push(b"0");
        }
        push(b".0");
    } else {
        let (whole, fraction) = digits.split_at(exponent as usize + 1);
        push(whole);
        push(b".");
        push(fraction);
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn peeper_rt_v1_print_pointer(pointer: *const c_void) {
    push(b"0x");
    let address = pointer as usize as u64;
    let shown = (64 - (address | 1).leading_zeros()).div_ceil(4);
    for nibble in (0..shown).rev() {
        push(&[b"0123456789abcdef"[(address >> (nibble * 4) & 0xF) as usize]]);
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn peeper_rt_v1_flush() {
    flush();
}
