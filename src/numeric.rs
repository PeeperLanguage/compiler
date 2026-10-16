//! The rules of number literals that the lexer, the parser and the type checker
//! share. Each phase asks its question here and reports in its own words.

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
}

#[cfg(test)]
mod tests {
    use super::*;

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
}
