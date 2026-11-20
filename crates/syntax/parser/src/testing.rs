//! What the parser's tests share: a text read as Peeper.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use source::SourceFile;
use tree::ast;

/// The tree of `text` and its syntax errors.
pub(crate) fn parse_text(text: &str) -> (ast::Tree, Vec<Diagnostic>) {
    crate::parse(&Arc::new(SourceFile::new("t.peep", text)))
}

/// The message of each syntax error of `text`.
pub(crate) fn messages(text: &str) -> Vec<String> {
    let (_, errors) = parse_text(text);
    errors.into_iter().map(|error| error.message).collect()
}

/// The expressions of the statements of the first function of `tree`.
pub(crate) fn statements(tree: &ast::Tree) -> Vec<Id<ast::Expr>> {
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Function(function) = tree.decls[decl] else {
        panic!("not a function")
    };
    let statements = tree.blocks[function.body.unwrap()].statements;
    let expression = |statement| match tree.stmts[statement] {
        ast::Stmt::Expression(expr) => expr,
        other => panic!("not an expression statement: {other:?}"),
    };
    tree.children
        .lists
        .items(statements)
        .map(expression)
        .collect()
}
