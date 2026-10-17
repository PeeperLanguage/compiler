//! Semantic analysis: what each name means, the type of each expression and
//! the value of each constant, built into THIR in one walk per body
//! (docs/ir-design.md). It starts with each module's scope.

pub mod builtin;
pub mod scope;
pub mod signature;

use diagnostics::Diagnostic;
use ir::types::TypeId;
use ir::types::Types;
use source::Span;

use crate::scope::ModuleScope;

/// What checking one declaration needs from the rest of the program. The
/// scheduler implements it: it owns the results of every declaration, so
/// `thir` never depends on the scheduler.
pub trait Queries {
    /// The program's types, shared by every declaration.
    fn types(&mut self) -> &mut Types;
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    fn pointer_bits(&self) -> u32;
}

/// Checking declarations of one module.
pub struct Checker<'a, Q: Queries> {
    pub module: &'a project::Module,
    pub scope: &'a ModuleScope,
    pub queries: &'a mut Q,
    pub diagnostics: &'a mut Vec<Diagnostic>,
}

impl<Q: Queries> Checker<'_, Q> {
    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types", and gives the type of an error already reported, so
    /// nothing more is reported about it.
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        self.diagnostics.push(
            Diagnostic::new(
                diagnostics::NOT_SUPPORTED_YET,
                format!("{what} are not supported yet"),
            )
            .label(&self.module.syntax.file, span, ""),
        );
        TypeId::INVALID
    }
}
