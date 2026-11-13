//! Reading bools. The parser's tree dump is its own tests', so these look
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

/// The language the bool tests are written in: the core and bools.
#[derive(Clone, Copy, Debug)]
enum Bools {}

impl Language for Bools {
    type Expr = BoolLiteral;
    type Type = Infallible;

    fn precedence(_: &BoolLiteral) -> Precedence {
        Precedence::Call
    }

    fn fields(ty: &Infallible) -> Option<List<ast::Field<Bools>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<ast::Decl<Bools>>> {
        match *ty {}
    }

    fn pointee(ty: &Infallible) -> Option<Id<ast::Type<Bools>>> {
        match *ty {}
    }

    fn string_kind(_: &BoolLiteral) -> Option<ast::StringKind> {
        None
    }

    fn written_range(_: &BoolLiteral) -> Option<ast::WrittenRange<Bools>> {
        None
    }
}

static RULES: Rules<Bools> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Bools {
    fn rules() -> &'static Rules<Bools> {
        &RULES
    }
}

/// `true` and `false` are bool literals, each spanning its word, and an
/// operand like any value.
#[test]
fn parses_literals() {
    let text = "fn f() { true; false; !true == false; }";
    let (tree, errors) = parser::parse::<Bools>(&Arc::new(SourceFile::new("t.peep", text)));
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(bool, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(BoolLiteral { value }) => {
                Some((value, tree.file.slice(tree.exprs.span(id))))
            }
            _ => None,
        })
        .collect();
    assert_eq!(
        literals,
        [
            (true, "true"),
            (false, "false"),
            (true, "true"),
            (false, "false")
        ]
    );
}
