//! Interfaces: `iface { fn name(&self) -> Result, ... }`. The checker gives
//! the core's interface type, so the phases after it know nothing of
//! them. Interface references, owned interfaces and the conversion of a
//! struct to an interface are the core's: they read the checked type.

use arena::Id;
use arena::List;
use check::Checker;
use check::declaration::first_copy_parameter;
use check::scope::report_redeclared;
use diagnostics::Diagnostic;
use model::DefId;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use parser::Parser;
use parser::Rules;
use parser::decl::type_decl;
use source::Span;
use syntax::ast;
use token::TokenKind;

use crate::FeatureType;
use crate::Peeper;
use crate::TypeNode;

/// `iface { methods }`: `Decl::Function`s without bodies.
#[derive(Clone, Copy, Debug)]
pub struct InterfaceType {
    pub methods: List<ast::Decl<Peeper>>,
}

impl InterfaceType {
    /// Adds the rules that read interfaces to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.declaration(TokenKind::Iface, type_decl);
        rules.type_start(TokenKind::Iface, interface_type);
    }
}

/// Reads `{ fn name(parameters) -> Result, ... }`, the methods of an
/// interface type that starts at `start`: function signatures without
/// bodies. A method takes no default values.
fn interface_type(p: &mut Parser<Peeper>, start: u32) -> Id<ast::Type<Peeper>> {
    let methods = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        |kind| kind == TokenKind::Fn,
        |p| {
            let start = p.next().span.start;
            let (name, signature) = p.named_signature();
            let parameters: Vec<_> = p.tree.children.lists.items(signature.parameters).collect();
            for parameter in parameters {
                if let Some(value) = p.tree.children.params[parameter].default {
                    let span = p.tree.exprs.span(value);
                    p.repair(
                        Diagnostic::new(
                            diagnostics::INVALID_DECLARATION,
                            "interface method defaults are not supported",
                        )
                        .label(&p.tree.file, span, ""),
                    );
                }
            }
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
    p.add(
        start,
        ast::Type::Feature(FeatureType::Interface(InterfaceType { methods })),
    )
}

impl TypeNode for InterfaceType {
    fn methods(&self) -> Option<List<ast::Decl<Peeper>>> {
        Some(self.methods)
    }

    /// The type an interface type written at `span` names: none yet, as only a
    /// declared interface is checked.
    fn resolve(&self, checker: &mut Checker<'_, Peeper>, span: Span) -> TypeId {
        checker.not_supported(span, "interface types")
    }

    /// The type the module's interface declaration `definition` declares:
    /// each method's function type, whose `self` is a reference to the
    /// interface (`Type::InterfaceReference`), or the interface taken whole.
    /// Invalid when a method has an error or is declared twice, as a struct
    /// is when a field has an error.
    fn declare(
        &self,
        checker: &mut Checker<'_, Peeper>,
        definition: DefId,
        _written: ast::TypeDecl<Peeper>,
    ) -> TypeId {
        let modules = checker.modules;
        let tree = &modules[definition.module.index()].ast;
        let declared: Vec<&ast::Function<Peeper>> =
            model::program::interface_methods(modules, definition)
                .expect("an interface declaration has methods")
                .collect();
        let mut methods = Vec::with_capacity(declared.len());
        for (index, method) in declared.iter().enumerate() {
            let text = method.name.text(&tree.file);
            // An interface has few methods, so the ones before are searched.
            if let Some(first) = declared[..index]
                .iter()
                .find(|first| first.name.text(&tree.file) == text)
            {
                let diagnostic = report_redeclared(
                    &tree.file,
                    text,
                    "interface",
                    method.name.span,
                    first.name.span,
                );
                checker.report(diagnostic);
                methods.push(TypeId::INVALID);
                continue;
            }
            let first = tree
                .children
                .lists
                .items(method.signature.parameters)
                .next();
            if !first.is_some_and(|first| tree.is_self(tree.children.params[first])) {
                let message = format!("an interface method takes `self` first: `fn {text}(&self)`");
                checker.error(diagnostics::INVALID_DECLARATION, message, method.name.span);
                methods.push(TypeId::INVALID);
                continue;
            }
            if let Some(span) = first_copy_parameter(tree, method.signature) {
                let message = "a call through an interface reference cannot pick a copy, so an interface method takes no type or `const` parameter";
                checker.error(diagnostics::INVALID_DECLARATION, message, span);
                methods.push(TypeId::INVALID);
                continue;
            }
            methods.push(checker.signature_type(method.signature, true, false));
        }
        let invalid = |ty: TypeId| match &checker.types[ty] {
            Type::Function {
                parameters, result, ..
            } => parameters.contains(&TypeId::INVALID) || *result == TypeId::INVALID,
            _ => true,
        };
        if methods.iter().any(|&method| invalid(method)) {
            return TypeId::INVALID;
        }
        let named = checker.types.intern(Type::Named(definition));
        checker.types.define_interface(named, methods.into());
        named
    }
}

#[cfg(test)]
mod tests;
