//! The rules of number literals that the lexer, the parser and the type checker
//! share. Each phase asks its question here and reports in its own words.

use std::str::FromStr;

/// A number literal's text in its parts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Literal<'a> {
    pub base: u32,       // 2, 8, 10 or 16
    pub digits: &'a str, // after any 0x, 0o or 0b prefix, `_` separators kept
    pub suffix: &'a str, // the type suffix, such as i32 or f64, or ""
}

/// Splits a number token's text into its parts. The suffix of a 0x, 0o or 0b
/// number starts at its first `i` or `u` (`f` is a hex digit); that of a decimal
/// number at its first `i`, `u` or `f`, letters a decimal number holds nowhere
/// else.
pub fn split(text: &str) -> Literal<'_> {
    let bytes = text.as_bytes();
    let base = match bytes {
        // Setting bit 0x20 turns an ASCII upper-case letter into lower case.
        [b'0', prefix, _, ..] => match prefix | 0x20 {
            b'x' => 16,
            b'o' => 8,
            b'b' => 2,
            _ => 10,
        },
        _ => 10,
    };
    let (digits, letters): (&str, &[char]) = if base == 10 {
        (text, &['i', 'u', 'f'])
    } else {
        (&text[2..], &['i', 'u'])
    };
    match digits.find(letters) {
        Some(i) => Literal {
            base,
            digits: &digits[..i],
            suffix: &digits[i..],
        },
        None => Literal {
            base,
            digits,
            suffix: "",
        },
    }
}

impl Literal<'_> {
    /// Whether the digits hold at least one digit and only digits of the base,
    /// besides `_` separators and, in a decimal number, the marks of a fraction
    /// and an exponent (`. e E + -`).
    pub fn digits_in_base(&self) -> bool {
        let mut count = 0;
        for c in self.digits.bytes() {
            if c == b'_' || self.base == 10 && b".eE+-".contains(&c) {
                continue;
            }
            match (c as char).to_digit(36) {
                Some(value) if value < self.base => count += 1,
                _ => return false,
            }
        }
        count > 0
    }

    /// Whether the digits are written as a float: a decimal number with a
    /// fraction or an exponent.
    pub fn is_float(&self) -> bool {
        self.base == 10 && self.digits.contains(['.', 'e', 'E'])
    }

    /// Whether the suffix, if there is one, is its letter followed by a size
    /// (i32, not i) and holds no `_`.
    pub fn suffix_well_formed(&self) -> bool {
        self.suffix.is_empty() || self.suffix.len() > 1 && !self.suffix.contains('_')
    }

    /// Whether the suffix names an integer type: its letter is `i` (signed) or
    /// `u` (unsigned); `f` names a float type.
    pub fn integer_suffix(&self) -> bool {
        !self.suffix.is_empty() && !self.suffix.starts_with('f')
    }

    /// The integer's exact value in 64-bit limbs, lowest first, as wide as it
    /// needs. The digits must be valid in the base (`digits_in_base`).
    pub fn integer_value(&self) -> Vec<u64> {
        let mut limbs = vec![0u64];
        for digit in self.digits.chars().filter_map(|c| c.to_digit(self.base)) {
            let mut carry = u128::from(digit);
            for limb in &mut limbs {
                let next = u128::from(*limb) * u128::from(self.base) + carry;
                (*limb, carry) = (next as u64, next >> 64);
            }
            if carry != 0 {
                limbs.push(carry as u64);
            }
        }
        limbs
    }

    /// The decimal number's value in the precision of `T` (`f32` or `f64`),
    /// rounded once from the text; reading an `f32` through an `f64` would
    /// round twice. `None` when the value is too large for `T`, or the number
    /// is not decimal.
    pub fn float_value<T: Copy + FromStr + Into<f64>>(&self) -> Option<T> {
        if self.base != 10 {
            return None;
        }
        let text: String = self.digits.chars().filter(|&c| c != '_').collect();
        let value: T = text.parse().ok()?;
        let finite = value.into().is_finite();
        finite.then_some(value)
    }
}

#[cfg(test)]
mod tests {
    use super::Literal;
    use super::split;

    #[test]
    fn split_parts() {
        for (text, base, digits, suffix) in [
            ("42", 10, "42", ""),
            ("42i32", 10, "42", "i32"),
            ("1_000u8", 10, "1_000", "u8"),
            ("2.5e-3f64", 10, "2.5e-3", "f64"),
            ("0xffu24", 16, "ff", "u24"),
            ("0XAB", 16, "AB", ""),
            ("0o7i", 8, "7", "i"),
            ("0b1_0", 2, "1_0", ""),
            ("0.5", 10, "0.5", ""),
            ("07", 10, "07", ""),
        ] {
            assert_eq!(
                split(text),
                Literal {
                    base,
                    digits,
                    suffix
                },
                "{text}"
            );
        }
    }

    #[test]
    fn questions() {
        for (text, in_base, float, well_formed, integer) in [
            ("42", true, false, true, false),
            ("0xFG", false, false, true, false),
            ("0o8", false, false, true, false),
            ("0b102u8", false, false, true, true),
            ("0xu8", false, false, true, true),
            ("0x1f_u8", true, false, true, true),
            ("1.5", true, true, true, false),
            ("1e3u8", true, true, true, true),
            ("1.5f32", true, true, true, false),
            ("1i", true, false, false, true),
            ("1i_32", true, false, false, true),
            ("1f", true, false, false, false),
        ] {
            let l = split(text);
            assert_eq!(
                (
                    l.digits_in_base(),
                    l.is_float(),
                    l.suffix_well_formed(),
                    l.integer_suffix()
                ),
                (in_base, float, well_formed, integer),
                "{text}"
            );
        }
    }

    #[test]
    fn integer_values() {
        for (text, limbs) in [
            ("0", vec![0]),
            ("1_000", vec![1000]),
            ("0b1010u8", vec![10]),
            ("0o777", vec![511]),
            ("007", vec![7]),
            ("18446744073709551615", vec![u64::MAX]),
            ("18446744073709551616", vec![0, 1]),
            (
                "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffu256",
                vec![u64::MAX; 4],
            ),
        ] {
            assert_eq!(split(text).integer_value(), limbs, "{text}");
        }
    }

    #[test]
    fn float_values() {
        assert_eq!(split("1_000.5").float_value::<f64>(), Some(1000.5));
        assert_eq!(split("2.5e-3f64").float_value::<f64>(), Some(2.5e-3));
        assert_eq!(split("5").float_value::<f32>(), Some(5.0));
        assert_eq!(
            split("1e39").float_value::<f32>(),
            None,
            "too large for f32"
        );
        assert_eq!(split("1e39").float_value::<f64>(), Some(1e39));
        assert_eq!(split("0x10").float_value::<f64>(), None, "not decimal");
        // Halfway between two f32 values, and just above it: read through f64,
        // the text rounds to the halfway f64, which then rounds to the even
        // f32 below; read directly, it rounds up.
        let above_halfway = "1.000000059604644775390625000001";
        assert_eq!(split(above_halfway).float_value::<f32>(), Some(1.0000001));
        assert_eq!(
            above_halfway.parse::<f64>().unwrap() as f32,
            1.0,
            "double rounding"
        );
    }
}
