//! Reading `[N]T` and array literals.

use arena::Id;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use parser::expr::spreadable;
use parser::expr::starts_expression;
use source::Span;
use syntax::ast::Expr;
use syntax::ast::Type;
use token::Token;
use token::TokenKind;

use crate::ArrayLiteral;
use crate::ArrayType;
use crate::LiteralType;

/// Adds the rules that read arrays to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<ArrayLiteral<L>>, Type: From<ArrayType<L>>>,
{
    rules.type_start(TokenKind::LeftBracket, array_type);
    rules.prefix(TokenKind::LeftBracket, array_literal);
}

/// Reads the `N]T` of `[N]T`, a type that starts at `start`, with its `[`;
/// a `[]T` alone is reported, as it has no fixed size.
fn array_type<L>(p: &mut Parser<L>, start: u32) -> Id<Type<L>>
where
    L: Grammar<Type: From<ArrayType<L>>>,
{
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
        let ty = p.add(start, Type::Invalid);
        // An element reported already, as `[]i32` in `[][]i32`, is the
        // one mistake: its fix fixes the whole.
        if let Type::Invalid = p.tree.types[element] {
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
        return p.add(start, Type::Invalid);
    }
    p.next();
    p.check_number(t);
    p.close(TokenKind::RightBracket, open);
    let element = p.type_expr();
    let length = t.span;
    p.add(start, Type::Feature(ArrayType { length, element }.into()))
}

/// Reads `[N]T{ entries }` or `[_]T{ entries }` (the length is the number of
/// values they give); an entry is a value, or `values...`, which spreads an
/// array or a range: `(0..3)...` or `0..3...`. Without `{` after it, `[N]T`
/// is the type alone.
fn array_literal<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<ArrayLiteral<L>>>,
{
    let start = p.tok().span.start;
    let ty = if p.ahead(1).kind == TokenKind::Underscore {
        let open = p.next();
        p.next();
        p.close(TokenKind::RightBracket, open);
        let element = p.type_expr();
        LiteralType::Counted { element }
    } else {
        let ty = p.type_expr();
        // Without entries `[N]T` is the type alone, as an argument that
        // takes one; `[_]T` needs its entries to have a length.
        if !p.at(TokenKind::LeftBrace) {
            return p.add(start, Expr::Type(ty));
        }
        LiteralType::Written(ty)
    };
    let entries = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        starts_expression::<L>,
        spreadable,
    );
    p.add(start, Expr::Feature(ArrayLiteral { ty, entries }.into()))
}
