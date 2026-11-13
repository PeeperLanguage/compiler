//! Reading and checking ranges. The parser's tree dump and the checker's
//! test program are their own crates' tests, so these build what they need.

use std::convert::Infallible;
use std::sync::Arc;

use ::check::Checked;
use ::check::Checker;
use ::check::builder::ThirBuilder;
use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DefId;
use model::FunctionCopy;
use model::ModuleId;
use model::machine::Machine;
use model::semantic_types::TypeId;
use model::thir;
use model::thir::Body;
use parser::Grammar;
use parser::Rules;
use source::SourceFile;
use source::Span;
use syntax::ast;
use syntax::ast::Language;
use syntax::ast::StringKind;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;

use super::*;

/// The language the range tests are written in: the core and ranges.
#[derive(Clone, Copy, Debug)]
enum Ranges {}

impl Language for Ranges {
    type Expr = WrittenRange<Ranges>;
    type Type = Infallible;

    fn precedence(_: &WrittenRange<Ranges>) -> Precedence {
        Precedence::Range
    }

    fn fields(ty: &Infallible) -> Option<List<ast::Field<Ranges>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<ast::Decl<Ranges>>> {
        match *ty {}
    }

    fn pointee(ty: &Infallible) -> Option<Id<ast::Type<Ranges>>> {
        match *ty {}
    }

    fn string_kind(_: &WrittenRange<Ranges>) -> Option<StringKind> {
        None
    }

    fn written_range(expr: &WrittenRange<Ranges>) -> Option<WrittenRange<Ranges>> {
        Some(*expr)
    }
}

static RULES: Rules<Ranges> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Ranges {
    fn rules() -> &'static Rules<Ranges> {
        &RULES
    }
}

impl Checked for Ranges {
    fn check_expr(
        builder: &mut ThirBuilder<'_, '_, Ranges>,
        expr: WrittenRange<Ranges>,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr {
        crate::check::check_range(builder, expr, span, expected)
    }

    fn resolve_type(_: &mut Checker<'_, Ranges>, ty: Infallible, _: Span) -> TypeId {
        match ty {}
    }

    fn declare_type(
        _: &mut Checker<'_, Ranges>,
        _: DefId,
        _: ast::TypeDecl<Ranges>,
        ty: Infallible,
    ) -> TypeId {
        match ty {}
    }
}

type Tree = ast::Tree<Ranges>;

fn parse_text(text: &str) -> (Tree, Vec<Diagnostic>) {
    parser::parse(&Arc::new(SourceFile::new("t.peep", text)))
}

/// The expression `id` with each range and binary operation in
/// parentheses, and the rest as written.
fn grouped(tree: &Tree, id: Id<ast::Expr<Ranges>>) -> String {
    let text = |span| tree.file.slice(span).to_string();
    match tree.exprs[id] {
        ast::Expr::Feature(WrittenRange {
            inclusive,
            start,
            end,
            step,
        }) => {
            let part = |part: Option<Id<ast::Expr<Ranges>>>| {
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
        ast::Expr::OrElse { value, fallback } => {
            format!(
                "({} orelse {})",
                grouped(tree, value),
                grouped(tree, fallback)
            )
        }
        ast::Expr::Index { value, index } => {
            format!("{}[{}]", grouped(tree, value), grouped(tree, index))
        }
        _ => text(tree.exprs.span(id)),
    }
}

/// A range binds looser than every binary operator and tighter than
/// `orelse`, its step is read as an end is, and ranges group to the left.
#[test]
fn precedence() {
    for (text, want) in [
        ("x.y.z[0][i..j]", "x.y.z[0][(i..j)]"),
        ("a + b..c * d", "((a + b)..(c * d))"),
        ("a orelse b..c", "(a orelse (b..c))"),
        ("a..b orelse c", "(a..(b orelse c))"),
        ("a..=b:c + d", "(a..=b:(c + d))"),
        ("a..b..c", "((a..b)..c)"),
        ("a[..b]", "a[(..b)]"),
        ("a[b..]", "a[(b..)]"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text}; }}"));
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
        let (_, errors) = parse_text(text);
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, [message], "{text}");
    }
}

/// The text of the body of every function of `text`, which must check
/// without errors.
fn bodies(text: &str) -> Vec<String> {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse::<Ranges>(&file);
    assert!(parse.is_empty(), "{parse:?}");
    let modules = [model::program::Module {
        path: "app:main".to_string(),
        ast,
        imports: Vec::new(),
    }];
    let tree = &modules[0].ast;
    let mut checker = Checker::new(&modules, Machine::named("x86_64-linux").unwrap(), false);
    let mut bodies = Vec::new();
    for (index, declaration) in tree.declarations().enumerate() {
        let ast::Decl::Function(function) = tree.decls[declaration] else {
            continue;
        };
        let definition = DefId {
            module: ModuleId::from_index(0),
            index: index as u32,
        };
        let copy = FunctionCopy {
            definition,
            arguments: ConstArguments::NONE,
        };
        let mut body = Body::default();
        assert!(checker.check_body(copy, function.name.span, &mut body, true));
        bodies.push(body.text(&checker.types, &file, |_| "f"));
    }
    let reported: Vec<_> = (checker.module_checks.iter())
        .flat_map(|module| &module.diagnostics)
        .map(|diagnostic| diagnostic.message.clone())
        .collect();
    assert!(reported.is_empty(), "{reported:?}");
    bodies
}

/// A range's literal start takes the end's type; its variables are
/// declared in the body. Its step is the ends' type made unsigned, 1
/// when not written.
#[test]
fn loops() {
    let bodies = bodies(
        "fn f(n: i64) {
                for i in 0..n { let j = i; }
                for 0..3 { }
                for k in 1..=n { }
                for i, m in n..0:2 { }
            }",
    );
    assert_eq!(
        bodies,
        ["    for i.1 in 0:i64..n.0 by 1:u64 {
        let j.2 = i.1
    }
    for 0:i32..3:i32 by 1:u32 {
    }
    for k.3 in 1:i64..=n.0 by 1:u64 {
    }
    for i.4, m.5 in n.0..0:i64 by 2:u64 {
    }
"]
    );
}
