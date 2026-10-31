//! How a program's numbers read. The runtime library prints with this file,
//! and `eval`, which runs programs with no library, reads the same file
//! (`#[path]`), so the two cannot print a number differently. It uses only
//! `core`, as the runtime has nothing else, and gives each piece of text to
//! `push`.

use core::fmt::Write;

/// Writes `value` in decimal, at least `width` digits, zeros first.
pub fn write_decimal(mut value: u64, width: usize, push: &mut impl FnMut(&[u8])) {
    let mut digits = [b'0'; 20];
    let mut at = digits.len();
    while value != 0 {
        at -= 1;
        digits[at] = b'0' + (value % 10) as u8;
        value /= 10;
    }
    push(&digits[at.min(digits.len() - width)..]);
}

/// Writes in decimal the integer of the limbs `wide`, least significant first,
/// in two's complement when `is_signed`. `wide` is used up, and `chunks`, of
/// `wide.len() * 3 + 1` elements, holds the digits on the way.
pub fn write_integer(
    is_signed: bool,
    wide: &mut [u64],
    chunks: &mut [u32],
    push: &mut impl FnMut(&[u8]),
) {
    let count = wide.len();
    let negative = is_signed && wide[count - 1] >> 63 != 0;
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
            *limb = ((high / CHUNK) << 32) | (low / CHUNK);
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
    write_decimal(u64::from(chunks[chunk_count - 1]), 1, push);
    for &chunk in chunks[..chunk_count - 1].iter().rev() {
        write_decimal(u64::from(chunk), 9, push);
    }
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
pub fn write_float(value: f64, bits: i32, push: &mut impl FnMut(&[u8])) {
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
    _ = if bits == 32 {
        write!(text, "{:e}", value as f32)
    } else {
        write!(text, "{value:e}")
    };
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
    let exponent = if exponent_text.len() == exponent_digits.len() {
        magnitude
    } else {
        -magnitude
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
        write_decimal(u64::from(exponent.unsigned_abs()), 2, push);
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
