//! Checking `let` and `const` statements: the variable or constant each
//! declares, and its value.

use arena::Id;
use diagnostics::Diagnostic;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Variable;

use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;

impl ThirBuilder<'_, '_> {
    /// `let mut name: ty = value;`. The variable is declared after its value is
    /// checked, so the value cannot use it.
    pub(super) fn check_let_statement(
        &mut self,
        mutable: bool,
        name: Ident,
        ty: Option<Id<ast::Type>>,
        value: Option<Id<ast::Expr>>,
    ) -> Stmt {
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let initialized = value.is_some();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (
                ty,
                value.map(|value| self.add_expr(value, TypeExpectation::Required(ty))),
            ),
            (None, Some(value)) => {
                let value = self.add_expr(value, TypeExpectation::Hint(None));
                let ty = self.body.exprs[value].ty;
                if ty == TypeId::VOID {
                    let span = self.body.exprs.span(value);
                    let message = "this expression has no value to bind";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    (TypeId::INVALID, None)
                } else {
                    (ty, Some(value))
                }
            }
            (None, None) => {
                let text = name.text(&self.checker.module().ast.file);
                let message = format!("`{text}` needs a type or a value");
                self.checker
                    .error(diagnostics::MISSING_TYPE, message, name.span);
                (TypeId::INVALID, None)
            }
        };
        let variable = Variable {
            ty,
            mutable,
            initialized,
            scope: self.block,
            hidden: false,
        };
        let text = name.text(&self.checker.module().ast.file);
        let shadowed = self
            .names
            .iter()
            .rposition(|&(declared, ..)| declared == text);
        if let Some(position) = shadowed
            && position < self.block_start
            && let Local::Variable(outer) = self.names[position].1
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
        if let Some(value) = value {
            self.keep_temporary(value);
        }
        let variable = self.body.add(variable, name.span);
        self.declare(name, Local::Variable(variable));
        Stmt::Let { variable, value }
    }

    /// Keeps the temporary that `value`, a `let`'s value, lends itself to the
    /// end of the block, as Rust does for `let r = &make();`. Any other in
    /// `value`, such as a call's argument, still ends with the `let`.
    fn keep_temporary(&mut self, value: Id<Expr>) {
        let (ExprKind::Borrow(place) | ExprKind::Slice { array: place, .. }) =
            self.body.exprs[value].kind
        else {
            return;
        };
        let (start, _) = self.place_root(place);
        if let ExprKind::Temporary { ref mut kept, .. } = self.body.exprs[start].kind {
            *kept = true;
        }
    }

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives its type and
    /// value, or `None` after an error.
    pub(crate) fn check_const_statement(&mut self, declaration: Id<ast::Decl>) -> Option<Constant> {
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
        let span = self.checker.module().ast.exprs.span(value);
        let outer = std::mem::replace(&mut self.in_constant, true);
        let checked = match declared {
            Some(ty) => self.check_expr(value, TypeExpectation::Required(ty)),
            None => self.check_expr(value, TypeExpectation::Hint(None)),
        };
        self.in_constant = outer;
        match checked.kind {
            ExprKind::Constant(value) => Some(Constant {
                ty: checked.ty,
                value,
            }),
            ExprKind::Invalid => None,
            _ => {
                let message = "the value of a constant must be known at compile time";
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
                None
            }
        }
    }
}
