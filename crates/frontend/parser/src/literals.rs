//! Literals: numbers, strings, characters, `true`, `false`, `none`, and array
//! literals.

use arena::Id;
use diagnostics::Diagnostic;
use syntax::ast::Expr;
use syntax::ast::Length;
use syntax::ast::Type;
use token::Token;
use token::TokenKind;

use crate::Grammar;
use crate::Parser;
use crate::expr::spreadable;
use crate::expr::starts_expression;

impl<L: Grammar> Parser<L> {
    /// Checks the type suffix of number literal `t`, the part of a number the
    /// lexer leaves to the parser. Whether the value fits its type is the type
    /// checker's to decide.
    pub(crate) fn check_number(&mut self, t: Token) {
        let literal = literal::numeric::split(self.text(t));
        let message = if literal.suffix.is_empty() {
            return; // most numbers
        } else if !literal.is_suffix_well_formed() {
            format!("invalid numeric literal suffix {}", literal.suffix)
        } else if literal.has_integer_suffix() && literal.is_float() {
            format!(
                "integer suffix {} cannot be used on a float literal",
                literal.suffix
            )
        } else {
            return;
        };
        self.repair(
            Diagnostic::new(diagnostics::INVALID_NUMBER_SUFFIX, message).label(
                &self.tree.file,
                t.span,
                "",
            ),
        );
    }
}

/// Reads `"text"` or `c"text"`.
pub(crate) fn string_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let c_string = p.is_either(TokenKind::CString, TokenKind::String);
    p.leaf(Expr::String { c_string })
}

/// Reads `'x'` or `b'x'`.
pub(crate) fn character_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let byte = p.is_either(TokenKind::ByteChar, TokenKind::Char);
    p.leaf(Expr::Char { byte })
}

/// Reads `none`.
pub(crate) fn none_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    p.leaf(Expr::None)
}

/// Reads a number and checks its type suffix.
pub(crate) fn number_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    p.check_number(p.tok());
    p.leaf(Expr::Number)
}

/// Reads `[N]T{ entries }` or `[_]T{ entries }` (the length is the number of
/// values they give); an entry is a value, or `values...`, which spreads an
/// array or a range: `(0..3)...` or `0..3...`. Without `{` after it, `[N]T`
/// is the type alone.
pub(crate) fn array_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let start = p.tok().span.start;
    let after = p.ahead(1);
    let ty = if after.kind == TokenKind::Underscore {
        let open = p.next();
        let length = Length::Inferred(p.next().span);
        p.close(TokenKind::RightBracket, open);
        let element = p.type_expr();
        p.add(open.span.start, Type::FixedArray { length, element })
    } else {
        p.array_type()
    };
    // Without entries `[N]T` is the type alone, as an argument that takes
    // one; `[_]T` needs its entries to have a length.
    if after.kind != TokenKind::Underscore && !p.at(TokenKind::LeftBrace) {
        return p.add(start, Expr::Type(ty));
    }
    let entries = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        starts_expression::<L>,
        spreadable,
    );
    p.add(start, Expr::ArrayLiteral { ty, entries })
}
