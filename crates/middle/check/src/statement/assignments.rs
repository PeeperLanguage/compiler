//! Checking assignments and `return` values: the place written, and
//! whether it can be.

use arena::Id;
use diagnostics::Diagnostic;
use model::ast;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;

use crate::builder::ThirBuilder;

impl ThirBuilder<'_, '_> {
    /// The value of `return value;`, checked against the function's result.
    pub(super) fn check_return_value(
        &mut self,
        span: Span,
        value: Option<Id<ast::Expr>>,
    ) -> Option<Id<Expr>> {
        match value {
            Some(value) if self.result == TypeId::VOID => {
                let message = "this function has no result, so `return` takes no value";
                self.checker.error(
                    diagnostics::INVALID_RETURN,
                    message,
                    self.checker.module().ast.exprs.span(value),
                );
                self.add_expr(value, None); // for its own errors
                None
            }
            Some(value) => {
                let value = self.add_required(value, self.result);
                // `@place` of the function's own storage, gone once it returns.
                // shortcut: only a written `return @x`; an address kept in a
                // variable or a field first (`let p = @x; return p;`) is
                // missed. Track where addresses flow in MIR, as loans do,
                // when that is decided.
                if let ExprKind::Address(place) = self.body.exprs[value].kind
                    && let (start, None) = self.place_root(place)
                    && let ExprKind::Variable(variable) = self.body.exprs[start].kind
                {
                    let file = &self.checker.module().ast.file;
                    let declared = self.body.variables.span(variable);
                    let name = file.slice(declared);
                    self.checker.report(
                        Diagnostic::new(
                            diagnostics::LOCAL_ADDRESS_RETURNED,
                            "cannot return pointer to local storage",
                        )
                        .label(file, self.body.exprs.span(value), "")
                        .label(
                            file,
                            declared,
                            format!("`{name}` is gone once the function returns"),
                        ),
                    );
                }
                Some(value)
            }
            None => {
                if self.result != TypeId::VOID && self.result != TypeId::INVALID {
                    let result = self.checker.type_text(self.result);
                    let message = format!("`return` needs a value of type `{result}`");
                    self.checker
                        .error(diagnostics::INVALID_RETURN, message, span);
                }
                None
            }
        }
    }

    /// `target = value;`, where the target is a mutable variable, a field of
    /// one, or what a `&mut` reference refers to.
    pub(super) fn assign(&mut self, target: Id<ast::Expr>, value: Id<ast::Expr>) -> Option<Stmt> {
        let Some(target) = self.assigned_place(target) else {
            self.add_expr(value, None); // for its own errors
            return None;
        };
        let value = self.add_required(value, self.body.exprs[target].ty);
        Some(Stmt::Assign { target, value })
    }

    /// The variable, field, element or referred value that `target` names,
    /// reporting one that cannot be set (`report_unwritable`). One that cannot
    /// be set is still given, so the value is checked against its type.
    pub(super) fn assigned_place(&mut self, target: Id<ast::Expr>) -> Option<Id<Expr>> {
        let tree = &self.checker.module().ast;
        let place = match tree.exprs[target] {
            ast::Expr::Selector { .. } | ast::Expr::Dereference(_) | ast::Expr::Index { .. } => {
                self.add_expr(target, None)
            }
            _ => {
                let variable = self.assigned_variable(target)?;
                let Variable {
                    ty, initialized, ..
                } = self.body.variables[variable];
                let kind = ExprKind::Variable(variable);
                let place = self.body.add(Expr { ty, kind }, tree.exprs.span(target));
                // Declared without a value, it is set once later; the checks
                // on MIR report one set twice.
                if !initialized {
                    return Some(place);
                }
                place
            }
        };
        if let ExprKind::Invalid = self.body.exprs[place].kind {
            return None; // its error was reported
        }
        let (start, decider) = self.place_root(place);
        let variable = match self.body.exprs[start].kind {
            ExprKind::Variable(variable) => variable,
            // Behind a reference or pointer that a temporary holds.
            ExprKind::Temporary { variable, .. } if decider.is_some() => variable,
            _ => {
                // The value is gone when the statement ends, so nothing could
                // read what is written.
                let message = self
                    .fixed_length(start)
                    .unwrap_or("cannot assign to a temporary value");
                let span = self.body.exprs.span(place);
                self.checker
                    .error(diagnostics::INVALID_ASSIGNMENT, message, span);
                return None;
            }
        };
        self.report_unwritable(place, variable, decider, "assign to");
        // shortcut: the loan check follows no loan stored behind a reference,
        // which would outlive what it borrows unseen; allow it when a
        // signature can say that two parameters share an origin.
        let ty = self.body.exprs[place].ty;
        let writes = decider.is_some_and(|decider| self.writes_through(decider));
        if writes && self.checker.types.holds_loans(ty).is_some() {
            let span = self.body.exprs.span(place);
            let what = "assignments of references through a reference";
            self.checker.not_supported(span, what);
            return None;
        }
        Some(place)
    }

    /// The variable that `target` names, reporting a target that is not a
    /// variable.
    pub(super) fn assigned_variable(&mut self, target: Id<ast::Expr>) -> Option<Id<Variable>> {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(target);
        let symbol = match tree.exprs[target] {
            ast::Expr::Name => self.resolve(span)?,
            ast::Expr::Path(path) => self.checker.resolve_path(path)?,
            // `*p = x;`: reported as a type, with the `p.*` help.
            ast::Expr::Type(_) => {
                self.add_expr(target, None);
                return None;
            }
            _ => {
                self.checker
                    .not_supported(span, "assignments to targets other than names");
                return None;
            }
        };
        let name = tree.file.slice(span);
        let (declared_in, declared) = match symbol {
            Symbol::Local(Local::Variable(variable)) => return Some(variable),
            Symbol::Local(Local::Constant(constant)) => {
                (&tree.file, self.body.constants.span(constant))
            }
            Symbol::Constant(definition) => {
                let (declared, file) =
                    model::program::declared_name(self.checker.modules, definition);
                (file, declared.span)
            }
            _ => {
                let message = format!("cannot assign to `{name}`, which is not a variable");
                self.checker
                    .error(diagnostics::INVALID_ASSIGNMENT, message, span);
                return None;
            }
        };
        self.checker.report(
            Diagnostic::new(
                diagnostics::CONSTANT_REASSIGNMENT,
                format!("cannot assign to constant `{name}`"),
            )
            .label(&tree.file, span, "")
            .label(declared_in, declared, "declared as a constant here"),
        );
        None
    }
}
