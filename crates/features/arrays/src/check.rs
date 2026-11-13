//! Checking `[N]T` and array literals into the core's array type and
//! literal (`Type::Array`, `ExprKind::ArrayLiteral`).

use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use source::Span;

use crate::ArrayLiteral;
use crate::ArrayType;
use crate::LiteralType;

/// The type `[length]element`, written at `span`: the length is a whole
/// number without a suffix that an array can hold.
pub fn resolve_type<L: Checked>(
    checker: &mut Checker<'_, L>,
    ArrayType { length, element }: ArrayType<L>,
    span: Span,
) -> TypeId {
    let literal = literal::numeric::split(checker.module().ast.file.slice(length));
    let value = literal.integer_value();
    if !literal.suffix.is_empty() || literal.is_float() {
        let message = "an array length is a whole number without a suffix";
        return checker.error(diagnostics::INVALID_TYPE, message, length);
    }
    if !checker.length_fits(&value, length) {
        return TypeId::INVALID;
    }
    let element = checker.resolve_value_type(element);
    checker.array_type(element, value[0], span)
}

/// `[N]T{ entries }`, written at `span`, or `[_]T{ entries }`, whose
/// length is the number of values its entries give: each entry a `T`, or
/// a spread of a range, an array or a slice (`count_spread`). They must
/// give exactly its length. A slice, or a range whose count is not known
/// now, is counted when the program runs, so its literal needs a written
/// length, which the entries known now must not already pass.
pub fn check_literal<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    literal: ArrayLiteral<L>,
    span: Span,
) -> Expr {
    let tree = &builder.checker.module().ast;
    let (element, length, written) = match literal.ty {
        LiteralType::Counted { element } => {
            // `[_]T`, from its `[` to its element's end
            let written = Span {
                start: span.start,
                end: tree.types.span(element).end,
            };
            (builder.checker.resolve_value_type(element), None, written)
        }
        LiteralType::Written(written) => {
            let span = tree.types.span(written);
            match builder.checker.resolve_value_type(written) {
                ty if ty == TypeId::INVALID => (TypeId::INVALID, None, span),
                ty => match builder.checker.types[ty] {
                    Type::Array {
                        element, length, ..
                    } => (element, Some((ty, length)), span),
                    _ => unreachable!("an array literal's written type is an array type"),
                },
            }
        }
    };
    let entries = tree.children.lists.items(literal.entries);
    let (parts, known, later) = builder.check_entries(entries, element, None);
    let Some(known) = known.filter(|_| element != TypeId::INVALID) else {
        return Expr::INVALID;
    };
    let Some(ty) = builder.array_literal_type(written, element, length, (known, later), span)
    else {
        return Expr::INVALID;
    };
    Expr {
        ty,
        kind: ExprKind::ArrayLiteral(builder.body.children.lists.add(&parts)),
    }
}
