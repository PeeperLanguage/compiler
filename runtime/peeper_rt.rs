//! The runtime library every program links with. It stands alone, with no
//! standard library, so one `rustc` call builds it for any target; below it
//! is only what each system offers every program.
#![no_std]

use core::cell::UnsafeCell;
use core::ffi::c_void;
mod number_text;

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

// `compiler_builtins`, in this archive, names it for the helpers a program can
// call (`fmod`, `__modti3`). With `panic=abort` nothing unwinds to call it.
#[unsafe(no_mangle)]
extern "C" fn rust_eh_personality() {}

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

const OUTPUT_SIZE: usize = 8192;

/// What `print` has written and stdout has not yet. A program has one thread.
struct Output(UnsafeCell<([u8; OUTPUT_SIZE], usize)>);

unsafe impl Sync for Output {}

static OUTPUT: Output = Output(UnsafeCell::new(([0; OUTPUT_SIZE], 0)));

fn push(bytes: &[u8]) {
    for chunk in bytes.chunks(OUTPUT_SIZE) {
        // Only this file's functions reach it, and no reference to it may
        // live across `flush`, which makes its own.
        if unsafe { (*OUTPUT.0.get()).1 } + chunk.len() > OUTPUT_SIZE {
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

#[unsafe(no_mangle)]
extern "C" fn peeper_rt_v1_alloc(size: usize) -> *mut c_void {
    unsafe { malloc(size) }
}

#[unsafe(no_mangle)]
unsafe extern "C" fn peeper_rt_v1_free(pointer: *mut c_void) {
    unsafe { free(pointer) }
}

#[unsafe(no_mangle)]
unsafe extern "C" fn peeper_rt_v1_write(fd: i32, buffer: *const c_void, length: i32) -> i32 {
    if length < 0 {
        return -1;
    }
    unsafe { system_write(fd, buffer, length) }
}

#[unsafe(no_mangle)]
unsafe extern "C" fn peeper_rt_v1_read(fd: i32, buffer: *mut c_void, length: i32) -> i32 {
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
extern "C" fn peeper_rt_v1_exit(code: i32) -> ! {
    unsafe { exit(code) }
}

#[unsafe(no_mangle)]
unsafe extern "C" fn peeper_rt_v1_print_text(text: *const u8) {
    if text.is_null() {
        push(b"null");
        return;
    }
    let text = unsafe { core::ffi::CStr::from_ptr(text.cast()) };
    push(text.to_bytes());
}

/// Writes the integer of `count` 64-bit limbs at `limbs`, least significant
/// first, in two's complement when `is_signed`. In v1 the limbs were
/// arguments; the new name makes an old library a link error, not wrong text.
#[unsafe(no_mangle)]
unsafe extern "C" fn peeper_rt_v2_print_int(is_signed: i32, count: i32, limbs: *const u64) {
    let count = count as usize;
    if count == 1 {
        let value = unsafe { limbs.read_unaligned() };
        if is_signed != 0 && (value as i64) < 0 {
            push(b"-");
            number_text::write_decimal(value.wrapping_neg(), 1, &mut push);
        } else {
            number_text::write_decimal(value, 1, &mut push);
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
    number_text::write_integer(is_signed != 0, wide, chunks, &mut push);
    unsafe { free(block) }
}

// `number_text` says how it reads.
#[unsafe(no_mangle)]
extern "C" fn peeper_rt_v1_print_float(value: f64, bits: i32) {
    number_text::write_float(value, bits, &mut push);
}

#[unsafe(no_mangle)]
extern "C" fn peeper_rt_v1_print_pointer(pointer: *const c_void) {
    push(b"0x");
    let address = pointer as usize as u64;
    let shown = (64 - (address | 1).leading_zeros()).div_ceil(4);
    for nibble in (0..shown).rev() {
        push(&[b"0123456789abcdef"[(address >> (nibble * 4) & 0xF) as usize]]);
    }
}

#[unsafe(no_mangle)]
extern "C" fn peeper_rt_v1_flush() {
    flush();
}
