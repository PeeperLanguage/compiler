//! What a feature brings the language: its syntax, its diagnostic codes
//! and the type names it gives. Everything else a feature knows sits in the
//! kinds its rules put on nodes and its types put on type entries.

use model::span::Span;

use crate::syntax::Rule;
use crate::types::TypeKind;

/// A diagnostic code. The language stops the build if two features share one.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Code(pub u16);

pub struct Feature {
    /// Words that are not names, such as `for`; no two features list one.
    pub keywords: &'static [&'static str],
    pub rules: &'static [Rule],
    pub codes: &'static [Code],
    /// Names such as `i32`, which are not keywords.
    pub type_names: &'static [TypeName],
}

pub struct TypeName {
    pub text: &'static str,
    pub kind: &'static TypeKind,
    pub detail: u32,
}

#[derive(Clone, Debug)]
pub struct Refusal {
    pub code: Code,
    pub message: String,
    pub span: Span,
}
