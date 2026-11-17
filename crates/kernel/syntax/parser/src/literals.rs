//! Literals: numbers, characters, bools, strings and `none`.

use arena::Id;
use diagnostics::Diagnostic;
use syntax::ast::Expr;
use syntax::ast::StringKind;
use token::Token;
use token::TokenKind;

use crate::Grammar;
use crate::Parser;

impl<L: Grammar> Parser<L> {
    /// Checks the type suffix of number literal `t`, the part of a number the
    /// lexer leaves to the parser. Whether the value fits its type is the type
    /// checker's to decide.
    pub fn check_number(&mut self, t: Token) {
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

/// Reads `none`.
pub(crate) fn none_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    p.leaf(Expr::None)
}

/// Reads `true` or `false`.
pub(crate) fn bool_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let value = p.tok().kind == TokenKind::True;
    p.leaf(Expr::Bool(value))
}

/// Reads `"text"` or `c"text"`.
pub(crate) fn string_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let kind = match p.tok().kind {
        TokenKind::CString => StringKind::C,
        _ => StringKind::Plain,
    };
    p.leaf(Expr::String(kind))
}

/// Reads a number and checks its type suffix.
pub(crate) fn number_literal<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    p.check_number(p.tok());
    p.leaf(Expr::Number)
}
