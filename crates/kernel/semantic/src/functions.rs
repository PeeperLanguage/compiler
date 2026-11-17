//! Functions: a function's type from its signature, what its declaration
//! may hold, its body (`bodies`), calls (`calls`) and the `#[builtin]`
//! functions (`builtin`). A method is a function with an owner.

pub(crate) mod bodies;
pub(crate) mod builtin;
pub(crate) mod calls;

use arena::Id;
use diagnostics::Diagnostic;
use model::DefId;
use model::semantic_types;
use source::Span;
use syntax::ast;

use crate::Checked;
use crate::Checker;
use crate::generics;
use crate::scope;

/// What a function's declaration may hold, from who gives its code: its
/// body, C, the compiler or each type that implements its interface. Each
/// rule reads it (`Checker::signature_type`, and `Checker::check_function`
/// for the body).
pub struct FunctionKind {
    /// How a message names such a function.
    pub name: &'static str,
    /// Who gives its code, as a message says it.
    pub coded_by: &'static str,
    pub has_body: bool,
    /// Whether it may take type or `const` parameters, which make a copy
    /// for each call.
    pub has_copies: bool,
    pub takes_variadic: bool,
    pub takes_self_first: bool,
    /// Whether its parameters and result must be C types.
    pub c_types: bool,
}

impl FunctionKind {
    pub const BODY: FunctionKind = FunctionKind {
        name: "a function",
        coded_by: "its body",
        has_body: true,
        has_copies: true,
        takes_variadic: true,
        takes_self_first: false,
        c_types: false,
    };
    pub const C: FunctionKind = FunctionKind {
        name: "an `extern` function",
        coded_by: "C",
        has_body: false,
        has_copies: false,
        takes_variadic: false,
        takes_self_first: false,
        c_types: true,
    };
    pub const COMPILER: FunctionKind = FunctionKind {
        name: "a `#[builtin]` function",
        coded_by: "the compiler",
        has_body: false,
        has_copies: true,
        takes_variadic: true,
        takes_self_first: false,
        c_types: false,
    };
    pub const INTERFACE_METHOD: FunctionKind = FunctionKind {
        name: "an interface method",
        coded_by: "each type that implements it",
        has_body: false,
        has_copies: false,
        takes_variadic: true,
        takes_self_first: true,
        c_types: false,
    };
}

impl<L: Checked> Checker<'_, L> {
    /// The type of the module's function or method `definition`.
    pub(crate) fn check_function(&mut self, definition: DefId) -> semantic_types::TypeId {
        let tree = &self.module().ast;
        let &ast::Function {
            owner,
            name,
            signature,
            body,
            ..
        } = model::program::function_declaration(self.modules, definition);
        // shortcut: a generic struct's functions are refused until a copy of
        // one takes its owner's type arguments.
        if let Some(written) = owner
            && let Some(owner) = (self.scope()).owner_named(self.modules, written.text(&tree.file))
            && (model::program::declaration_copy_parameters(self.modules, owner).next()).is_some()
        {
            return self.not_supported(written.span, "functions of generic types");
        }
        let symbol = model::program::extern_symbol(self.modules, definition);
        if let Err(span) = symbol {
            let message = "write `#[extern(\"name\")]` once, with the C function's name";
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
        // A malformed `extern` is still read as one, so its signature gets
        // the C rules.
        let builtin = model::program::is_builtin(self.modules, definition);
        let kind = if symbol != Ok(None) {
            &FunctionKind::C
        } else if builtin {
            &FunctionKind::COMPILER
        } else {
            &FunctionKind::BODY
        };
        let first_const = generics::first_copy_parameter(tree, signature);
        let module = definition.module.index();
        let reported = self.module_checks[module].diagnostics.len();
        let ty = self.signature_type(name, signature, owner.is_some(), kind);
        // A copy's signature differs only by what it is given: an error in
        // the function's own is in each copy's.
        let failed = || {
            let found = &self.module_checks[module].diagnostics[reported..];
            found.iter().any(Diagnostic::is_error)
        };
        if ty == semantic_types::TypeId::INVALID || first_const.is_some() && failed() {
            return semantic_types::TypeId::INVALID;
        }
        if builtin && !self.is_builtin_declared(definition, ty) {
            return semantic_types::TypeId::INVALID; // reported
        }
        if kind.has_body != body.is_some() {
            let message = if kind.has_body {
                "a function needs a body, or `#[extern(\"name\")]` when C defines it".to_owned()
            } else {
                format!(
                    "{} has no body: {} gives its code",
                    kind.name, kind.coded_by
                )
            };
            return self.error(diagnostics::INVALID_DECLARATION, message, name.span);
        }
        // Reported above; invalid, so no call of it is lowered.
        if symbol.is_err() {
            return semantic_types::TypeId::INVALID;
        }
        ty
    }

    /// Whether the `#[builtin]` function `definition`, of the type `ty`, is
    /// one the compiler has, declared as the compiler answers it: in the
    /// prelude, taking one type and giving the fact's type. Reports it when
    /// not.
    fn is_builtin_declared(&mut self, definition: DefId, ty: semantic_types::TypeId) -> bool {
        let tree = &self.module().ast;
        let &ast::Function {
            name, signature, ..
        } = model::program::function_declaration(self.modules, definition);
        let text = name.text(&tree.file);
        let message = match builtin::type_fact(text) {
            // A module with no prelude is one, as a test's is.
            _ if self.prelude().is_some() => {
                "only the `core` library's prelude declares a `#[builtin]` function".to_owned()
            }
            None => format!("the compiler has no built-in function `{text}`"),
            Some(fact) => {
                let (gives, written) = match fact {
                    builtin::TypeFact::Signed
                    | builtin::TypeFact::IsInteger
                    | builtin::TypeFact::IsFloat
                    | builtin::TypeFact::IsText
                    | builtin::TypeFact::IsBool
                    | builtin::TypeFact::IsPointer => (semantic_types::TypeId::BOOL, "bool"),
                    builtin::TypeFact::Size
                    | builtin::TypeFact::Align
                    | builtin::TypeFact::Bits => (self.types.usize, "usize"),
                };
                let mut parameters = (tree.children.lists.items(signature.parameters))
                    .map(|parameter| tree.children.params[parameter]);
                let takes_a_type = matches!(
                    (parameters.next(), parameters.next()),
                    (Some(parameter), None)
                        if parameter.constant && matches!(tree.types[parameter.ty], ast::Type::Type)
                );
                let result = match &self.types[ty] {
                    semantic_types::Type::Function { result, .. } => *result,
                    _ => unreachable!("a function's type is a function type"),
                };
                if takes_a_type && result == gives {
                    return true;
                }
                format!("the compiler's `{text}` is `fn {text}(const T: type) -> {written}`")
            }
        };
        self.error(diagnostics::INVALID_DECLARATION, message, name.span);
        false
    }

    /// The function type `signature` of the module's tree declares, for the
    /// function `name`, of a type's function or an interface's method when
    /// `has_owner` (only these take `self`), of the kind `kind`.
    /// Invalid when the signature is reported; a parameter or result whose
    /// type is reported is invalid inside it. A `const T: type` parameter is
    /// not among its parameters: each call gives it a type, which makes its
    /// own copy (`copy_type`).
    pub fn signature_type(
        &mut self,
        name: ast::Ident,
        signature: ast::Signature<L>,
        has_owner: bool,
        kind: &FunctionKind,
    ) -> semantic_types::TypeId {
        let tree = &self.module().ast;
        let first = tree.children.lists.items(signature.parameters).next();
        if kind.takes_self_first
            && !first.is_some_and(|first| tree.is_self(tree.children.params[first]))
        {
            let (what, text) = (kind.name, name.text(&tree.file));
            let message = format!("{what} takes `self` first: `fn {text}(&self)`");
            return self.error(diagnostics::INVALID_DECLARATION, message, name.span);
        }
        if !kind.has_copies
            && let Some(span) = generics::first_copy_parameter(tree, signature)
        {
            let message = format!(
                "{} takes no type or `const` parameter: {} gives one function for every call",
                kind.name, kind.coded_by
            );
            return self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
        // Each name that stands for a type, `<T>` or `const T: type`, once.
        let type_names: Vec<Span> = (model::program::copy_parameters(tree, signature))
            .flatten()
            .collect();
        for (place, &again) in type_names.iter().enumerate() {
            let name = tree.file.slice(again);
            let mut earlier = type_names[..place].iter();
            if let Some(&first) = earlier.find(|&&first| tree.file.slice(first) == name) {
                let file = &tree.file;
                self.report(scope::report_redeclared(
                    file, name, "function", again, first,
                ));
                return semantic_types::TypeId::INVALID;
            }
        }
        let tree = &self.module().ast;
        let mut parameters = Vec::with_capacity(signature.parameters.len());
        let mut variadic = false;
        for (position, parameter) in tree.children.lists.items(signature.parameters).enumerate() {
            let parameter = tree.children.params[parameter];
            if let Some(default) = parameter.default {
                return self.not_supported(tree.exprs.span(default), "default parameters");
            }
            let span = tree.types.span(parameter.ty);
            let is_variadic = matches!(tree.types[parameter.ty], ast::Type::Variadic { .. });
            if parameter.constant {
                if parameter.mutable || is_variadic {
                    let message = "a `const` parameter is one value known when compiling, so it takes no `mut` or `...`";
                    return self.error(diagnostics::INVALID_DECLARATION, message, span);
                }
                if let ast::Type::Type = tree.types[parameter.ty] {
                    continue;
                }
                if self.resolve_value_type(parameter.ty) != semantic_types::TypeId::INVALID {
                    continue;
                }
                // A value's type is read again at each call (`const_value_type`),
                // which cannot know a type parameter yet: only the copy does.
                // Any other error in it was reported.
                let tree = &self.module().ast;
                let type_parameters: Vec<&str> = (model::program::copy_parameters(tree, signature))
                    .flatten()
                    .map(|name| tree.file.slice(name))
                    .collect();
                if generics::names_type_parameter(tree, parameter.ty, &type_parameters) {
                    return self
                        .not_supported(span, "`const` values whose type names a type parameter");
                }
                return semantic_types::TypeId::INVALID;
            }
            if is_variadic && !kind.takes_variadic {
                let message = format!("{} cannot have a `...` parameter", kind.name);
                return self.error(diagnostics::INVALID_DECLARATION, message, span);
            }
            if is_variadic && position + 1 != signature.parameters.len() {
                let message = "a `...` parameter must be the last one";
                return self.error(diagnostics::INVALID_DECLARATION, message, span);
            }
            if tree.is_self(parameter) {
                let message = match (position, has_owner) {
                    (0, true) => None,
                    (0, false) => {
                        Some("only a type's function takes `self`: `fn Point::name(&self)`")
                    }
                    _ => Some("`self` must be the first parameter"),
                };
                if let Some(message) = message {
                    return self.error(diagnostics::INVALID_DECLARATION, message, span);
                }
            }
            variadic |= is_variadic;
            parameters.push(self.resolve_signature_type(parameter.ty, kind.c_types));
        }
        let result = signature
            .result
            .map_or(semantic_types::TypeId::VOID, |result| {
                self.resolve_signature_type(result, kind.c_types)
            });
        let Some(origins) = self.result_origins(&signature, &parameters, result) else {
            return semantic_types::TypeId::INVALID;
        };
        if !signature.constraints.is_empty() && !self.constraints_hold(signature, &parameters) {
            return semantic_types::TypeId::INVALID;
        }
        let parameters = parameters.into();
        self.types.intern(semantic_types::Type::Function {
            parameters,
            result,
            origins,
            variadic,
        })
    }

    /// The positions of the parameters a `result` of `signature` holding
    /// references may point into, in order: those `in` names, else every
    /// parameter that holds one (every `&mut` one for a `&mut` result).
    /// `None` when an error was reported.
    fn result_origins(
        &mut self,
        signature: &ast::Signature<L>,
        parameters: &[semantic_types::TypeId],
        result: semantic_types::TypeId,
    ) -> Option<Box<[u32]>> {
        let tree = &self.module().ast;
        let file = &tree.file;
        let mut names = tree.children.lists.items(signature.origins);
        // An invalid type was reported, or is a `const T: type` parameter's
        // in the function's own signature, which no copy gives one.
        if result == semantic_types::TypeId::INVALID {
            return Some(Box::default());
        }
        if self.types.holds_loans(result).is_none() {
            if let Some(origin) = names.next() {
                let span = tree.children.names.span(origin);
                self.error(
                    diagnostics::INVALID_RETURN,
                    "`in` needs a reference or slice result",
                    span,
                );
                return None;
            }
            return Some(Box::default());
        }
        // A struct may hold shared references too, so any parameter may be
        // where they come from; the body's returns are checked by the loans.
        let mutable = self.types[result].borrows() == Some(true);
        let mut origins: Vec<u32> = Vec::new();
        for origin in names {
            let span = tree.children.names.span(origin);
            let name = file.slice(span);
            // A `const T: type` parameter is not among the function's.
            let position = (tree.children.lists.items(signature.parameters))
                .map(|parameter| tree.children.params[parameter])
                .filter(|parameter| !parameter.constant)
                .position(|parameter| {
                    parameter
                        .name
                        .is_some_and(|written| written.text(file) == name)
                });
            let error = match position {
                None => Some((
                    diagnostics::UNDEFINED_NAME,
                    format!("`{name}` is not a parameter"),
                )),
                Some(position) if parameters[position] == semantic_types::TypeId::INVALID => None,
                // `Some(whether it can lend mutably)` for a parameter that lends.
                Some(position) => match self.types.holds_loans(parameters[position]) {
                    None => Some((
                        diagnostics::INVALID_RETURN,
                        format!(
                            "`{name}` is not a reference or a slice, so a result cannot borrow from it"
                        ),
                    )),
                    Some(false) if mutable => Some((
                        diagnostics::INVALID_RETURN,
                        if self.types[parameters[position]].borrows().is_some() {
                            format!("a `&mut` result cannot borrow from shared reference `{name}`")
                        } else {
                            format!(
                                "a `&mut` result cannot borrow from `{name}`, which holds only shared references"
                            )
                        },
                    )),
                    Some(_) if origins.contains(&(position as u32)) => Some((
                        diagnostics::INVALID_RETURN,
                        format!("`{name}` is named twice in `in`"),
                    )),
                    Some(_) => {
                        origins.push(position as u32);
                        None
                    }
                },
            };
            if let Some((code, message)) = error {
                self.error(code, message, span);
                return None;
            }
        }
        if signature.origins.is_empty() {
            origins = (0..parameters.len())
                .filter(|&position| {
                    self.types
                        .holds_loans(parameters[position])
                        .is_some_and(|source| source || !mutable)
                })
                .map(|position| position as u32)
                .collect();
            if origins.is_empty() {
                let kind = if mutable {
                    "`&mut` reference or slice"
                } else {
                    "reference or slice"
                };
                let message = if self.types[result].borrows().is_some() {
                    format!("a {kind} result needs a {kind} parameter to borrow from")
                } else if self.is_interface(result) {
                    let ty = self.type_text(result);
                    format!(
                        "a `{ty}` result needs a parameter to come from: a struct the function moves into it is gone once it returns"
                    )
                } else {
                    "a result holding references needs a parameter that holds one".to_owned()
                };
                let result = signature.result.expect("a reference result is written");
                self.error(
                    diagnostics::INVALID_RETURN,
                    message,
                    tree.types.span(result),
                );
                return None;
            }
        }
        origins.sort_unstable();
        Some(origins.into())
    }

    /// The type of a parameter or result written `ty`, which must be a C
    /// type when `c_types` (`FunctionKind::c_types`).
    fn resolve_signature_type(
        &mut self,
        ty: Id<ast::Type<L>>,
        c_types: bool,
    ) -> semantic_types::TypeId {
        let resolved = self.resolve_value_type(ty);
        if !c_types {
            return resolved;
        }
        let span = self.module().ast.types.span(ty);
        match self.types[resolved] {
            // shortcut: structs, arrays, ranges, slices and `&str` pass as
            // LLVM values, which the C ABI does not match; allow structs and
            // arrays when its aggregate rules are written (a range, slice or
            // `&str` has no C type).
            semantic_types::Type::Struct { .. }
            | semantic_types::Type::Array { .. }
            | semantic_types::Type::Range { .. }
            | semantic_types::Type::Slice { .. }
            | semantic_types::Type::Str => {
                let what = "struct, array, range, slice and `&str` parameters and results of `extern` functions";
                self.not_supported(span, what)
            }
            semantic_types::Type::InterfaceReference { .. } => {
                self.not_supported(span, "interface references in `extern` functions")
            }
            semantic_types::Type::Interface { .. } => {
                self.not_supported(span, "owned interfaces in `extern` functions")
            }
            // C could keep what the target holds, which no loan would follow.
            semantic_types::Type::Reference { target, .. }
                if self.types.holds_loans(target).is_some() =>
            {
                let what = self.loan_holder_text(target);
                self.not_supported(span, &format!("references to {what} in `extern` functions"))
            }
            semantic_types::Type::Integer(integer)
                if integer.bits > self.target.widest_c_integer() =>
            {
                let (target, widest) = (self.target.name, self.target.widest_c_integer());
                let message = format!(
                    "C has no `{integer}` on {target}, whose integers have at most {widest} bits"
                );
                self.error(diagnostics::INVALID_TYPE, message, span)
            }
            _ => resolved,
        }
    }
}

#[cfg(test)]
mod tests {
    use model::DefId;
    use model::semantic_types;
    use syntax::ast;

    use crate::testing;

    /// The type of every function and type declaration of `text`, as type
    /// text, and the diagnostics.
    fn declared_types(text: &str) -> (Vec<String>, Vec<testing::Reported>) {
        let (found, types, reported) = testing::with_checker(text, |checker, main| {
            let tree = &checker.modules[main.index()].ast;
            let found: Vec<semantic_types::TypeId> = tree
                .declarations()
                .enumerate()
                .map(|(index, declaration)| {
                    let definition = DefId {
                        module: main,
                        index: index as u32,
                    };
                    // As a compile asks, so a use does not report it again.
                    let written = tree.decls[declaration];
                    let name_span = written.name().span;
                    match written {
                        ast::Decl::Function(_) => checker.function_type(definition, name_span),
                        _ => checker.declared_type(definition, name_span),
                    }
                })
                .collect();
            found
        });
        let text_of = |ty: semantic_types::TypeId| match &types[ty] {
            semantic_types::Type::Function {
                parameters, result, ..
            } => format!("fn{parameters:?} -> {result:?}"),
            other => format!("{other:?}"),
        };
        (found.into_iter().map(text_of).collect(), reported)
    }

    #[test]
    fn function_types() {
        let (found, reported) = declared_types(
            "fn add(a: i32, b: i32) -> i32 { return a; }\nfn main() {}\nfn take(s: &cstr) {}\n",
        );
        assert!(reported.is_empty(), "{reported:?}");
        let i32 = format!("{:?}", semantic_types::TypeId::I32);
        let void = format!("{:?}", semantic_types::TypeId::VOID);
        assert_eq!(found[0], format!("fn[{i32}, {i32}] -> {i32}"));
        assert_eq!(found[1], format!("fn[] -> {void}"));
        assert_eq!(
            found[2],
            format!("fn[{:?}] -> {void}", semantic_types::TypeId::CSTR)
        );
    }

    /// An `extern` function is C's, so it has no body here, and any other
    /// function has one.
    #[test]
    fn only_an_extern_function_has_no_body() {
        let (found, reported) =
            declared_types("#[extern(\"f\")]\nfn f(a: i8) -> i8 { return a; }\nfn g();\n");
        let invalid = format!("{:?}", semantic_types::Type::Invalid);
        assert_eq!(found, [invalid.clone(), invalid]);
        let body = "a function needs a body, or `#[extern(\"name\")]` when C defines it";
        let extern_body = "an `extern` function has no body: C gives its code";
        assert_eq!(
            reported,
            crate::testing::reported(&[("P0005", extern_body, "f"), ("P0005", body, "g")])
        );
    }
}
