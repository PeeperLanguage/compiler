//! Panics: `panic message;`, read and checked into the core's panic
//! statement (`thir::Stmt::Panic`), which stops the program.

use arena::Id;
use check::builder::ThirBuilder;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::ast::Language;
use syntax::ast::StringKind;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureStmt;
use crate::Peeper;
use crate::StmtNode;

/// `panic message;`
#[derive(Clone, Copy, Debug)]
pub struct Panic {
    pub message: Id<ast::Expr<Peeper>>,
}

impl Panic {
    /// Adds the rule that reads panics to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.statement(TokenKind::Panic, panic);
    }
}

/// Reads `panic message;`.
fn panic(p: &mut Parser<Peeper>) -> Id<ast::Stmt<Peeper>> {
    let start = p.next().span.start;
    let message = p.expr(Precedence::Lowest);
    p.end();
    p.add(
        start,
        ast::Stmt::Feature(FeatureStmt::Panic(Panic { message })),
    )
}

impl StmtNode for Panic {
    /// `panic message;`: the message is a plain string literal.
    fn check(&self, builder: &mut ThirBuilder<'_, '_, Peeper>, _span: Span) -> Option<thir::Stmt> {
        let tree = &builder.checker.module().ast;
        let message = tree.unparenthesized(self.message);
        let span = tree.exprs.span(message);
        // shortcut: a plain string literal only, as there are no run-time
        // strings; take a string value once there are.
        let plain = |expr| Peeper::string_kind(&expr) == Some(StringKind::Plain);
        if !matches!(tree.exprs[message], ast::Expr::Feature(expr) if plain(expr)) {
            let checked = builder.add_expr(message, None);
            if builder.body.exprs[checked].ty != TypeId::INVALID {
                let what = "`panic` messages other than string literals";
                builder.checker.not_supported(span, what);
            }
            return None;
        }
        let message = builder.string_literal(span, TypeId::STR);
        Some(thir::Stmt::Panic(builder.body.add(message, span)))
    }
}

#[cfg(test)]
mod tests;
