//! Text: string literals, `"..."` and `c"..."`. The checker gives the
//! core's `str` or `cstr` constant, so the phases after it know nothing of
//! this crate. The `str` and `cstr` types are the core's; the core reads a
//! literal's kind through `Language::string_kind` (an `extern`'s name, a
//! `panic` message).

use syntax::ast::StringKind;

pub mod check;
pub mod parse;

/// `"..."`, or `c"..."`; `literal::value` decodes its text
#[derive(Clone, Copy, Debug)]
pub struct StringLiteral {
    pub kind: StringKind,
}

#[cfg(test)]
mod tests;
