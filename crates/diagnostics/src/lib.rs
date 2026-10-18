//! Compiler diagnostics. Each step returns the diagnostics it finds together
//! with its result; nothing collects them in a shared place.

use std::sync::Arc;

use source::SourceFile;
use source::Span;

use crate::fix::Certainty;
use crate::fix::Fix;

pub mod fix;
pub mod printer;
pub mod suggest;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Severity {
    Error,
    Warning,
    Info,
}

/// What kind of problem a diagnostic reports. Each code is declared once,
/// below, together with its severity, so changing an id cannot change a
/// severity.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Code {
    pub id: &'static str,
    pub severity: Severity,
}

const fn error(id: &'static str) -> Code {
    Code {
        id,
        severity: Severity::Error,
    }
}

const fn warning(id: &'static str) -> Code {
    Code {
        id,
        severity: Severity::Warning,
    }
}

const fn info(id: &'static str) -> Code {
    Code {
        id,
        severity: Severity::Info,
    }
}

pub const UNEXPECTED_CHARACTER: Code = error("L0001");
pub const UNTERMINATED_STRING: Code = error("L0002");
pub const INVALID_NUMBER: Code = error("L0003");
pub const UNTERMINATED_COMMENT: Code = error("L0005");
pub const EXPECTED_TOKEN: Code = error("P0002");
pub const INVALID_EXPRESSION: Code = error("P0003");
pub const INVALID_STATEMENT: Code = error("P0004");
pub const INVALID_DECLARATION: Code = error("P0005");
pub const MISSING_TYPE: Code = error("P0007");
pub const INVALID_TYPE: Code = error("P0011");
pub const INVALID_NUMBER_SUFFIX: Code = error("S0007");
pub const MODULE_NOT_FOUND: Code = error("M0001");
pub const CYCLIC_IMPORT: Code = error("M0002");
pub const INVALID_IMPORT_PATH: Code = error("M0003");
pub const NOT_EXPORTED: Code = error("M0004");
pub const ALIAS_IN_USE: Code = error("M0005");
pub const TYPE_MISMATCH: Code = error("T0001");
pub const UNDEFINED_NAME: Code = error("T0002");
pub const ALREADY_DECLARED: Code = error("T0003");
pub const INVALID_OPERATION: Code = error("T0004");
pub const NOT_CALLABLE: Code = error("T0005");
pub const WRONG_ARGUMENT_COUNT: Code = error("T0006");
pub const INVALID_ASSIGNMENT: Code = error("T0007");
pub const CIRCULAR_DEPENDENCY: Code = error("T0013");
pub const INVALID_RETURN: Code = error("T0016");
pub const MISSING_RETURN: Code = error("T0017");
pub const CONSTANT_REASSIGNMENT: Code = error("T0018");
pub const NOT_A_TYPE: Code = error("T0021");
pub const USED_BEFORE_SET: Code = error("T0039");
pub const NOT_SUPPORTED_YET: Code = error("T0043");
pub const UNREACHABLE_CODE: Code = warning("W0001");
pub const HIDDEN_MUTABLE: Code = warning("W0018");
pub const REDUNDANT_PRELUDE_IMPORT: Code = info("S0004");

/// One message about the source.
#[derive(Clone, Debug)]
pub struct Diagnostic {
    pub code: Code,
    pub message: String,
    pub labels: Vec<Label>, // the first is the primary place
    pub notes: Vec<Note>,
}

/// A span of a file marked with a short message.
#[derive(Clone, Debug)]
pub struct Label {
    pub file: Arc<SourceFile>,
    pub span: Span,
    pub message: String,
}

/// A note or help line. A help line may carry fixes; all of them are applied
/// together, and they make exactly the text the line suggests.
#[derive(Clone, Debug)]
pub struct Note {
    pub help: bool,
    pub text: String,
    pub fixes: Vec<Fix>,
    pub certainty: Certainty, // of the fixes
}

impl Note {
    /// Whether it carries fixes that are sure: what `peeper fix` applies, an
    /// editor prefers and the printer labels `fix`.
    pub fn sure_fix(&self) -> bool {
        !self.fixes.is_empty() && self.certainty == Certainty::Sure
    }
}

impl Diagnostic {
    pub fn new(code: Code, message: impl Into<String>) -> Diagnostic {
        Diagnostic {
            code,
            message: message.into(),
            labels: Vec::new(),
            notes: Vec::new(),
        }
    }

    /// Whether it is an error, which fails the compile.
    pub fn is_error(&self) -> bool {
        self.code.severity == Severity::Error
    }

    /// Adds a place; the first place added is the primary one.
    pub fn label(
        mut self,
        file: &Arc<SourceFile>,
        span: Span,
        message: impl Into<String>,
    ) -> Diagnostic {
        self.labels.push(Label {
            file: file.clone(),
            span,
            message: message.into(),
        });
        self
    }

    pub fn note(mut self, text: impl Into<String>) -> Diagnostic {
        self.notes.push(Note {
            help: false,
            text: text.into(),
            fixes: Vec::new(),
            certainty: Certainty::Sure,
        });
        self
    }

    /// Adds a help line whose `fixes`, if any, are sure.
    pub fn help(mut self, text: impl Into<String>, fixes: Vec<Fix>) -> Diagnostic {
        self.notes.push(Note {
            help: true,
            text: text.into(),
            fixes,
            certainty: Certainty::Sure,
        });
        self
    }

    /// Adds a help line whose `fixes` are a guess.
    pub fn guess(mut self, text: impl Into<String>, fixes: Vec<Fix>) -> Diagnostic {
        self.notes.push(Note {
            help: true,
            text: text.into(),
            fixes,
            certainty: Certainty::Guess,
        });
        self
    }
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
}
