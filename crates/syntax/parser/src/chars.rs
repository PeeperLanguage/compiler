//! Character literals: `'x'` and `b'x'`.

use arena::Id;
use token::TokenKind;
use tree::ast;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read character literals to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.prefix(TokenKind::Char, char_literal);
    rules.prefix(TokenKind::ByteChar, char_literal);
}

/// Reads `'x'` or `b'x'`.
fn char_literal(p: &mut Parser) -> Id<ast::Expr> {
    let token = p.next();
    let byte = token.kind == TokenKind::ByteChar;
    p.add(token.span.start, ast::Expr::Char { byte })
}

#[cfg(test)]
mod tests {
    //! Reading character literals.

    use crate::testing;
    use tree::ast;

    /// `'x'` is a character and `b'y'` a byte, each spanning its quotes.
    #[test]
    fn parses_literals() {
        let (tree, errors) = testing::parse_text("fn f() { 'x'; b'y'; }");
        assert!(errors.is_empty(), "{errors:?}");
        let literals: Vec<(bool, &str)> = (tree.exprs.ids())
            .filter_map(|id| match tree.exprs[id] {
                ast::Expr::Char { byte } => Some((byte, tree.file.slice(tree.exprs.span(id)))),
                _ => None,
            })
            .collect();
        assert_eq!(literals, [(false, "'x'"), (true, "b'y'")]);
    }
}
