//! Interfaces: `iface { fn name(&self) -> Result, ... }`. The tree holds an
//! interface type (`ast::Type::Interface`) whose methods are function
//! signatures without bodies.

use crate::declaration;
use arena::Id;
use token::TokenKind;
use tree::ast;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read interfaces to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.declaration(TokenKind::Iface, declaration::type_decl);
    rules.type_start(TokenKind::Iface, interface_type);
}

/// Reads `{ fn name(parameters) -> Result, ... }`, the methods of an
/// interface type that starts at `start`: function signatures without
/// bodies. A method takes no default values.
fn interface_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let methods = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        |kind| kind == TokenKind::Fn,
        |p| {
            let start = p.next().span.start;
            let (name, signature) = p.named_signature();
            p.add(
                start,
                ast::Decl::Function(ast::Function {
                    metadata: ast::Metadata::NONE,
                    owner: None,
                    name,
                    signature,
                    body: None,
                }),
            )
        },
    );
    p.add(start, ast::Type::Interface { methods })
}

#[cfg(test)]
mod tests {
    //! Reading interfaces.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// The written type `ty` in short: `&self`, `&mut self`, or its text.
    fn written_type(tree: &ast::Tree, ty: Id<ast::Type>) -> String {
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
    fn methods(tree: &ast::Tree, ty: Id<ast::Type>) -> Vec<String> {
        let ast::Type::Interface { methods } = tree.types[ty] else {
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
        let text =
            "iface Summer {\n    fn sum(&self) -> i32,\n    fn add<T>(&mut self, value: T)\n}\n";
        let (tree, errors) = testing::parse_text(text);
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

    /// A method's default value is read as any function's: checking a
    /// signature refuses it, for every function alike.
    #[test]
    fn default_values_are_read() {
        let (tree, errors) = testing::parse_text("iface I { fn f(&self, a: i32 = 1) }");
        assert!(errors.is_empty());
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
            let (tree, _) = testing::parse_text(text);
            let spans = (tree.types.ids().map(|id| tree.types.span(id)))
                .chain(tree.decls.ids().map(|id| tree.decls.span(id)))
                .chain((tree.children.params.ids()).map(|id| tree.children.params.span(id)));
            for span in spans {
                let (start, end) = (span.start as usize, span.end as usize);
                assert!(start <= end && end <= text.len(), "{text:?}: {span:?}");
            }
        }
    }
}
