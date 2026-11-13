//! Reading struct types and struct literals.

use arena::Id;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use parser::decl::type_decl;
use parser::is_name;
use syntax::ast::Expr;
use syntax::ast::Field;
use syntax::ast::FieldValue;
use syntax::ast::Type;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::StructLiteral;
use crate::StructType;

/// Adds the rules that read structs to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<StructLiteral<L>>, Type: From<StructType<L>>>,
{
    rules.declaration(TokenKind::Struct, type_decl);
    rules.keyword_type(TokenKind::Struct, struct_type);
    rules.prefix(TokenKind::Dot, anonymous_literal);
    rules.after_path(TokenKind::Dot, TokenKind::LeftBrace, typed_literal);
}

/// Reads `{ name: Type, ... }`, the fields of a struct type that starts at
/// `start`.
fn struct_type<L>(p: &mut Parser<L>, start: u32) -> Id<Type<L>>
where
    L: Grammar<Type: From<StructType<L>>>,
{
    let fields = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        is_name,
        |p| {
            let start = p.tok().span.start;
            let name = p.name();
            p.expect(TokenKind::Colon);
            let ty = p.type_expr();
            p.add(start, Field { name, ty })
        },
    );
    p.add(start, Type::Feature(StructType { fields }.into()))
}

/// Reads `.{ name = value, ... }`, a struct literal without a type.
fn anonymous_literal<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<StructLiteral<L>>>,
{
    let start = p.tok().span.start;
    literal(p, None, start)
}

/// Reads `.{ name = value, ... }` after the type `path`.
fn typed_literal<L>(p: &mut Parser<L>, path: Id<Type<L>>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<StructLiteral<L>>>,
{
    let start = p.tree.types.span(path).start;
    literal(p, Some(path), start)
}

/// Reads `.{ name = value, ... }` after `ty` (none when there is no type), a
/// literal that starts at `start`. A `name: value` field, the colon of a
/// declaration where a field is set, is read as if it were `=`, and all such
/// colons are reported in one error.
fn literal<L>(p: &mut Parser<L>, ty: Option<Id<Type<L>>>, start: u32) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<StructLiteral<L>>>,
{
    p.next(); // `.`
    let mut colons = Vec::new();
    let fields = p.delimited(
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
        .label(&p.tree.file, colons[0].span, "");
        p.repair(d.help("set a field with `=`", colons));
    }
    p.add(start, Expr::Feature(StructLiteral { ty, fields }.into()))
}
