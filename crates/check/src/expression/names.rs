//! Checking a name or a path used as a value: what it stands for.

use model::semantic_types;
use model::thir;
use source::Span;

use crate::builder;

impl builder::ThirBuilder<'_, '_> {
    /// The name or path written at `span`, which stands for `symbol`, as a
    /// value. `None` when finding the symbol reported an error.
    pub(super) fn check_symbol_value(
        &mut self,
        symbol: Option<thir::Symbol>,
        span: Span,
    ) -> thir::Expr {
        let what = match symbol {
            None => return thir::Expr::INVALID, // finding it reported the error
            Some(thir::Symbol::Local(thir::Local::Variable(variable))) => {
                return thir::Expr {
                    ty: self.body.variables[variable].ty,
                    kind: thir::ExprKind::Variable(variable),
                };
            }
            Some(thir::Symbol::Local(thir::Local::Constant(constant))) => {
                return crate::constant_expr(self.body.constants[constant].clone());
            }
            Some(thir::Symbol::Constant(constant)) => {
                let constant = self.checker.constant(constant, span);
                return crate::constant_expr(constant.map(semantic_types::CheckedConstant::Known));
            }
            Some(thir::Symbol::Function(_)) => {
                self.checker.not_supported(span, "functions as values");
                return thir::Expr::INVALID;
            }
            Some(
                thir::Symbol::Type(_)
                | thir::Symbol::Builtin(_)
                | thir::Symbol::TypeArgument { .. },
            ) => "a type",
            Some(thir::Symbol::Module(_)) => "a module",
        };
        let text = self.checker.module().ast.file.slice(span);
        let message = format!("`{text}` is {what}, not a value");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        thir::Expr::INVALID
    }
}
