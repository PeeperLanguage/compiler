//! Interfaces: checking `iface { fn name(&self) -> Result, ... }`
//! declarations. The checker gives a `Type::Interface`, so the phases after
//! it know nothing of this file. Interface references, owned interfaces and
//! the conversion of a struct to an interface read that checked type
//! wherever they are checked.

use crate::functions;
use crate::scope;
use model::DefId;
use model::semantic_types;
use source::Span;
use tree::ast;

use crate::Checker;

/// The type an interface type written at `span` names: none yet, as only a
/// declared interface is checked.
pub(crate) fn resolve_interface_type(
    checker: &mut Checker<'_>,
    span: Span,
) -> semantic_types::TypeId {
    checker.not_supported(span, "interface types")
}

/// The type the module's interface declaration `definition` declares:
/// each method's function type, whose `self` is a reference to the
/// interface (`Type::InterfaceReference`), or the interface taken whole.
/// Invalid when a method has an error or is declared twice, as a struct
/// is when a field has an error.
pub(crate) fn declare_interface_type(
    checker: &mut Checker<'_>,
    definition: DefId,
) -> semantic_types::TypeId {
    let modules = checker.modules;
    let tree = &modules[definition.module.index()].ast;
    let declared: Vec<&ast::Function> = model::program::interface_methods(modules, definition)
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
            let diagnostic = scope::report_redeclared(
                &tree.file,
                text,
                "interface",
                method.name.span,
                first.name.span,
            );
            checker.report(diagnostic);
            methods.push(semantic_types::TypeId::INVALID);
            continue;
        }
        let kind = &functions::FunctionKind::INTERFACE_METHOD;
        methods.push(checker.signature_type(method.name, method.signature, true, kind));
    }
    let invalid = |ty: semantic_types::TypeId| match &checker.types[ty] {
        semantic_types::Type::Function {
            parameters, result, ..
        } => {
            parameters.contains(&semantic_types::TypeId::INVALID)
                || *result == semantic_types::TypeId::INVALID
        }
        _ => true,
    };
    if methods.iter().any(|&method| invalid(method)) {
        return semantic_types::TypeId::INVALID;
    }
    let named = checker.declared_name(definition);
    checker.types.define_interface(named, methods.into());
    named
}
