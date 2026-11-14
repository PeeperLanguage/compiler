//! Enums: the type `enum { A, B: Payload }` and its declaration, a variant
//! literal (`Enum::Case with payload`), a case test (`x is Enum::Case`) and
//! `match`. All are read, and refused by the checker until enums exist.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use check::Checker;
use check::builder::ThirBuilder;
use diagnostics::Diagnostic;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use parser::decl::type_decl;
use parser::is_name;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureExpr;
use crate::FeatureStmt;
use crate::FeatureType;
use crate::Peeper;

/// `enum { variants }`
#[derive(Clone, Copy, Debug)]
pub struct EnumType {
    pub variants: List<ast::Variant<Peeper>>,
}

/// `Enum::Variant with payload`; the case is a `Type::Path`
#[derive(Clone, Copy, Debug)]
pub struct VariantLiteral {
    pub case: Id<ast::Type<Peeper>>,
    pub payload: Id<ast::Expr<Peeper>>,
}

/// `value is Enum::Variant`
#[derive(Clone, Copy, Debug)]
pub struct CaseTest {
    pub value: Id<ast::Expr<Peeper>>,
    pub case: Id<ast::Type<Peeper>>,
}

/// `match subject { arms }`
#[derive(Clone, Copy, Debug)]
pub struct Match {
    pub subject: Id<ast::Expr<Peeper>>,
    pub arms: List<ast::MatchArm<Peeper>>,
}

/// Adds the rules that read enums and `match` to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.declaration(TokenKind::Enum, type_decl);
    rules.type_start(TokenKind::Enum, enum_type);
    rules.infix(TokenKind::With, Precedence::Call, variant_literal);
    rules.infix(TokenKind::Is, Precedence::Equality, case_test);
    rules.statement(TokenKind::Match, match_statement);
}

/// Reads `{ Name, Name: Payload, ... }`, the variants of an enum type that
/// starts at `start`.
fn enum_type(p: &mut Parser<Peeper>, start: u32) -> Id<ast::Type<Peeper>> {
    let variants = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        is_name,
        |p| {
            let start = p.tok().span.start;
            let name = p.name();
            let payload = p.at(TokenKind::Colon).then(|| {
                p.next();
                p.type_expr()
            });
            p.add(start, ast::Variant { name, payload })
        },
    );
    let ty = FeatureType::Enum(EnumType { variants });
    p.add(start, ast::Type::Feature(ty))
}

/// Reports `case` unless it is a full path `Enum::Case` (or a type already
/// reported as invalid).
fn require_path(p: &mut Parser<Peeper>, case: Id<ast::Type<Peeper>>) {
    if !matches!(p.tree.types[case], ast::Type::Path(_) | ast::Type::Invalid) {
        let span = p.tree.types.span(case);
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "enum case must use a fully named path",
            )
            .label(&p.tree.file, span, "write `Enum::Variant`"),
        );
    }
}

/// Reads `with payload` after `case`, which is a full path `Enum::Case`.
fn variant_literal(p: &mut Parser<Peeper>, case: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let payload = p.expr(Precedence::Lowest);
    let path = match p.tree.exprs[case] {
        ast::Expr::Path(path) if matches!(p.tree.types[path], ast::Type::Path(_)) => path,
        _ => {
            let span = p.tree.exprs.span(case);
            p.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "enum case must use a fully named path",
                )
                .label(&p.tree.file, span, "write `Enum::Variant`"),
            );
            return case;
        }
    };
    let literal = FeatureExpr::VariantLiteral(VariantLiteral {
        case: path,
        payload,
    });
    p.add(p.start_of(case), ast::Expr::Feature(literal))
}

/// Reads `is Enum::Case` after `value`; the case is a full path.
fn case_test(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let case = p.type_expr();
    require_path(p, case);
    let test = FeatureExpr::CaseTest(CaseTest { value, case });
    p.add(p.start_of(value), ast::Expr::Feature(test))
}

/// Reads `match subject { arms }`. An arm is `_ => { }`, `Enum::Case => { }`
/// or `Enum::Case with pattern => { }`; arms are not separated by commas.
fn match_statement(p: &mut Parser<Peeper>) -> Id<ast::Stmt<Peeper>> {
    let start = p.next().span.start;
    let subject = p.expr(Precedence::Lowest);
    let mut arms = List::EMPTY;
    if let Some(open) = p.expect(TokenKind::LeftBrace) {
        let mut read: Vec<NonZeroU32> = Vec::new();
        // Every arm begins with `_` or a path and reads at least it, so the
        // loop always moves on.
        while p.at(TokenKind::Underscore) || p.at(TokenKind::Ident) {
            let arm_start = p.tok().span.start;
            let (pattern, body) = match_arm(p);
            read.push(p.add(arm_start, ast::MatchArm { pattern, body }).into());
            let t = p.tok();
            if t.kind == TokenKind::Comma {
                p.next();
                let fix = p.fix(t.span, "");
                p.repair(
                    Diagnostic::new(diagnostics::EXPECTED_TOKEN, "match arms do not use commas")
                        .label(&p.tree.file, t.span, "")
                        .help("remove this comma", vec![fix]),
                );
            }
        }
        arms = p.tree.children.lists.add(&read);
        p.close(TokenKind::RightBrace, open);
    }
    let statement = FeatureStmt::Match(Match { subject, arms });
    p.add(start, ast::Stmt::Feature(statement))
}

/// Reads one arm's pattern, its `=>` and its block. A missing `=>` before
/// the block is read as if it were there.
fn match_arm(p: &mut Parser<Peeper>) -> (Id<ast::Pattern<Peeper>>, Id<ast::Block<Peeper>>) {
    let t = p.tok();
    let pattern = if t.kind == TokenKind::Underscore {
        let start = p.next().span.start;
        p.add(start, ast::Pattern::Wildcard)
    } else {
        case_pattern(p)
    };
    if p.at(TokenKind::LeftBrace) {
        let at = p.here();
        let fix = p.fix(at, " =>");
        p.repair(
            Diagnostic::new(
                diagnostics::EXPECTED_TOKEN,
                "expected `=>` after match case",
            )
            .label(&p.tree.file, at, "")
            .help("add missing `=>`", vec![fix]),
        );
    } else if p.expect(TokenKind::FatArrow).is_none() {
        p.sync();
    }
    (pattern, p.block())
}

/// Reads `Enum::Case`, perhaps followed by `with` and the pattern of its
/// payload. The case is a full path.
fn case_pattern(p: &mut Parser<Peeper>) -> Id<ast::Pattern<Peeper>> {
    // Cases in cases, `A::B with C::D with ...`, are a deep recursion.
    source::grow_stack(|| {
        let start = p.tok().span.start;
        let case = p.type_expr();
        require_path(p, case);
        let inner = p.at(TokenKind::With).then(|| {
            p.next();
            payload_pattern(p)
        });
        p.add(start, ast::Pattern::Case { case, inner })
    })
}

/// Reads the pattern after `with`: `none`, `_`, a name to bind,
/// `.{ fields }`, or another case.
fn payload_pattern(p: &mut Parser<Peeper>) -> Id<ast::Pattern<Peeper>> {
    let t = p.tok();
    match t.kind {
        TokenKind::None => {
            p.next();
            p.add(t.span.start, ast::Pattern::None)
        }
        TokenKind::Underscore => {
            p.next();
            p.add(t.span.start, ast::Pattern::Wildcard)
        }
        TokenKind::Dot => p.struct_pattern(),
        TokenKind::Ident if !matches!(p.ahead(1).kind, TokenKind::ColonColon | TokenKind::Less) => {
            let name = p.name();
            p.tree.add(ast::Pattern::Binding, name.span)
        }
        TokenKind::Ident => case_pattern(p),
        _ => {
            p.fail(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "expected a pattern after `with`",
                )
                .label(&p.tree.file, t.span, "")
                .help(
                    "write `none`, `_`, a name, `.{ field }`, or a case of the payload's enum",
                    Vec::new(),
                ),
            );
            p.add(p.here().start, ast::Pattern::Invalid)
        }
    }
}

/// An enum type at `span`, which no check exists for yet.
pub(crate) fn resolve_type(checker: &mut Checker<'_, Peeper>, span: Span) -> TypeId {
    checker.not_supported(span, "enum types")
}

/// The declaration of an enum, written `written`, which no check exists
/// for yet.
pub(crate) fn declare(checker: &mut Checker<'_, Peeper>, written: ast::TypeDecl<Peeper>) -> TypeId {
    checker.not_supported(written.name.span, "enum declarations")
}

/// The enum expression `what` names, at `span`, which no check exists for
/// yet.
pub(crate) fn check_expr(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    span: Span,
    what: &str,
) -> thir::Expr {
    builder.checker.not_supported(span, what);
    thir::Expr::INVALID
}

/// A `match` at `span`, which no check exists for yet.
pub(crate) fn check_match(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    span: Span,
) -> Option<thir::Stmt> {
    builder.checker.not_supported(span, "`match` statements");
    None
}

#[cfg(test)]
mod tests;
