//! Printing: `print(values)` and `println(values)`, read and checked into
//! the core's print of values (`ExprKind::Print`).

use source::Span;
use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use check::builder::ThirBuilder;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use parser::Parser;
use parser::Rules;
use parser::expr::starts_expression;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::ExprNode;
use crate::FeatureExpr;
use crate::Peeper;

/// `print(values)`, or `println(values)` when `newline`
#[derive(Clone, Copy, Debug)]
pub struct Print {
    pub newline: bool,
    pub values: List<ast::Expr<Peeper>>,
}

impl Print {
    /// Adds the rules that read `print` and `println` to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.prefix(TokenKind::Print, print);
        rules.prefix(TokenKind::Println, print);
    }
}

/// Reads `print(values)` or `println(values)`, which take any number of
/// values.
fn print(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let keyword = p.next();
    let print = FeatureExpr::Print(Print {
        newline: keyword.kind == TokenKind::Println,
        values: p.delimited(
            TokenKind::LeftParen,
            TokenKind::RightParen,
            false,
            starts_expression::<Peeper>,
            |p| p.expr(Precedence::Lowest),
        ),
    });
    p.add(keyword.span.start, ast::Expr::Feature(print))
}

/// `print(values)`, or `println(values)` when `newline`: each value of a
/// type `printable` takes. A plain string literal is printed as a `&cstr`,
/// though it is not one anywhere else.
// shortcut: a built-in until a library function takes any number of
// values of any printable type; then delete it with THIR's and MIR's
// `Print`.
impl ExprNode for Print {
    fn precedence(&self) -> Precedence {
        Precedence::Call
    }

    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        _span: Span,
        _expected: Option<TypeId>,
    ) -> thir::Expr {
        let Print { newline, values } = *self;
        let tree = &builder.checker.module().ast;
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(values.len());
        for value in tree.children.lists.items(values) {
            let span = tree.exprs.span(value);
            let expr = builder.check_expr(value, None);
            let ty = expr.ty;
            let types = &builder.checker.types;
            // On an error the others are still checked, for their own.
            if !printable(types, ty) {
                if ty != TypeId::INVALID {
                    let message = format!("`{}` cannot be printed", builder.checker.type_text(ty));
                    (builder.checker).error(diagnostics::INVALID_OPERATION, message, span);
                }
                continue;
            }
            let mut printed = builder.body.add(expr, span);
            // Printing reads a value, never takes it: a `&mut` reference in
            // a place, the one printable value that moves, is lent as `&`.
            if let Type::Reference {
                mutable: true,
                target,
            } = types[ty]
                && builder
                    .start_variable(builder.place_root(printed).0)
                    .is_some()
            {
                let reference = builder.body.exprs[printed].clone();
                let shared = builder.checker.reference_type(false, target);
                let lent = builder.reborrow(reference, span, shared);
                printed = builder.body.add(lent, span);
            }
            checked.push(printed.into());
        }
        if checked.len() < values.len() {
            return thir::Expr::INVALID;
        }
        thir::Expr {
            ty: TypeId::VOID,
            kind: thir::ExprKind::Print {
                newline,
                values: builder.body.children.lists.add(&checked),
            },
        }
    }
}

/// Whether `print` writes a value of the type `ty`: a number, `bool`,
/// `&cstr`, `&str` or raw pointer, or a reference to one. A struct or array
/// prints only once a trait or interface lets its type say how.
fn printable(types: &Types, mut ty: TypeId) -> bool {
    // A loop, not recursion: references nest as deep as the source writes.
    loop {
        match &types[ty] {
            Type::Integer(_)
            | Type::Float(_)
            | Type::Bool
            | Type::CString
            | Type::Str
            | Type::RawPointer { .. } => return true,
            Type::Reference { target, .. } => ty = *target,
            _ => return false,
        }
    }
}

#[cfg(test)]
mod tests;
