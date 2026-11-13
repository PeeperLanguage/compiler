//! Checking `true` and `false` into the core's bool constant.

use model::constant::Value;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;

use crate::BoolLiteral;

/// The constant `literal` is.
pub fn check_literal(literal: BoolLiteral) -> Expr {
    Expr {
        ty: TypeId::BOOL,
        kind: ExprKind::Constant(Value::Bool(literal.value)),
    }
}
