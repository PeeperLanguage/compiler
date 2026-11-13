//! Reading interfaces. The parser's tree dump is its own tests', so these
//! look at the tree.

use std::convert::Infallible;
use std::sync::Arc;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Rules;
use source::SourceFile;
use syntax::ast;
use syntax::ast::Language;
use syntax::operators::Precedence;

use super::*;

/// The language the interface tests are written in: the core and
/// interfaces.
#[derive(Clone, Copy, Debug)]
enum Interfaces {}

impl Language for Interfaces {
    type Expr = Infallible;
    type Type = InterfaceType<Interfaces>;

    fn precedence(expr: &Infallible) -> Precedence {
        match *expr {}
    }

    fn fields(_: &InterfaceType<Interfaces>) -> Option<List<ast::Field<Interfaces>>> {
        None
    }

    fn methods(ty: &InterfaceType<Interfaces>) -> Option<List<ast::Decl<Interfaces>>> {
        Some(ty.methods)
    }
}

static RULES: Rules<Interfaces> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Interfaces {
    fn rules() -> &'static Rules<Interfaces> {
        &RULES
    }
}

type Tree = ast::Tree<Interfaces>;

fn parse_text(text: &str) -> (Tree, Vec<Diagnostic>) {
    parser::parse(&Arc::new(SourceFile::new("t.peep", text)))
}

/// The written type `ty` in short: `&self`, `&mut self`, or its text.
fn written_type(tree: &Tree, ty: Id<ast::Type<Interfaces>>) -> String {
    match tree.types[ty] {
        ast::Type::Reference { mutable, target }
            if matches!(tree.types[target], ast::Type::Owner) =>
        {
            if mutable { "&mut self" } else { "&self" }.to_string()
        }
        _ => tree.file.slice(tree.types.span(ty)).to_string(),
    }
}

/// Each method of the interface type `ty` as `name<T>(name: type, ...) -> result`.
fn methods(tree: &Tree, ty: Id<ast::Type<Interfaces>>) -> Vec<String> {
    let ast::Type::Feature(InterfaceType { methods }) = tree.types[ty] else {
        panic!("not an interface type: {:?}", tree.types[ty])
    };
    let method = |id| {
        let ast::Decl::Function(function) = tree.decls[id] else {
            panic!("an interface holds functions")
        };
        assert!(function.body.is_none(), "a method has no body");
        let signature = function.signature;
        let names = tree.children.lists.items(signature.type_parameters);
        let names: Vec<_> = names
            .map(|name| tree.file.slice(tree.children.names.span(name)))
            .collect();
        let parameters: Vec<_> = (tree.children.lists.items(signature.parameters))
            .map(|parameter| {
                let ast::Param { name, ty, .. } = tree.children.params[parameter];
                let name = name.map_or("_", |name| name.text(&tree.file));
                format!("{name}: {}", written_type(tree, ty))
            })
            .collect();
        let result = signature
            .result
            .map(|result| format!(" -> {}", written_type(tree, result)));
        format!(
            "{}<{}>({}){}",
            function.name.text(&tree.file),
            names.join(", "),
            parameters.join(", "),
            result.unwrap_or_default()
        )
    };
    tree.children.lists.items(methods).map(method).collect()
}

/// An interface declaration's type is an interface type, whose methods are
/// signatures without bodies.
#[test]
fn parses_declarations() {
    let text = "iface Summer {\n    fn sum(&self) -> i32,\n    fn add<T>(&mut self, value: T)\n}\n";
    let (tree, errors) = parse_text(text);
    assert!(errors.is_empty(), "{errors:?}");
    let declarations: Vec<_> = tree.declarations().collect();
    assert_eq!(declarations.len(), 1);
    let ast::Decl::Type(declared) = tree.decls[declarations[0]] else {
        panic!("not a type declaration")
    };
    assert_eq!(declared.name.text(&tree.file), "Summer");
    assert_eq!(
        methods(&tree, declared.ty),
        [
            "sum<>(self: &self) -> i32",
            "add<T>(self: &mut self, value: T)"
        ]
    );
}

/// A method takes no default value; the rest of the interface is still
/// read.
#[test]
fn declaration_errors() {
    let (tree, errors) = parse_text("iface I { fn f(&self, a: i32 = 1) }");
    let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
    assert_eq!(got, ["interface method defaults are not supported"]);
    let ast::Decl::Type(declared) = tree.decls[tree.declarations().next().unwrap()] else {
        panic!("not a type declaration")
    };
    assert_eq!(methods(&tree, declared.ty), ["f<>(self: &self, a: i32)"]);
}

/// Every span of a tree read from broken interfaces lies in the text and
/// starts before it ends.
#[test]
fn spans_lie_in_the_text() {
    for text in ["iface I { fn", "iface I { fn (", "iface", "iface I {"] {
        let (tree, _) = parse_text(text);
        let spans = (tree.types.ids().map(|id| tree.types.span(id)))
            .chain(tree.decls.ids().map(|id| tree.decls.span(id)))
            .chain((tree.children.params.ids()).map(|id| tree.children.params.span(id)));
        for span in spans {
            let (start, end) = (span.start as usize, span.end as usize);
            assert!(start <= end && end <= text.len(), "{text:?}: {span:?}");
        }
    }
}
