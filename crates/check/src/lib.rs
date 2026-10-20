//! Semantic analysis: what each name means, the type of each expression and
//! the value of each constant, built into THIR in one walk per body
//! (docs/ir-design.md). It starts with each module's scope.

pub mod builtin;
mod expression;
pub mod scope;
pub mod signature;
mod statement;
#[cfg(test)]
mod testing;

use diagnostics::Code;
use diagnostics::Diagnostic;
use model::Declared;
use model::DefId;
use model::ModuleId;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use project::Scope;
use source::Span;

use model::Symbol;

/// What checking one declaration needs from the rest of the program. The
/// scheduler implements it: it owns the results of every declaration, so
/// `check` never depends on the scheduler.
pub trait Queries {
    /// The program's types, shared by every declaration.
    fn types(&mut self) -> &mut Types;
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    fn pointer_bits(&self) -> u32;
    /// What checking the declaration `definition` found: a constant's type
    /// and value (`Checker::constant`), else a signature (`Checker::signature`).
    fn declaration(&mut self, definition: DefId) -> Declared;
}

/// Checking declarations of one module.
pub struct Checker<'a, Q: Queries> {
    /// The program's modules and the scope of each, indexed by `ModuleId`.
    pub modules: &'a [project::Module],
    pub current_module: ModuleId, // the module being checked
    pub queries: &'a mut Q,
    pub diagnostics: &'a mut Vec<Diagnostic>,
}

impl<'a, Q: Queries> Checker<'a, Q> {
    /// The module being checked.
    pub fn module(&self) -> &'a project::Module {
        &self.modules[self.current_module.index()]
    }

    /// Where a name the module does not declare is looked up next:
    /// `core:global`, unless the module is it or the program has none.
    fn prelude(&self) -> Option<ModuleId> {
        self.scope().parent
    }

    /// The names the module being checked declares and imports.
    pub fn scope(&self) -> &'a Scope {
        self.module().scope()
    }

    /// Reports the error `message` at `span` of the module, and gives the type
    /// of an error already reported, so nothing more is reported about it.
    fn error(&mut self, code: Code, message: impl Into<String>, span: Span) -> TypeId {
        self.diagnostics.push(Diagnostic::new(code, message).label(
            &self.module().ast.file,
            span,
            "",
        ));
        TypeId::INVALID
    }

    /// What `name` stands for at the top level: in the module's scope, else
    /// in its parent's, and so on out.
    fn global(&self, name: &str) -> Option<Symbol> {
        let mut scope = self.scope();
        loop {
            if let Some(symbol) = scope.get(name) {
                return Some(symbol);
            }
            scope = self.modules[scope.parent?.index()].scope();
        }
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
