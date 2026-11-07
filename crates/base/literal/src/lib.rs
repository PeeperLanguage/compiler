//! The rules of literal text that the phases share: the values of string,
//! character and byte literals here, number literals in `numeric`.

pub mod numeric;

use std::borrow::Cow;

/// Reads the escape sequence at `text[i]`, a backslash: Ok holds the byte it
/// stands for, and either way the second value is its length in `text`.
/// `quote_char` is the literal's quote character, the only quote that may be
/// escaped. Every escape stands for one byte: `\n \r \t \0 \\ \" \'` and `\xHH`.
pub fn escape(text: &[u8], i: usize, quote_char: u8) -> (Result<u8, String>, usize) {
    let Some(&c) = text.get(i + 1) else {
        return (Err("unterminated escape sequence".into()), 1);
    };
    match c {
        b'n' => (Ok(b'\n'), 2),
        b'r' => (Ok(b'\r'), 2),
        b't' => (Ok(b'\t'), 2),
        b'0' => (Ok(0), 2),
        b'\\' => (Ok(b'\\'), 2),
        b'"' | b'\'' if c == quote_char => (Ok(c), 2),
        b'"' | b'\'' => (Err(format!("invalid escape sequence \\{}", c as char)), 2),
        b'x' => {
            let digits = text[i + 2..]
                .iter()
                .take(2)
                .take_while(|&&d| d != quote_char && d != b'\n')
                .count();
            if digits < 2 {
                return (
                    Err("hex escape \\x requires exactly two hexadecimal digits".into()),
                    2 + digits,
                );
            }
            let hex = &text[i + 2..i + 4];
            match (
                char::from(hex[0]).to_digit(16),
                char::from(hex[1]).to_digit(16),
            ) {
                (Some(high), Some(low)) => (Ok((high * 16 + low) as u8), 4),
                _ => (
                    Err(format!("invalid hex escape {}", quote(&text[i..i + 4]))),
                    4,
                ),
            }
        }
        _ => {
            let width = next_char_width(&text[i + 1..]);
            (
                Err(format!(
                    "unknown escape sequence {}",
                    quote(&text[i..i + 1 + width])
                )),
                1 + width,
            )
        }
    }
}

/// The length of the UTF-8 character at the start of `s`, or 1 for a byte that
/// starts none.
pub fn next_char_width(s: &[u8]) -> usize {
    s.utf8_chunks().next().map_or(1, |chunk| {
        chunk.valid().chars().next().map_or(1, char::len_utf8)
    })
}

/// The value of a string, character or byte literal token's text. For a literal
/// the lexer reported as invalid it returns its best reading, so the error is
/// reported once, by the lexer.
pub fn value(text: &str) -> Cow<'_, [u8]> {
    let bytes = text.as_bytes();
    // Any prefix, such as `c` or `b`, is before the first quote.
    let prefix = (bytes.iter())
        .position(|&c| c == b'"' || c == b'\'')
        .expect("a literal token has a quote");
    let quote_char = bytes[prefix];
    let inner = &bytes[prefix + 1..];
    let inner = inner.strip_suffix(&[quote_char]).unwrap_or(inner);
    if !inner.contains(&b'\\') {
        return Cow::Borrowed(inner);
    }
    let mut out = Vec::with_capacity(inner.len());
    let mut i = 0;
    while i < inner.len() {
        if inner[i] != b'\\' {
            out.push(inner[i]);
            i += 1;
            continue;
        }
        let (value, width) = escape(inner, i, quote_char);
        match value {
            Ok(b) => out.push(b),
            Err(_) => return Cow::Borrowed(inner),
        }
        i += width;
    }
    Cow::Owned(out)
}

/// Writes `bytes` between double quotes the way messages show source text: `"`
/// and `\` escaped, common control characters as `\n`, `\t` and the like, other
/// control characters and bytes that are not UTF-8 as `\xHH`, and everything
/// else as written.
pub fn quote(bytes: &[u8]) -> String {
    use std::fmt::Write;
    let mut out = String::with_capacity(bytes.len() + 2);
    out.push('"');
    for chunk in bytes.utf8_chunks() {
        for c in chunk.valid().chars() {
            match c {
                '"' => out.push_str("\\\""),
                '\\' => out.push_str("\\\\"),
                '\x07' => out.push_str("\\a"),
                '\x08' => out.push_str("\\b"),
                '\x0c' => out.push_str("\\f"),
                '\n' => out.push_str("\\n"),
                '\r' => out.push_str("\\r"),
                '\t' => out.push_str("\\t"),
                '\x0b' => out.push_str("\\v"),
                c if (c as u32) < 0x80 && c.is_control() => _ = write!(out, "\\x{:02x}", c as u32),
                c if c.is_control() => _ = write!(out, "\\u{:04x}", c as u32),
                c => out.push(c),
            }
        }
        for b in chunk.invalid() {
            _ = write!(out, "\\x{b:02x}");
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quote_like_the_messages() {
        assert_eq!(quote(b"$"), r#""$""#);
        assert_eq!(quote(b"\x0b"), r#""\v""#);
        assert_eq!(quote(b"\\q"), r#""\\q""#);
        assert_eq!(quote("é".as_bytes()), r#""é""#);
        assert_eq!(quote(b"\\x\xc3"), r#""\\x\xc3""#);
        assert_eq!(quote(b"\x01"), r#""\x01""#);
    }

    #[test]
    fn values() {
        for (text, want) in [
            (r#""a\tb""#, &b"a\tb"[..]),
            (r#"c"c""#, b"c"),
            ("'x'", b"x"),
            (r"b'\x41'", b"A"),
            (r#""\xff""#, b"\xff"),
            (r#""open"#, b"open"), // unterminated: reported by the lexer
            (r#""a\q""#, br"a\q"), // bad escape: reported by the lexer
            (r#""\'""#, br"\'"),   // \' is not an escape in a string
        ] {
            assert_eq!(&*value(text), want, "{text}");
        }
    }

    #[test]
    fn escapes() {
        for (text, quote_char, want, width) in [
            (r"\n", b'"', Ok(b'\n'), 2),
            (r"\'", b'\'', Ok(b'\''), 2),
            (r"\'", b'"', Err(r"invalid escape sequence \'"), 2),
            (r"\xZZ", b'"', Err(r#"invalid hex escape "\\xZZ""#), 4),
            (
                r"\x4",
                b'"',
                Err(r"hex escape \x requires exactly two hexadecimal digits"),
                3,
            ),
            (r"\x+1", b'"', Err(r#"invalid hex escape "\\x+1""#), 4),
            (r"\é", b'"', Err(r#"unknown escape sequence "\\é""#), 3),
            ("\\", b'"', Err("unterminated escape sequence"), 1),
        ] {
            let (got, got_width) = escape(text.as_bytes(), 0, quote_char);
            assert_eq!(
                (got.as_ref().copied().map_err(String::as_str), got_width),
                (want, width),
                "{text}"
            );
        }
    }
}
