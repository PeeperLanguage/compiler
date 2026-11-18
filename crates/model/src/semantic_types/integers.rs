//! Integer types: which values fit one, how ranges of one count, and
//! the names `isize`, `usize`, `iN` and `uN`.

use std::fmt;

use bigint::Limbs;

use crate::machine;
use crate::mir;

use super::Integer;
use super::Type;

impl fmt::Display for Integer {
    fn fmt(&self, out: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(out, "{}{}", if self.signed { 'i' } else { 'u' }, self.bits)
    }
}

impl Integer {
    /// The integer a `bool` is converted as: 1 bit, unsigned, 1 for `true`.
    pub const BOOL: Integer = Integer {
        signed: false,
        bits: 1,
    };

    /// The cast that makes a value of this type one of the integer type
    /// `to`, each at its own width.
    pub fn cast_to(self, to: Integer) -> mir::Cast {
        if to.bits < self.bits {
            mir::Cast::Truncate
        } else if self.signed {
            mir::Cast::SignExtend
        } else {
            mir::Cast::ZeroExtend
        }
    }

    /// Whether the number `magnitude` (64-bit limbs, lowest first), negated
    /// when `negative`, is a value of this type: `-128` fits `i8`, `128` does
    /// not.
    pub fn fits(self, magnitude: &[u64], negative: bool) -> bool {
        let length = magnitude
            .iter()
            .rposition(|&limb| limb != 0)
            .map_or(0, |top| {
                top as u64 * 64 + u64::from(64 - magnitude[top].leading_zeros())
            });
        let bits = u64::from(self.bits);
        match (self.signed, negative) {
            _ if length == 0 => true, // zero, also written `-0`
            (false, false) => length <= bits,
            (false, true) => false,
            (true, false) => length < bits,
            // Down to -2^(bits-1): shorter, or exactly that power of two.
            (true, true) => {
                let power_of_two = magnitude.iter().map(|limb| limb.count_ones()).sum::<u32>() == 1;
                length < bits || length == bits && power_of_two
            }
        }
    }

    /// Whether a range of this type from the constant `start` to `end`
    /// counts up (`start <= end`), and how far apart the two are, read as
    /// unsigned of the same width, so it never overflows. `range_count`
    /// and lowering's walk of a constant range both start here, so they
    /// agree on how many values it has.
    pub fn range_distance(self, start: &Limbs, end: &Limbs) -> (bool, Limbs) {
        let up = bigint::compare(self.bits, self.signed, start, end).is_le();
        let (near, far) = if up { (start, end) } else { (end, start) };
        (up, bigint::subtract(self.bits, far, near))
    }

    /// How many values a range of this type from the constant `start` to
    /// `end` by `step` has, `end` counted when `inclusive`, as `for` counts
    /// them: one bit wider than the type, so `0..=u64::MAX`, 2^64 values,
    /// is exact. `None` for a zero step.
    pub fn range_count(
        self,
        start: &Limbs,
        end: &Limbs,
        step: &Limbs,
        inclusive: bool,
    ) -> Option<Limbs> {
        let (_, distance) = self.range_distance(start, end);
        self.count_over(&distance, step, inclusive)
    }

    /// How many values a range of this type or its step type has over
    /// `distance` by `step`, as `range_count` gives them.
    pub fn count_over(self, distance: &Limbs, step: &Limbs, inclusive: bool) -> Option<Limbs> {
        let whole = bigint::divide(self.bits, false, distance, step)?;
        let rest = bigint::remainder(self.bits, false, distance, step)?;
        let extra = inclusive || rest.as_slice().iter().any(|&limb| limb != 0);
        let wide = self.bits + 1;
        let whole = bigint::extend(self.bits, false, &whole, wide);
        Some(bigint::add(
            wide,
            &whole,
            &Limbs::new(wide, &[u64::from(extra)]),
        ))
    }
}

pub(super) const fn integer(signed: bool, bits: u32) -> Type {
    Type::Integer(Integer { signed, bits })
}

/// The integer types as wide as a pointer, and whether each is signed.
pub(super) const POINTER_SIZED: [(&str, bool); 2] = [("isize", true), ("usize", false)];

/// The integer type `name` stands for when it is not in `NAMED`: `isize` and
/// `usize` (as wide as a pointer, `pointer_bits`), or `iN` and `uN` for N
/// from 1 to `machine::MAX_INTEGER_BITS`, written without leading zeros.
pub(super) fn integer_named(name: &str, pointer_bits: u32) -> Option<Integer> {
    if let Some(&(_, signed)) = POINTER_SIZED.iter().find(|(text, _)| *text == name) {
        return Some(Integer {
            signed,
            bits: pointer_bits,
        });
    }
    let (signed, width) = match name.split_at_checked(1)? {
        ("i", width) => (true, width),
        ("u", width) => (false, width),
        _ => return None,
    };
    let digits = !width.is_empty() && width.bytes().all(|b| b.is_ascii_digit());
    if !digits || width.starts_with('0') {
        return None; // `u`, `i8x`, `u08`
    }
    let bits = (width.parse().ok()).filter(|&bits| bits <= machine::MAX_INTEGER_BITS)?;
    Some(Integer { signed, bits })
}
