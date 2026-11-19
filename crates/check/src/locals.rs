//! Checking `let` and `const` statements: the variable or constant each
//! declares, and its value.

use arena::Id;
use diagnostics::Diagnostic;
use model::layout;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::builder;
use crate::functions;

impl builder::ThirBuilder<'_, '_> {
    /// `let mut name: ty = value;`. The variable is declared after its value is
    /// checked, so the value cannot use it.
    pub(super) fn check_let_statement(
        &mut self,
        mutable: bool,
        name: ast::Ident,
        ty: Option<Id<ast::Type>>,
        value: Option<Id<ast::Expr>>,
    ) -> thir::Stmt {
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let initialized = value.is_some();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (ty, value.map(|value| self.add_required(value, ty))),
            (None, Some(value)) => {
                let value = self.add_expr(value, None);
                let ty = self.body.exprs[value].ty;
                if ty == semantic_types::TypeId::VOID {
                    let span = self.body.exprs.span(value);
                    let message = "this expression has no value to bind";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    (semantic_types::TypeId::INVALID, None)
                } else {
                    (ty, Some(value))
                }
            }
            (None, None) => {
                let text = name.text(&self.checker.module().ast.file);
                let message = format!("`{text}` needs a type or a value");
                self.checker
                    .error(diagnostics::MISSING_TYPE, message, name.span);
                (semantic_types::TypeId::INVALID, None)
            }
        };
        let declared_variable = thir::Variable {
            ty,
            mutable,
            initialized,
            scope: self.block,
            hidden: false,
        };
        let text = name.text(&self.checker.module().ast.file);
        if let Some(&position) = self.innermost.get(text)
            && position < self.block_start
            && let thir::Local::Variable(outer) = self.names[position].local
            && self.body.variables[outer].mutable
        {
            // Likely meant as `x = ...`: the outer local stays unchanged.
            let file = &self.checker.module().ast.file;
            self.checker.report(
                Diagnostic::new(
                    diagnostics::HIDDEN_MUTABLE,
                    format!("this `let` hides the outer `mut {text}`"),
                )
                .label(
                    file,
                    name.span,
                    format!("a new `{text}`, only inside this block"),
                )
                .label(
                    file,
                    self.body.variables.span(outer),
                    format!("the outer `{text}`, declared `mut` here"),
                )
                .help(
                    format!("to change the outer `{text}`, write `{text} = ...` without `let`"),
                    Vec::new(),
                ),
            );
        }
        let variable = self.body.add(declared_variable, name.span);
        if let Some(value) = value {
            self.body.keep_temporaries(value, variable);
        }
        self.declare(name, thir::Local::Variable(variable));
        thir::Stmt::Let { variable, value }
    }

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives it as the
    /// check knows it (`known_constant`), or `None` after an error.
    pub(crate) fn check_const_statement(
        &mut self,
        declaration: Id<ast::Decl>,
    ) -> Option<thir::CheckedConstant> {
        let ast::Decl::Const {
            name, ty, value, ..
        } = self.checker.module().ast.decls[declaration]
        else {
            unreachable!("`{declaration:?}` is a const declaration")
        };
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let Some(value) = value else {
            let text = name.text(&self.checker.module().ast.file);
            let message = format!("constant `{text}` needs a value");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name.span);
            return None;
        };
        self.check_constant_value(value, declared)
    }

    /// `value`, of the type `declared` when given, as a value known at
    /// compile time: a constant's, or a `const` parameter's argument
    /// (`copy_called`). Calls in it run now. `None` after an error, or when
    /// it is known only when the program runs, reported.
    pub(crate) fn check_constant_value(
        &mut self,
        value: Id<ast::Expr>,
        declared: Option<semantic_types::TypeId>,
    ) -> Option<thir::CheckedConstant> {
        let span = self.checker.module().ast.exprs.span(value);
        let outer = std::mem::replace(&mut self.in_constant, true);
        let checked = match declared {
            Some(ty) => self.check_required(value, ty),
            None => self.check_expr(value, None),
        };
        self.in_constant = outer;
        self.known_constant(checked, span)
    }

    /// `checked`, worked out with `in_constant`, as a constant; reported at
    /// `span` when it is not one. In a generic declaration's own check, one
    /// each copy works out is known by its type only.
    pub(crate) fn known_constant(
        &mut self,
        checked: thir::Expr,
        span: Span,
    ) -> Option<thir::CheckedConstant> {
        match checked.kind {
            thir::ExprKind::Constant(value) => {
                let ty = checked.ty;
                Some(thir::CheckedConstant::Known(semantic_types::Constant {
                    ty,
                    value,
                }))
            }
            thir::ExprKind::Invalid => None, // reported
            _ => {
                let file = self.checker.module().ast.file.clone();
                let unknown = self.body.first_named_variable(&checked.kind);
                // Each copy knows it, unless it reads what only a run knows.
                if unknown.is_none() && self.body.holds_undecided(&checked.kind) {
                    return Some(thir::CheckedConstant::PerCopy(checked.ty));
                }
                let types = &self.checker.types;
                let message = if unknown.is_some() {
                    "the value of a constant must be known at compile time"
                } else if layout::held(types, checked.ty) == layout::Held::Memory
                    || types[checked.ty].borrows() == Some(true)
                    || matches!(types[checked.ty], semantic_types::Type::RawPointer { .. })
                {
                    functions::bodies::constant_refusal(&types[checked.ty])
                } else {
                    "this value cannot be worked out at compile time yet"
                };
                let mut diagnostic =
                    Diagnostic::new(diagnostics::INVALID_OPERATION, message).label(&file, span, "");
                if let Some(variable) = unknown {
                    let declared = self.body.variables.span(variable);
                    let name = file.slice(declared);
                    let label = format!("`{name}` gets its value only when the program runs");
                    diagnostic = diagnostic.label(&file, declared, label);
                }
                self.checker.report(diagnostic);
                None
            }
        }
    }
}
