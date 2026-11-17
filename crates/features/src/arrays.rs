//! Arrays: `[N]T` and the array literals `[N]T{ ... }` and `[_]T{ ... }`.
//! The checker gives the core's array type and literal (`Type::Array`,
//! `ExprKind::ArrayLiteral`), so the phases after it know nothing of
//! them. A literal's entries and their `...` spreads are the core's, as a
//! call to a `...` parameter checks them too; so are slices, read after the
//! core's `&`, and indexing.

use arena::Id;
use check::Checker;
use check::builder::ThirBuilder;
use diagnostics::Diagnostic;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use parser::expr::spreadable;
use parser::expr::starts_expression;
use source::Span;
use syntax::ast;
use token::Token;
use token::TokenKind;

/// Adds the rules that read array types and array literals to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::LeftBracket, array_type);
    rules.prefix(TokenKind::LeftBracket, array_literal);
}

/// Reads the `N]T` of `[N]T`, a type that starts at `start`, with its `[`;
/// a `[]T` alone is reported, as it has no fixed size.
fn array_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let open = Token {
        kind: TokenKind::LeftBracket,
        span: Span {
            start,
            end: start + 1,
        },
    };
    let t = p.tok();
    // `[]T` alone, read whole so the message names it and parsing goes on.
    if t.kind == TokenKind::RightBracket {
        p.next();
        let element = p.type_expr();
        let ty = p.add(start, ast::Type::Invalid);
        // An element reported already, as `[]i32` in `[][]i32`, is the
        // one mistake: its fix fixes the whole.
        if let ast::Type::Invalid = p.tree.types[element] {
            return ty;
        }
        let element = p.tree.file.slice(p.tree.types.span(element));
        let message =
            format!("`[]{element}` is any number of `{element}`s, so it has no fixed size");
        let label = format!("use `[N]{element}` for a fixed array or `&[]{element}` for a slice");
        let span = p.tree.types.span(ty);
        p.repair(Diagnostic::new(diagnostics::INVALID_TYPE, message).label(
            &p.tree.file,
            span,
            label,
        ));
        return ty;
    }
    if t.kind != TokenKind::Number {
        p.fail(
            Diagnostic::new(diagnostics::INVALID_TYPE, "expected array length").label(
                &p.tree.file,
                t.span,
                "use `[N]T` for fixed arrays or `&[]T` for slices",
            ),
        );
        return p.add(start, ast::Type::Invalid);
    }
    p.next();
    p.check_number(t);
    p.close(TokenKind::RightBracket, open);
    let element = p.type_expr();
    let length = t.span;
    p.add(
        start,
        ast::Type::Feature(ast::FeatureType::Array(ast::ArrayType { length, element })),
    )
}

/// Reads `[N]T{ entries }` or `[_]T{ entries }` (the length is the number of
/// values they give); an entry is a value, or `values...`, which spreads an
/// array or a range: `(0..3)...` or `0..3...`. Without `{` after it, `[N]T`
/// is the type alone.
fn array_literal(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    let ty = if p.ahead(1).kind == TokenKind::Underscore {
        let open = p.next();
        p.next();
        p.close(TokenKind::RightBracket, open);
        let element = p.type_expr();
        ast::ArrayLiteralType::Counted { element }
    } else {
        let ty = p.type_expr();
        // Without entries `[N]T` is the type alone, as an argument that
        // takes one; `[_]T` needs its entries to have a length.
        if !p.at(TokenKind::LeftBrace) {
            return p.add(start, ast::Expr::Type(ty));
        }
        ast::ArrayLiteralType::Written(ty)
    };
    let entries = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        starts_expression,
        spreadable,
    );
    p.add(
        start,
        ast::Expr::Feature(ast::FeatureExpr::ArrayLiteral(ast::ArrayLiteral {
            ty,
            entries,
        })),
    )
}

#[cfg(test)]
mod tests;

/// The type `[length]element`, written at `span`: the length is a whole
/// number without a suffix that an array can hold.
pub(crate) fn resolve_array_type(
    checker: &mut Checker<'_>,
    ty: ast::ArrayType,
    span: Span,
) -> TypeId {
    let ast::ArrayType { length, element } = ty;
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
pub(crate) fn check_array_literal(
    builder: &mut ThirBuilder<'_, '_>,
    literal: ast::ArrayLiteral,
    span: Span,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let (element, length, written) = match literal.ty {
        ast::ArrayLiteralType::Counted { element } => {
            // `[_]T`, from its `[` to its element's end
            let written = Span {
                start: span.start,
                end: tree.types.span(element).end,
            };
            (builder.checker.resolve_value_type(element), None, written)
        }
        ast::ArrayLiteralType::Written(written) => {
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
        return thir::Expr::INVALID;
    };
    let Some(ty) = builder.array_literal_type(written, element, length, (known, later), span)
    else {
        return thir::Expr::INVALID;
    };
    thir::Expr {
        ty,
        kind: thir::ExprKind::ArrayLiteral(builder.body.children.lists.add(&parts)),
    }
}
