//! Ranges: `a..b`, `a..=b`, `a..b:step`, and `..b` or `a..` before a `]`.
//! The parser reads the written parts into a `WrittenRange`, as slicing reads
//! them (`Tree::written_range`); the checker's file checks a range into the
//! built-in type `range[T]`.

use crate::Parser;
use crate::Rules;
use arena::Id;
use diagnostics::Diagnostic;
use token::TokenKind;
use tree::ast;
use tree::operators;

/// Adds the rules that read ranges to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.prefix(TokenKind::DotDot, open_range);
    rules.prefix(TokenKind::DotDotEqual, open_range);
    rules.infix(TokenKind::DotDot, operators::Precedence::Range, range);
    rules.infix(TokenKind::DotDotEqual, operators::Precedence::Range, range);
}

/// Reads a range with no start: `..b`, `..=b`, or `..` before `]`.
fn open_range(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    range_from(p, start, None)
}

/// Reads a range from `low`: `a..b`, `a..=b`, or `a..` before `]`.
fn range(p: &mut Parser, low: Id<ast::Expr>) -> Id<ast::Expr> {
    range_from(p, p.start_of(low), Some(low))
}

/// Reads `..` or `..=` and what follows it: an end, then `:step` to step by
/// more than one. Only right before the `]` may a range leave out its end.
fn range_from(p: &mut Parser, start: u32, low: Option<Id<ast::Expr>>) -> Id<ast::Expr> {
    let op = p.next();
    let mut high = None;
    if !p.at(TokenKind::RightBracket) {
        high = Some(p.expr(operators::Precedence::Range));
    } else if op.kind == TokenKind::DotDotEqual {
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "inclusive range requires an end bound",
            )
            .label(
                &p.tree.file,
                op.span,
                "add the inclusive end bound after `..=`",
            ),
        );
    }
    let mut step = None;
    if high.is_some() && p.at(TokenKind::Colon) {
        p.next();
        step = Some(p.expr(operators::Precedence::Range));
    }
    let inclusive = op.kind == TokenKind::DotDotEqual;
    let written = ast::WrittenRange {
        inclusive,
        start: low,
        end: high,
        step,
    };
    p.add(start, ast::Expr::Range(written))
}

#[cfg(test)]
mod tests {
    //! Parsing ranges: how a range binds, and where an end may be left out.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// The expression `id` with each range and binary operation in
    /// parentheses, and the rest as written.
    fn grouped(tree: &ast::Tree, id: Id<ast::Expr>) -> String {
        let text = |span| tree.file.slice(span).to_string();
        match tree.exprs[id] {
            ast::Expr::Range(written) => {
                let ast::WrittenRange {
                    inclusive,
                    start,
                    end,
                    step,
                } = written;
                let part = |part: Option<Id<ast::Expr>>| {
                    part.map_or(String::new(), |part| grouped(tree, part))
                };
                let op = if inclusive { "..=" } else { ".." };
                let step = step.map_or(String::new(), |step| format!(":{}", grouped(tree, step)));
                format!("({}{op}{}{step})", part(start), part(end))
            }
            ast::Expr::Binary { op, left, right } => {
                let (left, right) = (grouped(tree, left), grouped(tree, right));
                format!("({left} {} {right})", op.symbol())
            }
            ast::Expr::Index { value, index } => {
                format!("{}[{}]", grouped(tree, value), grouped(tree, index))
            }
            _ => text(tree.exprs.span(id)),
        }
    }

    /// A range binds looser than every binary operator, its step is read as an end is, and ranges group to the left.
    #[test]
    fn precedence() {
        for (text, want) in [
            ("x.y.z[0][i..j]", "x.y.z[0][(i..j)]"),
            ("a + b..c * d", "((a + b)..(c * d))"),
            ("a..=b:c + d", "(a..=b:(c + d))"),
            ("a..b..c", "((a..b)..c)"),
            ("a[..b]", "a[(..b)]"),
            ("a[b..]", "a[(b..)]"),
        ] {
            let (tree, errors) = testing::parse_text(&format!("fn f() {{ {text}; }}"));
            assert!(errors.is_empty(), "{text}: {errors:?}");
            let decl = tree.declarations().next().unwrap();
            let ast::Decl::Function(function) = tree.decls[decl] else {
                panic!("not a function")
            };
            let statements = tree.blocks[function.body.unwrap()].statements;
            let statement = tree.children.lists.items(statements).next().unwrap();
            let ast::Stmt::Expression(expr) = tree.stmts[statement] else {
                panic!("not an expression statement")
            };
            assert_eq!(grouped(&tree, expr), want, "{text}");
        }
    }

    /// Only right before a `]` may a range leave out its end, and an inclusive
    /// one never.
    #[test]
    fn open_ends() {
        for (text, message) in [
            (
                "fn f() { a[1..=]; }",
                "inclusive range requires an end bound",
            ),
            (
                "fn f() { for i in 0.. { } }",
                "expected expression, found `{`",
            ),
        ] {
            let (_, errors) = testing::parse_text(text);
            let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
            assert_eq!(got, [message], "{text}");
        }
    }
}
