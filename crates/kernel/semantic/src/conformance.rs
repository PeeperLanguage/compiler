//! Interface conformance: whether a type has every function an interface
//! asks for, with the signature it asks for, its receiver the type's own;
//! each answer is kept as an `ImplementationId`.

use model::DefId;
use model::semantic_types;
use model::thir;
use source::Span;

use crate::Checked;
use crate::Checker;
use crate::generics;

impl<L: Checked> Checker<'_, L> {
    /// The functions of the struct `structure` that are the methods of the
    /// interface `interface`, for a reference to the struct used as a
    /// reference to the interface at `span`: for each method, the struct's
    /// function of its name and type, `self` being the struct; looked up
    /// once for the pair. `None` after reporting the first method the struct
    /// lacks or has with another type, or when the interface's error was
    /// reported; such a pair is reported at each conversion.
    pub(crate) fn implementation(
        &mut self,
        structure: semantic_types::TypeId,
        interface: semantic_types::TypeId,
        span: Span,
    ) -> Option<semantic_types::ImplementationId> {
        if let Some(&found) = self
            .internals
            .known_implementations
            .get(&(structure, interface))
        {
            return Some(found);
        }
        let modules = self.modules;
        let Some(semantic_types::Members {
            definition: owner, ..
        }) = self.types[structure].members()
        else {
            unreachable!(
                "only a reference to a type with members converts to an interface reference"
            )
        };
        let (semantic_types::Type::Named { definition, .. }
        | semantic_types::Type::Interface { definition, .. }) = self.types[interface]
        else {
            unreachable!("an interface reference refers to an interface")
        };
        if self.declared_type(definition, span) == semantic_types::TypeId::INVALID {
            return None;
        }
        let semantic_types::Type::Interface { methods, .. } = &self.types[interface] else {
            unreachable!("a checked interface is `Type::Interface`")
        };
        let signatures = methods.clone();
        let declared = model::program::interface_methods(modules, definition)
            .expect("an interface declaration has methods");
        let file = &modules[definition.module.index()].ast.file;
        let mut found = Vec::with_capacity(signatures.len());
        for (method, &signature) in declared.zip(&signatures) {
            let name = method.name.text(file);
            let function = match self.module_checks[owner.module.index()]
                .scope
                .get(Some(owner), name)
            {
                Some(thir::Symbol::Function(function)) => Some(function),
                _ => None,
            };
            let expected = self.method_type(signature, structure);
            let ty = function.map(|function| (function, self.function_type(function, span)));
            let generic = |function: DefId| {
                let tree = &modules[function.module.index()].ast;
                generics::first_copy_parameter(
                    tree,
                    model::program::function_declaration(modules, function).signature,
                )
                .is_some()
            };
            let message = match ty {
                Some((function, _)) if generic(function) => {
                    format!(
                        "its `{name}` takes a `const` parameter, so it is not one function an interface reference can call"
                    )
                }
                Some((_, semantic_types::TypeId::INVALID)) => return None, // reported
                Some((function, ty)) if ty == expected => {
                    if model::program::is_extern(modules, function) {
                        self.not_supported(span, "C functions as interface methods");
                        return None;
                    }
                    found.push(function);
                    continue;
                }
                Some((_, ty)) => format!(
                    "its `{name}` is `{}`, but `{}` needs `{}`",
                    self.type_text(ty),
                    self.type_text(interface),
                    self.type_text(expected)
                ),
                None => format!("it has no method `{name}`"),
            };
            let (structure, interface) = self.type_texts(structure, interface);
            let message = format!("`{structure}` does not implement `{interface}`: {message}");
            self.error(diagnostics::TYPE_MISMATCH, message, span);
            return None;
        }
        let id = self.types.add_implementation(found.into());
        self.internals
            .known_implementations
            .insert((structure, interface), id);
        Some(id)
    }

    /// The type a function of the struct `structure` has when it is the
    /// interface method of the type `signature`: the same, with `self` the
    /// struct.
    fn method_type(
        &mut self,
        signature: semantic_types::TypeId,
        structure: semantic_types::TypeId,
    ) -> semantic_types::TypeId {
        let semantic_types::Type::Function {
            mut parameters,
            result,
            origins,
            variadic,
        } = self.types[signature].clone()
        else {
            unreachable!("a method's type is a function type")
        };
        parameters[0] = match self.types[parameters[0]] {
            semantic_types::Type::InterfaceReference { mutable, .. } => {
                self.types.intern(semantic_types::Type::Reference {
                    mutable,
                    target: structure,
                })
            }
            _ => structure, // `self` taken whole
        };
        self.types.intern(semantic_types::Type::Function {
            parameters,
            result,
            origins,
            variadic,
        })
    }
}
