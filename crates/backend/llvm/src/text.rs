//! The LLVM text of names and lists.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

/// `bytes` in double quotes, as LLVM reads a string: a quote, a backslash
/// and each byte outside printable ASCII as `\XX`.
pub(crate) fn quoted(bytes: &[u8]) -> String {
    let mut text = String::with_capacity(bytes.len() + 2);
    text.push('"');
    for &byte in bytes {
        if byte.is_ascii_graphic() && byte != b'"' && byte != b'\\' || byte == b' ' {
            text.push(byte as char);
        } else {
            _ = write!(text, "\\{byte:02X}");
        }
    }
    text.push('"');
    text
}

/// `symbol` as an LLVM global name.
pub(crate) fn global(symbol: &str) -> String {
    format!("@{}", quoted(symbol.as_bytes()))
}

/// `items` with a comma between each two, written where it is printed.
pub(crate) fn comma_separated<T: Display>(items: impl Iterator<Item = T> + Clone) -> impl Display {
    fmt::from_fn(move |out| {
        for (index, item) in items.clone().enumerate() {
            if index > 0 {
                out.write_str(", ")?;
            }
            item.fmt(out)?;
        }
        Ok(())
    })
}
