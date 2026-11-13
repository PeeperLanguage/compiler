//! Checking interface declarations into the core's interface type
//! (`Type::Interface`).

use check::Checked;
use check::Checker;
use check::declaration::const_parameter;
use check::declaration::is_self;
use check::scope::report_redeclared;
use model::DefId;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use source::Span;
use syntax::ast;

/// The type an interface type written at `span` names: none yet, as only a
/// declared interface is checked.
pub fn resolve_type<L: Checked>(checker: &mut Checker<'_, L>, span: Span) -> TypeId {
    checker.not_supported(span, "interface types")
}

/// The type the module's interface declaration `definition` declares:
/// each method's function type, whose `self` is a reference to the
/// interface (`Type::InterfaceReference`), or the interface taken whole.
/// Invalid when a method has an error or is declared twice, as a struct
/// is when a field has an error.
pub fn declare<L: Checked>(checker: &mut Checker<'_, L>, definition: DefId) -> TypeId {
    let modules = checker.modules;
    let tree = &modules[definition.module.index()].ast;
    let declared: Vec<&ast::Function<L>> = model::program::interface_methods(modules, definition)
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
        if !first.is_some_and(|first| is_self(tree, tree.children.params[first])) {
            let message = format!("an interface method takes `self` first: `fn {text}(&self)`");
            checker.error(diagnostics::INVALID_DECLARATION, message, method.name.span);
            methods.push(TypeId::INVALID);
            continue;
        }
        if let Some(span) = const_parameter(tree, method.signature) {
            let message = "a call through an interface reference cannot pick a copy, so an interface method takes no `const` parameter";
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
