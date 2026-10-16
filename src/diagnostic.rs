//! Compiler diagnostics. Each step returns the diagnostics it finds together
//! with its result; nothing collects them in a shared place.

use std::sync::Arc;

use crate::source::{File, Span};

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

pub const UNEXPECTED_CHARACTER: Code = error("L0001");
pub const UNTERMINATED_STRING: Code = error("L0002");
pub const INVALID_NUMBER: Code = error("L0003");
pub const UNTERMINATED_COMMENT: Code = error("L0005");
pub const EXPECTED_TOKEN: Code = error("P0002");
pub const INVALID_EXPRESSION: Code = error("P0003");
pub const INVALID_STATEMENT: Code = error("P0004");
pub const INVALID_DECLARATION: Code = error("P0005");
pub const INVALID_TYPE: Code = error("P0011");
pub const INVALID_NUMBER_SUFFIX: Code = error("S0007");
pub const MODULE_NOT_FOUND: Code = error("M0001");
pub const CYCLIC_IMPORT: Code = error("M0002");
pub const INVALID_IMPORT_PATH: Code = error("M0003");
pub const ALIAS_IN_USE: Code = error("M0005");
pub const REDUNDANT_PRELUDE_IMPORT: Code = Code {
    id: "S0004",
    severity: Severity::Info,
};

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
    pub file: Arc<File>,
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
}

/// Replaces a span of a file with new text: an empty span inserts, empty text
/// removes.
#[derive(Clone, Debug)]
pub struct Fix {
    pub file: Arc<File>,
    pub span: Span,
    pub new_text: String,
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

    /// Adds a place; the first place added is the primary one.
    pub fn label(mut self, file: &Arc<File>, span: Span, message: impl Into<String>) -> Diagnostic {
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
        });
        self
    }

    pub fn help(mut self, text: impl Into<String>, fixes: Vec<Fix>) -> Diagnostic {
        self.notes.push(Note {
            help: true,
            text: text.into(),
            fixes,
        });
        self
    }
}
