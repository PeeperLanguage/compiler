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
use model::DefId;
use model::ModuleId;
use model::constant::Constant;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::Span;

use crate::scope::ModuleScope;
use model::Binding;

/// What checking one declaration needs from the rest of the program. The
/// scheduler implements it: it owns the results of every declaration, so
/// `check` never depends on the scheduler.
pub trait Queries {
    /// The program's types, shared by every declaration.
    fn types(&mut self) -> &mut Types;
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    fn pointer_bits(&self) -> u32;
    /// The type of the function `function` (`Checker::signature`).
    fn signature(&mut self, function: DefId) -> TypeId;
    /// The type and value of the constant `constant` (`Checker::constant`);
    /// `None` when it had an error, already reported.
    fn constant(&mut self, constant: DefId) -> Option<Constant>;
}

/// Checking declarations of one module.
pub struct Checker<'a, Q: Queries> {
    /// The program's modules and the scope of each, indexed by `ModuleId`.
    pub modules: &'a [project::Module],
    pub scopes: &'a [ModuleScope],
    pub current_module: ModuleId, // the module being checked
    /// Where a name this module does not declare is looked up next:
    /// `core:global`, unless this module is it or the program has none.
    pub prelude: Option<ModuleId>,
    pub queries: &'a mut Q,
    pub diagnostics: &'a mut Vec<Diagnostic>,
}

impl<'a, Q: Queries> Checker<'a, Q> {
    /// The module being checked.
    pub fn module(&self) -> &'a project::Module {
        &self.modules[self.current_module.index()]
    }

    /// The names the module being checked declares and imports.
    pub fn scope(&self) -> &'a ModuleScope {
        &self.scopes[self.current_module.index()]
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

    /// What `name` stands for at the top level: in this module, else in the
    /// prelude.
    fn global(&self, name: &str) -> Option<Binding> {
        let prelude = || self.scopes[self.prelude?.index()].get(name);
        self.scope().get(name).or_else(prelude)
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
