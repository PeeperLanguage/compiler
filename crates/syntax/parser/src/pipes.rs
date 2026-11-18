//! Pipes: reads `value |> target` into `ast::Expr::Pipe`; the checker reads
//! the target as the call that takes the value first.

use arena::Id;
use token::TokenKind;
use tree::ast;
use tree::operators;

use crate::Parser;
use crate::Rules;

/// Adds the rule that reads pipes to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.infix(TokenKind::PipeArrow, operators::Precedence::Pipe, pipe);
}

/// Reads `|> target` after `value`. The target takes only what binds
/// tighter than a prefix operator, so `a |> len() as i32` is
/// (a |> len()) as i32.
fn pipe(p: &mut Parser, value: Id<ast::Expr>) -> Id<ast::Expr> {
    p.next();
    let target = p.expr(operators::Precedence::Prefix);
    let pipe = ast::Expr::Pipe { value, target };
    p.add(p.start_of(value), pipe)
}

#[cfg(test)]
mod tests {
    //! Reading pipes.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// `expr` with each pipe in parentheses.
    fn grouped(tree: &ast::Tree, expr: Id<ast::Expr>) -> String {
        match tree.exprs[expr] {
            ast::Expr::Pipe { value, target } => {
                format!("({} |> {})", grouped(tree, value), grouped(tree, target))
            }
            _ => tree.file.slice(tree.exprs.span(expr)).to_string(),
        }
    }

    /// A pipe groups to the left and binds looser than `+`; what is written
    /// after its target, as `as`, takes the whole pipe. Any target is read:
    /// the checker says what one may be.
    #[test]
    fn parses_pipes() {
        let (tree, errors) = testing::parse_text(
            "fn f() { a |> f(b) |> g(); a + b |> f(); a |> b; c |> len() as i32; }",
        );
        assert!(errors.is_empty(), "{errors:?}");
        let found: Vec<String> = (testing::statements(&tree).into_iter())
            .map(|statement| grouped(&tree, statement))
            .collect();
        assert_eq!(
            found[..3],
            ["((a |> f(b)) |> g())", "(a + b |> f())", "(a |> b)"]
        );
        let last = (tree.exprs.ids())
            .filter(|&id| matches!(tree.exprs[id], ast::Expr::Pipe { .. }))
            .last()
            .unwrap();
        assert_eq!(tree.file.slice(tree.exprs.span(last)), "c |> len()");
    }
}
