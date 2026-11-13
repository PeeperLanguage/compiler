//! Reading raw pointer types and `@x`.

use arena::Id;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use syntax::ast::Expr;
use syntax::ast::Type;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::PointerType;
use crate::RawAddress;

/// Adds the rules that read raw pointers to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<RawAddress<L>>, Type: From<PointerType<L>>>,
{
    rules.keyword_type(TokenKind::Asterisk, typed_pointer);
    rules.keyword_type(TokenKind::Rawptr, raw_pointer);
    rules.prefix(TokenKind::At, raw_address);
    // types that cannot be values
    rules.prefix(TokenKind::Asterisk, type_value);
    rules.prefix(TokenKind::Rawptr, type_value);
}

/// Reads the `T` of `*T`, a pointer type that starts at `start`.
fn typed_pointer<L>(p: &mut Parser<L>, start: u32) -> Id<Type<L>>
where
    L: Grammar<Type: From<PointerType<L>>>,
{
    let target = Some(p.type_expr());
    p.add(start, Type::Feature(PointerType { target }.into()))
}

/// `rawptr`, which starts at `start`.
fn raw_pointer<L>(p: &mut Parser<L>, start: u32) -> Id<Type<L>>
where
    L: Grammar<Type: From<PointerType<L>>>,
{
    p.add(start, Type::Feature(PointerType { target: None }.into()))
}

/// Reads a type that cannot be a value (`*T`, `rawptr`) where a value could
/// go.
fn type_value<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let start = p.tok().span.start;
    let ty = p.type_expr();
    p.add(start, Expr::Type(ty))
}

/// Reads `@x`.
fn raw_address<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<RawAddress<L>>>,
{
    let start = p.next().span.start;
    let place = p.expr(Precedence::Prefix);
    p.add(start, Expr::Feature(RawAddress { place }.into()))
}
