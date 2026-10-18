//! Literals: numbers, strings, characters, `true`, `false`, `none`, and struct
//! and array literals.

use arena::Id;
use diagnostics::Diagnostic;
use model::ast::Expr;
use model::ast::FieldValue;
use model::ast::Length;
use model::ast::Type;
use token::Token;
use token::TokenKind;

use crate::Parser;
use crate::expr::Precedence;
use crate::expr::starts_expression;
use crate::is_name;

impl Parser<'_> {
    /// Reads `.{ name = value, ... }` after `ty` (none when there is no type), a
    /// literal that starts at `start`. A `name: value` field, the colon of a
    /// declaration where a field is set, is read as if it were `=`, and all such
    /// colons are reported in one error.
    pub(crate) fn struct_literal(&mut self, ty: Option<Id<Type>>, start: u32) -> Id<Expr> {
        self.next(); // `.`
        let mut colons = Vec::new();
        let fields = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            false,
            is_name,
            |p| {
                let field_start = p.tok().span.start;
                let name = p.name();
                let t = p.tok();
                if t.kind == TokenKind::Colon {
                    p.next();
                    colons.push(p.fix(t.span, " ="));
                } else {
                    p.expect(TokenKind::Assign);
                }
                let value = p.expr(Precedence::Lowest);
                p.add(field_start, FieldValue { name, value })
            },
        );
        if !colons.is_empty() {
            let d = Diagnostic::new(
                diagnostics::EXPECTED_TOKEN,
                "expected `=` after struct literal field name",
            )
            .label(self.file, colons[0].span, "");
            self.repair(d.help("set a field with `=`", colons));
        }
        self.add(start, Expr::StructLiteral { ty, fields })
    }

    /// Checks the type suffix of number literal `t`, the part of a number the
    /// lexer leaves to the parser. Whether the value fits its type is the type
    /// checker's to decide.
    pub(crate) fn check_number(&mut self, t: Token) {
        let literal = numeric::split(self.text(t));
        let message = if literal.suffix.is_empty() {
            return; // most numbers
        } else if !literal.suffix_well_formed() {
            format!("invalid numeric literal suffix {}", literal.suffix)
        } else if literal.integer_suffix() && literal.is_float() {
            format!(
                "integer suffix {} cannot be used on a float literal",
                literal.suffix
            )
        } else {
            return;
        };
        self.repair(
            Diagnostic::new(diagnostics::INVALID_NUMBER_SUFFIX, message)
                .label(self.file, t.span, ""),
        );
    }
}

/// Reads `"text"` or `c"text"`.
pub(crate) fn string_literal(p: &mut Parser) -> Id<Expr> {
    let c_string = p.at(TokenKind::CString);
    p.leaf(Expr::String { c_string })
}

/// Reads `'x'` or `b'x'`.
pub(crate) fn character_literal(p: &mut Parser) -> Id<Expr> {
    let byte = p.at(TokenKind::ByteChar);
    p.leaf(Expr::Char { byte })
}

/// Reads `true` or `false`.
pub(crate) fn bool_literal(p: &mut Parser) -> Id<Expr> {
    p.leaf(Expr::Bool)
}

/// Reads `none`.
pub(crate) fn none_literal(p: &mut Parser) -> Id<Expr> {
    p.leaf(Expr::None)
}

/// Reads a number and checks its type suffix.
pub(crate) fn number_literal(p: &mut Parser) -> Id<Expr> {
    p.check_number(p.tok());
    p.leaf(Expr::Number)
}

/// Reads `.{ name = value, ... }`, a struct literal without a type.
pub(crate) fn anonymous_struct_literal(p: &mut Parser) -> Id<Expr> {
    let start = p.tok().span.start;
    p.struct_literal(None, start)
}

/// Reads `[N]T{ values }`, `[_]T{ values }` (the length is the number of
/// values) or `[]T{ values }`.
pub(crate) fn array_literal(p: &mut Parser) -> Id<Expr> {
    let start = p.tok().span.start;
    let after = p.ahead(1);
    let ty = if after.kind == TokenKind::Ident && p.text(after) == "_" {
        let open = p.next();
        let length = Length::Inferred(p.next().span);
        p.close(TokenKind::RightBracket, open);
        let element = p.type_expr();
        p.add(open.span.start, Type::FixedArray { length, element })
    } else {
        p.array_type()
    };
    let values = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        starts_expression,
        |p| p.expr(Precedence::Lowest),
    );
    p.add(start, Expr::ArrayLiteral { ty, values })
}
