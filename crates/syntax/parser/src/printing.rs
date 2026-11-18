//! Printing: reads `print(values)` and `println(values)` into
//! `ast::Expr::Print`, which the checker turns into calls of the prelude's
//! print functions.

use crate::expression;
use arena::Id;
use token::TokenKind;
use tree::ast;
use tree::operators;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read `print` and `println` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.prefix(TokenKind::Print, print);
    rules.prefix(TokenKind::Println, print);
}

/// Reads `print(values)` or `println(values)`, which take any number of
/// values.
fn print(p: &mut Parser) -> Id<ast::Expr> {
    let keyword = p.next();
    let print = ast::Expr::Print {
        newline: keyword.kind == TokenKind::Println,
        values: p.delimited(
            TokenKind::LeftParen,
            TokenKind::RightParen,
            false,
            expression::starts_expression,
            |p| p.expr(operators::Precedence::Lowest),
        ),
    };
    p.add(keyword.span.start, print)
}

#[cfg(test)]
mod tests {
    //! Reading `print` and `println`. What they accept is the test programs'
    //! to show.

    use crate::testing;
    use tree::ast;

    /// Each takes any number of values, none too; `println` is the one with a
    /// newline.
    #[test]
    fn parses_prints() {
        let (tree, errors) = testing::parse_text("fn f() { print(); println(items[n], 1); }");
        assert!(errors.is_empty(), "{errors:?}");
        let prints: Vec<(bool, usize)> = (tree.exprs.ids())
            .filter_map(|id| match tree.exprs[id] {
                ast::Expr::Print { newline, values } => Some((newline, values.len())),
                _ => None,
            })
            .collect();
        assert_eq!(prints, [(false, 0), (true, 2)]);
    }

    /// An unclosed list is one error, and the statement after it is read.
    #[test]
    fn unclosed_values() {
        assert_eq!(
            testing::messages("fn f() { print(; return; }"),
            ["expected `)`, found `;`"]
        );
    }
}
