//! Two's-complement integers of any fixed width, from 1 to 2^23 bits, in the
//! manner of LLVM's APInt: a value is only its bits, every operation takes the
//! width, and the operations where sign matters (`divide`, `remainder`,
//! `shift_right`, `compare`) take whether the bits are read as signed.
//! Results wrap to the width.

use std::cmp::Ordering;

/// An integer's bits in 64-bit limbs, lowest first, zero above the width. The
/// same bits are 255 read as unsigned 8 bits and -1 read as signed. Up to 128
/// bits are held inline.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Limbs {
    Inline([u64; 2]),
    Heap(Box<[u64]>),
}

impl Limbs {
    /// The low `bits` bits of `limbs` (lowest first); missing limbs are zero.
    pub fn new(bits: u32, limbs: &[u64]) -> Limbs {
        with_result(bits, |out| {
            let n = out.len().min(limbs.len());
            out[..n].copy_from_slice(&limbs[..n]);
        })
    }

    /// The limbs; an inline value always shows two.
    pub fn as_slice(&self) -> &[u64] {
        match self {
            Limbs::Inline(limbs) => limbs,
            Limbs::Heap(limbs) => limbs,
        }
    }

    fn as_mut_slice(&mut self) -> &mut [u64] {
        match self {
            Limbs::Inline(limbs) => limbs,
            Limbs::Heap(limbs) => limbs,
        }
    }
}

pub fn add(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    let (left, right) = (used(bits, left), used(bits, right));
    with_result(bits, |out| add_into(left, right, out))
}

pub fn subtract(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    let (left, right) = (used(bits, left), used(bits, right));
    with_result(bits, |out| subtract_into(left, right, out))
}

/// Schoolbook multiplication, keeping only the low limbs.
pub fn multiply(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    let (left, right) = (used(bits, left), used(bits, right));
    with_result(bits, |out| {
        let n = out.len();
        for (i, &a) in left.iter().enumerate().filter(|&(_, &a)| a != 0) {
            let mut carry = 0u128;
            for j in 0..n - i {
                let sum = u128::from(a) * u128::from(right[j]) + u128::from(out[i + j]) + carry;
                out[i + j] = sum as u64;
                carry = sum >> 64;
            }
        }
    })
}

pub fn negate(bits: u32, value: &Limbs) -> Limbs {
    let value = used(bits, value);
    with_result(bits, |out| {
        out.copy_from_slice(value);
        negate_in_place(out);
    })
}

/// Every bit flipped.
pub fn not(bits: u32, value: &Limbs) -> Limbs {
    let value = used(bits, value);
    with_result(bits, |out| {
        for (out, &limb) in out.iter_mut().zip(value) {
            *out = !limb;
        }
    })
}

pub fn bit_and(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    each_limb(bits, left, right, |a, b| a & b)
}

pub fn bit_or(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    each_limb(bits, left, right, |a, b| a | b)
}

pub fn bit_xor(bits: u32, left: &Limbs, right: &Limbs) -> Limbs {
    each_limb(bits, left, right, |a, b| a ^ b)
}

/// `value << count`; `count` is below `bits`.
pub fn shift_left(bits: u32, value: &Limbs, count: u32) -> Limbs {
    assert!(count < bits, "a shift count is below the width");
    let value = used(bits, value);
    let (limbs, offset) = ((count / 64) as usize, count % 64);
    let at = |j: usize| if j < limbs { 0 } else { value[j - limbs] };
    with_result(bits, |out| {
        for (i, out) in out.iter_mut().enumerate() {
            let carried = if offset > 0 && i > 0 {
                at(i - 1) >> (64 - offset)
            } else {
                0
            };
            *out = at(i) << offset | carried;
        }
    })
}

/// `value >> count`, bringing in the sign when `signed`; `count` is below
/// `bits`.
pub fn shift_right(bits: u32, signed: bool, value: &Limbs, count: u32) -> Limbs {
    assert!(count < bits, "a shift count is below the width");
    let mut value = used(bits, value).to_vec();
    let negative = is_negative(bits, signed, &value);
    if negative && !bits.is_multiple_of(64) {
        *value.last_mut().unwrap() |= u64::MAX << (bits % 64); // copy the sign into the unused top bits
    }
    let fill = if negative { u64::MAX } else { 0 };
    let (limbs, offset) = ((count / 64) as usize, count % 64);
    let at = |j: usize| value.get(j).copied().unwrap_or(fill);
    with_result(bits, |out| {
        for (i, out) in out.iter_mut().enumerate() {
            let carried = if offset > 0 {
                at(i + limbs + 1) << (64 - offset)
            } else {
                0
            };
            *out = at(i + limbs) >> offset | carried;
        }
    })
}

/// `left / right`, rounded toward zero; `None` when `right` is zero. The signed
/// minimum divided by -1 wraps to the minimum.
pub fn divide(bits: u32, signed: bool, left: &Limbs, right: &Limbs) -> Option<Limbs> {
    let (left, right) = (used(bits, left), used(bits, right));
    let (quotient, _) = divide_magnitudes(bits, signed, left, right)?;
    let negative = is_negative(bits, signed, left) != is_negative(bits, signed, right);
    Some(signed_result(bits, &quotient, negative))
}

/// `left % right`, with the sign of `left`; `None` when `right` is zero.
pub fn remainder(bits: u32, signed: bool, left: &Limbs, right: &Limbs) -> Option<Limbs> {
    let (left, right) = (used(bits, left), used(bits, right));
    let (_, remainder) = divide_magnitudes(bits, signed, left, right)?;
    Some(signed_result(
        bits,
        &remainder,
        is_negative(bits, signed, left),
    ))
}

pub fn compare(bits: u32, signed: bool, left: &Limbs, right: &Limbs) -> Ordering {
    let (left, right) = (used(bits, left), used(bits, right));
    match (
        is_negative(bits, signed, left),
        is_negative(bits, signed, right),
    ) {
        (true, false) => Ordering::Less,
        (false, true) => Ordering::Greater,
        _ => compare_unsigned(left, right),
    }
}

/// The value as a `u64`; `None` when it is negative or does not fit.
pub fn to_u64(bits: u32, signed: bool, value: &Limbs) -> Option<u64> {
    let value = used(bits, value);
    let fits = !is_negative(bits, signed, value) && value[1..].iter().all(|&limb| limb == 0);
    fits.then_some(value[0])
}

fn limb_count(bits: u32) -> usize {
    bits.div_ceil(64) as usize
}

/// The limbs that hold the `bits` bits of `value`.
fn used(bits: u32, value: &Limbs) -> &[u64] {
    &value.as_slice()[..limb_count(bits)]
}

/// Runs `operation` on zeroed limbs for `bits` bits, then clears the bits
/// above the width: what carried out of them is dropped, so results wrap.
fn with_result(bits: u32, operation: impl FnOnce(&mut [u64])) -> Limbs {
    let n = limb_count(bits);
    let mut out = match n {
        0..=2 => Limbs::Inline([0; 2]),
        _ => Limbs::Heap(vec![0; n].into()),
    };
    let limbs = out.as_mut_slice();
    operation(&mut limbs[..n]);
    clear_unused(bits, limbs);
    out
}

fn clear_unused(bits: u32, limbs: &mut [u64]) {
    let n = limb_count(bits);
    if !bits.is_multiple_of(64) {
        limbs[n - 1] &= (1 << (bits % 64)) - 1;
    }
    limbs[n..].fill(0);
}

fn each_limb(bits: u32, left: &Limbs, right: &Limbs, operation: fn(u64, u64) -> u64) -> Limbs {
    let (left, right) = (used(bits, left), used(bits, right));
    with_result(bits, |out| {
        for ((out, &a), &b) in out.iter_mut().zip(left).zip(right) {
            *out = operation(a, b);
        }
    })
}

fn bit(limbs: &[u64], index: usize) -> bool {
    limbs[index / 64] >> (index % 64) & 1 == 1
}

fn is_negative(bits: u32, signed: bool, limbs: &[u64]) -> bool {
    signed && bit(limbs, bits as usize - 1)
}

/// `(|left| / |right|, |left| % |right|)`; `None` when `right` is zero.
fn divide_magnitudes(
    bits: u32,
    signed: bool,
    left: &[u64],
    right: &[u64],
) -> Option<(Vec<u64>, Vec<u64>)> {
    if right.iter().all(|&limb| limb == 0) {
        return None;
    }
    let magnitude = |limbs: &[u64]| {
        let mut out = limbs.to_vec();
        if is_negative(bits, signed, limbs) {
            negate_in_place(&mut out);
            clear_unused(bits, &mut out);
        }
        out
    };
    Some(divide_unsigned(
        &magnitude(left),
        &magnitude(right),
        bits as usize,
    ))
}

/// `magnitude`, negated when `negative`.
fn signed_result(bits: u32, magnitude: &[u64], negative: bool) -> Limbs {
    with_result(bits, |out| {
        out.copy_from_slice(magnitude);
        if negative {
            negate_in_place(out);
        }
    })
}

// Operations on limbs of one length, lowest limb first, wrapping: what carries
// out of the top limb is dropped.

fn add_into(left: &[u64], right: &[u64], out: &mut [u64]) {
    let mut carry = false;
    for ((out, &a), &b) in out.iter_mut().zip(left).zip(right) {
        let (sum, carry_a) = a.overflowing_add(b);
        let (sum, carry_b) = sum.overflowing_add(u64::from(carry));
        (*out, carry) = (sum, carry_a || carry_b);
    }
}

fn subtract_into(left: &[u64], right: &[u64], out: &mut [u64]) {
    let mut borrow = false;
    for ((out, &a), &b) in out.iter_mut().zip(left).zip(right) {
        let (difference, borrow_a) = a.overflowing_sub(b);
        let (difference, borrow_b) = difference.overflowing_sub(u64::from(borrow));
        (*out, borrow) = (difference, borrow_a || borrow_b);
    }
}

/// Invert, then add one.
fn negate_in_place(limbs: &mut [u64]) {
    let mut carry = true;
    for limb in limbs {
        (*limb, carry) = (!*limb).overflowing_add(u64::from(carry));
    }
}

fn compare_unsigned(left: &[u64], right: &[u64]) -> Ordering {
    left.iter().rev().cmp(right.iter().rev())
}

/// `(left / right, left % right)` for unsigned numbers of `bits` bits.
/// shortcut: bit-by-bit long division costs bits × limbs; switch to Knuth's
/// algorithm D if very wide constants show up in profiles.
fn divide_unsigned(left: &[u64], right: &[u64], bits: usize) -> (Vec<u64>, Vec<u64>) {
    let n = left.len();
    let mut quotient = vec![0; n];
    let mut remainder = vec![0; n + 1]; // one limb more: shifting may carry out
    let mut divisor = right.to_vec();
    divisor.push(0);
    let mut scratch = vec![0; n + 1];
    for index in (0..bits).rev() {
        let mut carry = u64::from(bit(left, index));
        for limb in &mut remainder {
            (*limb, carry) = (*limb << 1 | carry, *limb >> 63);
        }
        if compare_unsigned(&remainder, &divisor).is_ge() {
            subtract_into(&remainder, &divisor, &mut scratch);
            remainder.copy_from_slice(&scratch);
            quotient[index / 64] |= 1 << (index % 64);
        }
    }
    remainder.pop();
    (quotient, remainder)
}

#[cfg(test)]
mod tests {
    use std::cmp::Ordering;

    use super::Limbs;

    /// `value` in `bits` bits, sign-extended to every limb before wrapping.
    fn limbs(bits: u32, value: i128) -> Limbs {
        let fill = if value < 0 { u64::MAX } else { 0 };
        let mut limbs = vec![fill; bits.div_ceil(64) as usize + 2];
        limbs[0] = value as u64;
        limbs[1] = (value >> 64) as u64;
        Limbs::new(bits, &limbs)
    }

    #[test]
    fn results_wrap_to_the_width() {
        assert_eq!(super::add(8, &limbs(8, 127), &limbs(8, 1)), limbs(8, -128));
        assert_eq!(super::add(1, &limbs(1, 1), &limbs(1, 1)), limbs(1, 0));
        assert_eq!(super::add(5, &limbs(5, 15), &limbs(5, 1)), limbs(5, -16));
        assert_eq!(
            super::multiply(24, &limbs(24, 1 << 23), &limbs(24, 2)),
            limbs(24, 0)
        );
        assert_eq!(
            super::add(128, &limbs(128, -1), &limbs(128, 1)),
            limbs(128, 0)
        );
        assert_eq!(
            super::add(256, &limbs(256, -1), &limbs(256, 1)),
            limbs(256, 0)
        );
        assert_eq!(super::negate(8, &limbs(8, -128)), limbs(8, -128));
        assert_eq!(super::not(8, &limbs(8, 0)), limbs(8, 255));
        assert_eq!(limbs(8, 255), limbs(8, -1), "one set of bits, two readings");
    }

    #[test]
    fn signed_and_unsigned_readings() {
        let (minus_one, zero) = (limbs(1, -1), limbs(1, 0));
        assert_eq!(super::compare(1, true, &minus_one, &zero), Ordering::Less);
        assert_eq!(
            super::compare(1, false, &minus_one, &zero),
            Ordering::Greater
        );
        let min = limbs(128, i128::MIN);
        assert_eq!(
            super::compare(128, true, &min, &limbs(128, 0)),
            Ordering::Less
        );
        assert_eq!(super::shift_right(5, true, &limbs(5, -16), 2), limbs(5, -4));
        assert_eq!(super::shift_right(5, false, &limbs(5, -16), 2), limbs(5, 4));
        assert_eq!(
            super::shift_right(200, true, &limbs(200, -1), 199),
            limbs(200, -1)
        );
        assert_eq!(super::to_u64(8, true, &limbs(8, -1)), None);
        assert_eq!(super::to_u64(8, false, &limbs(8, -1)), Some(255));
        assert_eq!(super::to_u64(200, false, &limbs(200, 1 << 64)), None);
    }

    #[test]
    fn division_rounds_toward_zero() {
        let divide =
            |bits, left, right| super::divide(bits, true, &limbs(bits, left), &limbs(bits, right));
        let remainder = |bits, left, right| {
            super::remainder(bits, true, &limbs(bits, left), &limbs(bits, right))
        };
        assert_eq!(divide(32, -7, 3), Some(limbs(32, -2)));
        assert_eq!(remainder(32, -7, 3), Some(limbs(32, -1)));
        assert_eq!(divide(8, -128, -1), Some(limbs(8, -128)));
        assert_eq!(remainder(8, -128, -1), Some(limbs(8, 0)));
        assert_eq!(divide(128, i128::MIN, -1), Some(limbs(128, i128::MIN)));
        assert_eq!(divide(200, -21, 4), Some(limbs(200, -5)));
        assert_eq!(remainder(200, -21, 4), Some(limbs(200, -1)));
        assert_eq!(divide(8, 1, 0), None);
        // 2^200 / 2^100 = 2^100, with no remainder
        let power = |exponent| super::shift_left(256, &limbs(256, 1), exponent);
        assert_eq!(
            super::divide(256, false, &power(200), &power(100)),
            Some(power(100))
        );
        assert_eq!(
            super::remainder(256, false, &power(200), &power(100)),
            Some(limbs(256, 0))
        );
    }

    #[test]
    fn storage() {
        let Limbs::Heap(wide) = limbs(200, -1) else {
            panic!("200 bits are on the heap")
        };
        assert_eq!(wide[3], 0xff, "bits above the width are zero");
        assert_eq!(limbs(128, -1).as_slice(), [u64::MAX, u64::MAX]);
        assert_eq!(limbs(64, -1).as_slice(), [u64::MAX, 0]);
    }
}
