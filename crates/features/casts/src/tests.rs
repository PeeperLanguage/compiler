//! Reading casts. The parser's tree dump is its own tests', so these look
//! at the tree.

use std::convert::Infallible;
use std::sync::Arc;

use arena::Id;
use arena::List;
use parser::Grammar;
use parser::Rules;
use source::SourceFile;
use syntax::ast;
use syntax::ast::Language;
use syntax::operators::Precedence;

use super::*;

/// The language the cast tests are written in: the core and casts.
#[derive(Clone, Copy, Debug)]
enum Casts {}

impl Language for Casts {
    type Expr = Cast<Casts>;
    type Type = Infallible;

    fn precedence(_: &Cast<Casts>) -> Precedence {
        Precedence::Cast
    }

    fn fields(ty: &Infallible) -> Option<List<ast::Field<Casts>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<ast::Decl<Casts>>> {
        match *ty {}
    }

    fn pointee(ty: &Infallible) -> Option<Id<ast::Type<Casts>>> {
        match *ty {}
    }

    fn string_kind(_: &Cast<Casts>) -> Option<ast::StringKind> {
        None
    }

    fn written_range(_: &Cast<Casts>) -> Option<ast::WrittenRange<Casts>> {
        None
    }
}

static RULES: Rules<Casts> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Casts {
    fn rules() -> &'static Rules<Casts> {
        &RULES
    }
}

type Tree = ast::Tree<Casts>;

/// The expression `id` with each cast and operation that holds a cast in
/// parentheses, and the rest as written.
fn grouped(tree: &Tree, id: Id<ast::Expr<Casts>>) -> String {
    let text = |span| tree.file.slice(span).to_string();
    match tree.exprs[id] {
        ast::Expr::Feature(Cast { value, ty }) => {
            format!(
                "({} as {})",
                grouped(tree, value),
                text(tree.types.span(ty))
            )
        }
        ast::Expr::Binary { op, left, right } => {
            format!(
                "({} {} {})",
                grouped(tree, left),
                op.symbol(),
                grouped(tree, right)
            )
        }
        ast::Expr::Pipe { value, call } => {
            format!("({} |> {})", grouped(tree, value), grouped(tree, call))
        }
        _ => text(tree.exprs.span(id)),
    }
}

/// A cast binds tighter than every binary operator, and looser than a
/// pipe.
#[test]
fn precedence() {
    for (text, want) in [
        ("a + b as i64", "(a + (b as i64))"),
        ("a as i32 * b", "((a as i32) * b)"),
        ("a |> len() as i32", "((a |> len()) as i32)"),
    ] {
        let source = Arc::new(SourceFile::new("t.peep", format!("fn f() {{ {text}; }}")));
        let (tree, errors) = parser::parse::<Casts>(&source);
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
