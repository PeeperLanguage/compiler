//! Module-scope declarations, without their bodies, one check per category:
//! a type declaration's type, a function's type and a constant's type and
//! value.

use arena::Id;
use diagnostics::Diagnostic;
use model::DefId;
use model::ast;
use model::constant::Constant;
use model::layout::layout;
use model::layout::struct_layout;
use model::program::EXTERN_ATTRIBUTE;
use model::semantic_types;
use model::semantic_types::ConstArgument;
use model::semantic_types::ConstArguments;
use model::semantic_types::ImplementationId;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Body;
use model::thir::FactKind;
use model::thir::Symbol;
use source::Span;

use crate::Checker;
use crate::Checking;
use crate::builder::ThirBuilder;
use crate::builtin::unsupported_type;
use crate::function::function_declaration;
use crate::scope;
use crate::scope::report_redeclared;

/// The most references and slices a struct holds, however deep.
// shortcut: the loan check keeps each one apart, so a struct that holds the
// last one twice costs twice as much at each level, and 40 of them need 2^39
// holders. Real structs hold a few; lift this if the check ever tracks them
// by lifetime, as Rust does.
const MOST_HOLDERS: usize = 4_096;

impl Checker<'_> {
    /// The type the module's type declaration or alias `definition` declares:
    /// a struct's type (`Type::Struct`), an interface's (`Type::Interface`),
    /// or the type an alias names. Enums and type parameters are not
    /// supported yet.
    pub(crate) fn check_type_declaration(&mut self, definition: DefId) -> TypeId {
        let tree = &self.module().ast;
        let (written, is_alias) = match tree.decls[tree.declaration(definition.index)] {
            ast::Decl::Type(written) => (written, false),
            ast::Decl::Alias(written) => (written, true),
            _ => unreachable!("`{definition:?}` is a type declaration"),
        };
        if !written.type_parameters.is_empty() {
            return self.not_supported(written.name.span, "generic types");
        }
        if is_alias {
            return self.resolve_value_type(written.ty);
        }
        let what = match tree.types[written.ty] {
            ast::Type::Struct(fields) => {
                let mut field_types = Vec::with_capacity(fields.len());
                let name_of = |field: Id<ast::Field>| tree.children.fields[field].name;
                for (index, field) in tree.children.lists.items(fields).enumerate() {
                    let ast::Field { name, ty } = tree.children.fields[field];
                    let text = name.text(&tree.file);
                    // A struct has few fields, so the ones before are searched.
                    let mut before = tree.children.lists.items(fields).take(index).map(name_of);
                    if let Some(first) = before.find(|first| first.text(&tree.file) == text) {
                        let diagnostic =
                            report_redeclared(&tree.file, text, "struct", name.span, first.span);
                        self.report(diagnostic);
                    }
                    // A struct held by value is checked here: one that holds
                    // itself, however deep, is a cycle, as is one a reference
                    // field reaches: a reference's target is a complete type.
                    // A raw pointer's need not be (`resolve_type`).
                    field_types.push(self.resolve_value_type(ty));
                }
                // Invalid when a field is, as an array is when its element
                // is, so no later phase lays out a field it cannot.
                if field_types.contains(&TypeId::INVALID) {
                    return TypeId::INVALID;
                }
                let (offsets, size, align) = struct_layout(&self.types, &field_types, self.target);
                let named = self.types.intern(Type::Named(definition));
                let mut ty = self.fits_target(named, size, written.name.span);
                if self.types.holders_in_fields(&field_types) > MOST_HOLDERS {
                    let text = self.type_text(named);
                    let message =
                        format!("`{text}` holds more than {MOST_HOLDERS} references and slices");
                    ty = self.error(diagnostics::INVALID_TYPE, message, written.name.span);
                }
                if ty != TypeId::INVALID {
                    self.types
                        .define_struct(named, field_types.into(), (offsets, size, align));
                }
                return ty;
            }
            ast::Type::Interface(_) => return self.check_interface(definition),
            ast::Type::Enum(_) => "enum declarations",
            _ => unreachable!("the parser gives a type declaration only these types"),
        };
        self.not_supported(written.name.span, what)
    }

    /// Reports each attribute of the declaration `definition` but an
    /// `extern` on a function, the one attribute there is; `check_function`
    /// checks the `extern`'s shape.
    pub(crate) fn check_attributes(&mut self, definition: DefId) {
        let tree = &self.module().ast;
        let declaration = &tree.decls[tree.declaration(definition.index)];
        let function = matches!(declaration, ast::Decl::Function(_));
        for attribute in tree.children.lists.items(declaration.metadata().attributes) {
            let name = tree.children.attributes[attribute].name.text(&tree.file);
            let message = match name {
                EXTERN_ATTRIBUTE if function => continue,
                EXTERN_ATTRIBUTE => {
                    "`extern` names a C function, so only a function has it".to_owned()
                }
                _ => format!("unknown attribute `{name}`"),
            };
            let span = tree.children.attributes.span(attribute);
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
    }

    /// What the function of `signature`, in `tree`, is checked with outside
    /// any copy: an invalid type for each `const` parameter. Only a type one
    /// is read as one (`resolve_global`), and an invalid type reports
    /// nothing.
    fn own_arguments(&mut self, tree: &ast::Tree, signature: ast::Signature) -> ConstArguments {
        let count = (tree.children.lists.items(signature.parameters))
            .filter(|&parameter| tree.children.params[parameter].constant)
            .count();
        let invalid = ConstArgument::Type(TypeId::INVALID);
        self.types.const_arguments(&vec![invalid; count])
    }

    /// The type of the `const` value parameter written `ty` of the function
    /// `definition`, for its call at `used_at`. The function's own signature
    /// read it with no error and no type parameter (`signature_type`), so
    /// reading it again reports nothing, and records nothing: its facts are
    /// the signature's.
    pub(crate) fn const_value_type(
        &mut self,
        definition: DefId,
        ty: Id<ast::Type>,
        used_at: Span,
    ) -> TypeId {
        let tree = &self.modules[definition.module.index()].ast;
        let signature = function_declaration(self.modules, definition).signature;
        let checking = Checking {
            definition,
            arguments: self.own_arguments(tree, signature),
            used_at,
            records_facts: false,
        };
        self.checking_in(checking, |checker| checker.resolve_value_type(ty))
    }

    /// The type of the module's function or method `definition`.
    pub(crate) fn check_function(&mut self, definition: DefId) -> TypeId {
        let tree = &self.module().ast;
        let &ast::Function {
            owner,
            name,
            signature,
            body,
            ..
        } = function_declaration(self.modules, definition);
        let symbol = model::program::extern_symbol(self.modules, definition);
        if let Err(span) = symbol {
            let message = "write `#[extern(\"name\")]` once, with the C function's name";
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
        // A malformed `extern` is still read as one, so its signature gets
        // the C rules.
        let external = symbol != Ok(None);
        let first_const = const_parameter(tree, signature);
        if external && let Some(span) = first_const {
            let message =
                "an `extern` function is one C function, so it takes no `const` parameter";
            return self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
        let module = definition.module.index();
        let reported = self.module_checks[module].diagnostics.len();
        let checking = self.checking.last().expect("the function is being checked");
        if first_const.is_some() && checking.arguments == ConstArguments::NONE {
            let own = self.own_arguments(tree, signature);
            self.checking
                .last_mut()
                .expect("the function is being checked")
                .arguments = own;
        }
        let ty = self.signature_type(signature, owner.is_some(), external);
        // A copy's signature differs only by what it is given: an error in
        // the function's own is in each copy's.
        let failed = || {
            let found = &self.module_checks[module].diagnostics[reported..];
            found.iter().any(Diagnostic::is_error)
        };
        if ty == TypeId::INVALID || first_const.is_some() && failed() {
            return TypeId::INVALID;
        }
        if external == body.is_some() {
            let message = if external {
                "an `extern` function is defined in C, so it has no body"
            } else {
                "a function needs a body, or `#[extern(\"name\")]` when C defines it"
            };
            return self.error(diagnostics::INVALID_DECLARATION, message, name.span);
        }
        // Reported above; invalid, so no call of it is lowered.
        if symbol.is_err() {
            return TypeId::INVALID;
        }
        ty
    }

    /// The function type `signature` of the module's tree declares, of a
    /// type's function or an interface's method when `has_owner` (only these
    /// take `self`), of a C function when `external`. Invalid when the
    /// signature is reported; a parameter or result whose type is reported
    /// is invalid inside it. A `const T: type` parameter is not among its
    /// parameters: each call gives it a type, which makes its own copy
    /// (`copy_type`).
    fn signature_type(
        &mut self,
        signature: ast::Signature,
        has_owner: bool,
        external: bool,
    ) -> TypeId {
        let tree = &self.module().ast;
        if let Some(parameter) = tree.children.lists.items(signature.type_parameters).next() {
            return self.not_supported(tree.children.names.span(parameter), "generic functions");
        }
        let mut parameters = Vec::with_capacity(signature.parameters.len());
        let mut variadic = false;
        for (position, parameter) in tree.children.lists.items(signature.parameters).enumerate() {
            let parameter = tree.children.params[parameter];
            if let Some(default) = parameter.default {
                return self.not_supported(tree.exprs.span(default), "default parameters");
            }
            let span = tree.types.span(parameter.ty);
            if parameter.constant {
                if parameter.mutable || parameter.variadic {
                    let message = "a `const` parameter is one value known when compiling, so it takes no `mut` or `...`";
                    return self.error(diagnostics::INVALID_DECLARATION, message, span);
                }
                if let ast::Type::Type = tree.types[parameter.ty] {
                    continue;
                }
                if self.resolve_value_type(parameter.ty) != TypeId::INVALID {
                    continue;
                }
                // A value's type is read again at each call (`const_value_type`),
                // which cannot know a type parameter yet: only the copy does.
                // Any other error in it was reported.
                let tree = &self.module().ast;
                let type_parameters: Vec<&str> = (tree.children.lists.items(signature.parameters))
                    .map(|parameter| tree.children.params[parameter])
                    .filter(|parameter| matches!(tree.types[parameter.ty], ast::Type::Type))
                    .filter_map(|parameter| Some(parameter.name?.text(&tree.file)))
                    .collect();
                if names_type_parameter(tree, parameter.ty, &type_parameters) {
                    return self
                        .not_supported(span, "`const` values whose type names a type parameter");
                }
                return TypeId::INVALID;
            }
            if parameter.variadic && external {
                let message = "an `extern` function cannot have a `...` parameter";
                return self.error(diagnostics::INVALID_DECLARATION, message, span);
            }
            if parameter.variadic && position + 1 != signature.parameters.len() {
                let message = "a `...` parameter must be the last one";
                return self.error(diagnostics::INVALID_DECLARATION, message, span);
            }
            if is_self(tree, parameter) {
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
            variadic |= parameter.variadic;
            parameters.push(self.resolve_signature_type(parameter.ty, external));
        }
        let result = signature.result.map_or(TypeId::VOID, |result| {
            self.resolve_signature_type(result, external)
        });
        let Some(origins) = self.result_origins(&signature, &parameters, result) else {
            return TypeId::INVALID;
        };
        let parameters = parameters.into();
        self.types.intern(Type::Function {
            parameters,
            result,
            origins,
            variadic,
        })
    }

    /// The type the module's interface declaration `definition` declares:
    /// each method's function type, whose `self` is a reference to the
    /// interface (`Type::InterfaceReference`), or the interface taken whole.
    /// Invalid when a method has an error or is declared twice, as a struct
    /// is when a field has an error.
    fn check_interface(&mut self, definition: DefId) -> TypeId {
        let modules = self.modules;
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
                let diagnostic = report_redeclared(
                    &tree.file,
                    text,
                    "interface",
                    method.name.span,
                    first.name.span,
                );
                self.report(diagnostic);
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
                self.error(diagnostics::INVALID_DECLARATION, message, method.name.span);
                methods.push(TypeId::INVALID);
                continue;
            }
            if let Some(span) = const_parameter(tree, method.signature) {
                let message = "a call through an interface reference cannot pick a copy, so an interface method takes no `const` parameter";
                self.error(diagnostics::INVALID_DECLARATION, message, span);
                methods.push(TypeId::INVALID);
                continue;
            }
            methods.push(self.signature_type(method.signature, true, false));
        }
        let invalid = |ty: TypeId| match &self.types[ty] {
            Type::Function {
                parameters, result, ..
            } => parameters.contains(&TypeId::INVALID) || *result == TypeId::INVALID,
            _ => true,
        };
        if methods.iter().any(|&method| invalid(method)) {
            return TypeId::INVALID;
        }
        let named = self.types.intern(Type::Named(definition));
        self.types.define_interface(named, methods.into());
        named
    }

    /// The functions of the struct `structure` that are the methods of the
    /// interface `interface`, for a reference to the struct used as a
    /// reference to the interface at `span`: for each method, the struct's
    /// function of its name and type, `self` being the struct; looked up
    /// once for the pair. `None` after reporting the first method the struct
    /// lacks or has with another type, or when the interface's error was
    /// reported; such a pair is reported at each conversion.
    pub(crate) fn implementation(
        &mut self,
        structure: TypeId,
        interface: TypeId,
        span: Span,
    ) -> Option<ImplementationId> {
        if let Some(&found) = self.implementations.get(&(structure, interface)) {
            return Some(found);
        }
        let modules = self.modules;
        let Type::Struct {
            definition: owner, ..
        } = self.types[structure]
        else {
            unreachable!("only a struct's reference converts to an interface reference")
        };
        let (Type::Named(definition) | Type::Interface { definition, .. }) = self.types[interface]
        else {
            unreachable!("an interface reference refers to an interface")
        };
        if self.declared_type(definition, span) == TypeId::INVALID {
            return None;
        }
        let Type::Interface { methods, .. } = &self.types[interface] else {
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
                Some(Symbol::Function(function)) => Some(function),
                _ => None,
            };
            let expected = self.method_type(signature, structure);
            let ty = function.map(|function| (function, self.function_type(function, span)));
            let generic = |function: DefId| {
                let tree = &modules[function.module.index()].ast;
                const_parameter(tree, function_declaration(modules, function).signature).is_some()
            };
            let message = match ty {
                Some((function, _)) if generic(function) => {
                    format!(
                        "its `{name}` takes a `const` parameter, so it is not one function an interface reference can call"
                    )
                }
                Some((_, TypeId::INVALID)) => return None, // reported
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
        self.implementations.insert((structure, interface), id);
        Some(id)
    }

    /// The type a function of the struct `structure` has when it is the
    /// interface method of the type `signature`: the same, with `self` the
    /// struct.
    fn method_type(&mut self, signature: TypeId, structure: TypeId) -> TypeId {
        let Type::Function {
            mut parameters,
            result,
            origins,
            variadic,
        } = self.types[signature].clone()
        else {
            unreachable!("a method's type is a function type")
        };
        parameters[0] = match self.types[parameters[0]] {
            Type::InterfaceReference { mutable, .. } => self.types.intern(Type::Reference {
                mutable,
                target: structure,
            }),
            _ => structure, // `self` taken whole
        };
        self.types.intern(Type::Function {
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
        signature: &ast::Signature,
        parameters: &[TypeId],
        result: TypeId,
    ) -> Option<Box<[u32]>> {
        let tree = &self.module().ast;
        let file = &tree.file;
        let mut names = tree.children.lists.items(signature.origins);
        // An invalid type was reported, or is a `const T: type` parameter's
        // in the function's own signature, which no copy gives one.
        if result == TypeId::INVALID {
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
                Some(position) if parameters[position] == TypeId::INVALID => None,
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
                } else {
                    "a struct holding references needs a parameter that holds one".to_owned()
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

    /// The type and value of the module's constant `definition`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// variables in scope.
    pub(crate) fn check_constant(&mut self, definition: DefId) -> Option<Constant> {
        let declaration = self.module().ast.declaration(definition.index);
        let mut body = Body::default();
        ThirBuilder::new(self, &mut body, TypeId::VOID).check_const_statement(declaration)
    }

    /// The type of a value whose type is written `ty`: a named type is the
    /// type its declaration declares, so a value's type is complete (an
    /// alias names a complete type already).
    pub(crate) fn resolve_value_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let resolved = self.resolve_type(ty);
        self.value_type(resolved, ty)
    }

    /// `resolve_value_type` of `resolved`, the type written `ty`. An
    /// interface is used through a reference; only an interface method's
    /// `self` taken whole has its type.
    fn value_type(&mut self, resolved: TypeId, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module().ast;
        if let ast::Type::Owner = tree.types[ty]
            && self.is_interface(resolved)
        {
            return resolved;
        }
        let span = tree.types.span(ty);
        self.value_type_at(resolved, span)
    }

    /// `resolved`, written at `span`, as a value's type: an interface is
    /// used only through a reference, and a named type is checked.
    fn value_type_at(&mut self, resolved: TypeId, span: Span) -> TypeId {
        if self.is_interface(resolved) {
            return self.not_supported(span, "owned interface values");
        }
        match self.types[resolved] {
            Type::Named(definition) => self.declared_type(definition, span),
            _ => resolved,
        }
    }

    /// Whether `ty` is an interface, checked or only named.
    pub(crate) fn is_interface(&self, ty: TypeId) -> bool {
        match self.types[ty] {
            Type::Interface { .. } => true,
            Type::Named(definition) => {
                model::program::interface_methods(self.modules, definition).is_some()
            }
            _ => false,
        }
    }

    /// The type of a parameter or result written `ty`, of a C function when
    /// `external`.
    fn resolve_signature_type(&mut self, ty: Id<ast::Type>, external: bool) -> TypeId {
        let resolved = self.resolve_value_type(ty);
        if !external {
            return resolved;
        }
        let span = self.module().ast.types.span(ty);
        match self.types[resolved] {
            // shortcut: structs, arrays, ranges, slices and `&str` pass as
            // LLVM values, which the C ABI does not match; allow structs and
            // arrays when its aggregate rules are written (a range, slice or
            // `&str` has no C type).
            Type::Struct { .. }
            | Type::Array { .. }
            | Type::Range { .. }
            | Type::Slice { .. }
            | Type::Str => {
                let what = "struct, array, range, slice and `&str` parameters and results of `extern` functions";
                self.not_supported(span, what)
            }
            Type::InterfaceReference { .. } => {
                self.not_supported(span, "interface references in `extern` functions")
            }
            // C could keep what the struct holds, which no loan would follow.
            Type::Reference { target, .. } if self.types.holds_loans(target).is_some() => {
                let what = "references to structs holding references in `extern` functions";
                self.not_supported(span, what)
            }
            Type::Integer(integer) if integer.bits > self.target.widest_c_integer() => {
                let (target, widest) = (self.target.name, self.target.widest_c_integer());
                let message = format!(
                    "C has no `{integer}` on {target}, whose integers have at most {widest} bits"
                );
                self.error(diagnostics::INVALID_TYPE, message, span)
            }
            _ => resolved,
        }
    }

    /// The real type of `held`, for a type written at `span` that holds it,
    /// so `&Meters` is `&f64` under an alias; `None` when it is invalid, or
    /// after reporting that it borrows. `holder` starts the message:
    /// "references to".
    fn held_type(&mut self, held: TypeId, holder: &str, span: Span) -> Option<TypeId> {
        let what = match self.types[held] {
            Type::Invalid => return None,
            // shortcut: the loan check follows no reference held in an array
            // or a slice, behind a raw pointer or another reference; allow
            // each when lifetimes are checked (ownership step 4).
            Type::Reference { .. } => "references",
            Type::Slice { .. } => "slices",
            Type::InterfaceReference { .. } => "interface references",
            _ if self.is_interface(held) => "interfaces",
            _ => return Some(self.types.real(held)),
        };
        self.not_supported(span, &format!("{holder} {what}"));
        None
    }

    /// `held_type` for an array or a slice, which hold no struct that holds
    /// a reference either, and for what is reached through a raw pointer: a
    /// holder is one reference, and no index or pointer says which.
    pub(crate) fn loan_free_held_type(
        &mut self,
        held: TypeId,
        holder: &str,
        span: Span,
    ) -> Option<TypeId> {
        let held = self.held_type(held, holder, span)?;
        if self.types.holds_loans(held).is_some() {
            self.not_supported(span, &format!("{holder} structs holding references"));
            return None;
        }
        Some(held)
    }

    /// `&target`, or `&mut target` when `mutable`, written or borrowed at
    /// `span` (`held_type`); an interface reference to an interface.
    pub(crate) fn reference_type(&mut self, mutable: bool, target: TypeId, span: Span) -> TypeId {
        if self.is_interface(target) {
            return self.types.intern(Type::InterfaceReference {
                mutable,
                interface: target,
            });
        }
        match self.held_type(target, "references to", span) {
            Some(target) => self.types.intern(Type::Reference { mutable, target }),
            None => TypeId::INVALID,
        }
    }

    /// `[length]element`, written at `span` (`held_type`).
    pub(crate) fn array_type(&mut self, element: TypeId, length: u64, span: Span) -> TypeId {
        match self.loan_free_held_type(element, "arrays of", span) {
            Some(element) => {
                let ty = self.types.array(element, length);
                let (size, _) = layout(&self.types, ty, self.target);
                self.fits_target(ty, size, span)
            }
            None => TypeId::INVALID,
        }
    }

    /// `ty`, made at `span`, or invalid after an error there when its `size`
    /// is more bytes than the target allows for a value, which no copy of it
    /// could count.
    fn fits_target(&mut self, ty: TypeId, size: u64, span: Span) -> TypeId {
        let largest = self.target.largest_object();
        if size <= largest {
            return ty;
        }
        let (text, target) = (self.type_text(ty), self.target.name);
        let message = format!(
            "`{text}` is too big for {target}, where a value takes at most {largest} bytes"
        );
        self.error(diagnostics::INVALID_TYPE, message, span)
    }

    /// `range[element]`, written or made at `span`: invalid when `element`
    /// is, reported when it is not an integer.
    pub(crate) fn range_type(&mut self, element: TypeId, span: Span) -> TypeId {
        if element == TypeId::INVALID {
            return element;
        }
        if let Some(range) = self.types.range(element) {
            return range;
        }
        let found = self.type_text(element);
        let message = format!("a range's values must be integers, found `{found}`");
        self.error(diagnostics::INVALID_TYPE, message, span)
    }

    /// Whether an array may have `length` elements, as its `len` is a
    /// `usize` (`check_field`); reported at `span` when not.
    pub(crate) fn length_fits(&mut self, length: &[u64], span: Span) -> bool {
        let usize = Integer {
            signed: false,
            bits: self.target.pointer_bits,
        };
        let fits = usize.fits(length, false);
        if !fits {
            let bits = self.target.pointer_bits;
            let message = format!("an array length must fit `usize`, of {bits} bits");
            self.error(diagnostics::INVALID_TYPE, message, span);
        }
        fits
    }

    /// The type `symbol`, written at `span`, stands for, however it was
    /// reached: every kind of type declaration is `Symbol::Type`, in this
    /// module or another. An error when it is not a type.
    pub(crate) fn type_named_by(&mut self, symbol: Symbol, span: Span) -> TypeId {
        let what = match symbol {
            Symbol::Builtin(ty) | Symbol::TypeArgument(ty) => return ty,
            Symbol::Type(definition) => return self.named_type(definition, span),
            Symbol::Function(_) | Symbol::Constant(_) | Symbol::Local(_) => "a value",
            Symbol::Module(_) => "a module",
        };
        let name = self.module().ast.file.slice(span);
        let message = format!("`{name}` is {what}, not a type");
        self.error(diagnostics::NOT_A_TYPE, message, span)
    }

    /// The type the name written at `span` stands for, its name recorded: a
    /// built-in type or a top-level declaration (`resolve_type`).
    fn named_type_at(&mut self, span: Span) -> TypeId {
        let tree = &self.module().ast;
        let name = tree.file.slice(span);
        let symbol = self.resolve_global(name);
        if let Some(symbol) = symbol {
            self.record(span, FactKind::Name(symbol));
        }
        match symbol {
            // Text alone has no fixed size: only a reference to it is a
            // value (`text_view`). A `const` type parameter given `&str`
            // stands for the reference, which is the same type.
            Some(Symbol::Builtin(text))
                if matches!(self.types[text], Type::Str | Type::CString) =>
            {
                let (what, view) = match self.types[text] {
                    Type::Str => ("text", "&str"),
                    _ => ("C text", "&cstr"),
                };
                let message = format!("`{name}` is {what} of any length, so it has no fixed size");
                let label = format!("use `{view}` for {what}");
                let diagnostic = Diagnostic::new(diagnostics::INVALID_TYPE, message)
                    .label(&tree.file, span, label);
                self.report(diagnostic);
                TypeId::INVALID
            }
            Some(symbol) => self.type_named_by(symbol, span),
            None if name == semantic_types::RANGE => {
                let message = format!("`{name}` needs the type of its values: `{name}[T]`");
                self.error(diagnostics::INVALID_TYPE, message, span)
            }
            None if let Some(what) = unsupported_type(name) => self.not_supported(span, what),
            None => {
                let symbols = self.visible_symbols();
                let types = symbols.filter(|&(_, symbol)| matches!(symbol, Symbol::Type(_)));
                let builtin = semantic_types::builtin_names();
                let meant = scope::nearest(name, builtin, types.map(|(declared, _)| declared));
                let message = format!("unknown type `{name}`");
                self.report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
                TypeId::INVALID
            }
        }
    }

    /// `&text` or `&mut text`, written at `span`, when `target` names `str`
    /// or `cstr`: a reference to text is its own type (`Type::Str`,
    /// `Type::CString`), as it holds a count or reads up to a zero byte, and
    /// is read only. `None` when `target` names other than text, as a `const`
    /// type parameter does, even one named `str`: given `&str`, `&T` is a
    /// reference to the view.
    fn text_view(&mut self, mutable: bool, target: Span, span: Span) -> Option<TypeId> {
        let tree = &self.module().ast;
        let name = tree.file.slice(target);
        let Some(Symbol::Builtin(text)) = self.resolve_global(name) else {
            return None;
        };
        if !matches!(self.types[text], Type::Str | Type::CString) {
            return None;
        }
        self.record(target, FactKind::Name(Symbol::Builtin(text)));
        if !mutable {
            return Some(text);
        }
        let message = format!("`&mut {name}` is not a type: text views are read only");
        let diagnostic = Diagnostic::new(diagnostics::INVALID_TYPE, message).label(
            &tree.file,
            span,
            format!("use `&{name}`"),
        );
        self.report(diagnostic);
        Some(TypeId::INVALID)
    }

    /// `&target` or `&mut target`, written at `span`, of `target` resolved
    /// and written at `target_span`: an interface is referred to as it is,
    /// any other type as a value's type.
    fn reference_to(
        &mut self,
        mutable: bool,
        target: TypeId,
        target_span: Span,
        span: Span,
    ) -> TypeId {
        let target = if self.is_interface(target) {
            target
        } else {
            self.value_type_at(target, target_span)
        };
        self.reference_type(mutable, target, span)
    }

    /// The value type the argument `argument` is read as where a parameter
    /// takes a type: a type the parser read (`ast::Expr::Type`), or a value
    /// spelled like a type, a name, a path, `&x`, `&mut x` or `range[x]`.
    /// Anything else is reported as not a type.
    pub(crate) fn resolve_type_argument(&mut self, argument: Id<ast::Expr>) -> TypeId {
        let resolved = self.expression_type(argument);
        let span = self.module().ast.exprs.span(argument);
        self.value_type_at(resolved, span)
    }

    /// `resolve_type_argument` before it is made a value's type, as
    /// `resolve_type` is.
    fn expression_type(&mut self, expression: Id<ast::Expr>) -> TypeId {
        source::grow_stack(|| {
            let tree = &self.module().ast;
            let span = tree.exprs.span(expression);
            match tree.exprs[expression] {
                ast::Expr::Type(ty) | ast::Expr::Path(ty) => self.resolve_type(ty),
                ast::Expr::Name => self.named_type_at(span),
                ast::Expr::Parentheses(inner) => self.expression_type(inner),
                ast::Expr::Reference { mutable, value } => {
                    let target = tree.exprs.span(value);
                    if let ast::Expr::Name = tree.exprs[value]
                        && let Some(view) = self.text_view(mutable, target, span)
                    {
                        return view;
                    }
                    let resolved = self.expression_type(value);
                    self.reference_to(mutable, resolved, target, span)
                }
                ast::Expr::Index { value, index }
                    if let ast::Expr::Name = tree.exprs[value]
                        && tree.file.slice(tree.exprs.span(value)) == semantic_types::RANGE =>
                {
                    let element = self.resolve_type_argument(index);
                    let range = self.range_type(element, span);
                    let name = tree.exprs.span(value);
                    self.record(name, FactKind::Name(Symbol::Builtin(range)));
                    range
                }
                ast::Expr::Invalid => TypeId::INVALID, // the parser reported it
                _ => {
                    let text = tree.file.slice(span);
                    let message = format!("`{text}` is a value, not a type");
                    self.error(diagnostics::NOT_A_TYPE, message, span)
                }
            }
        })
    }

    /// The type a type expression of the module's tree stands for, its names
    /// recorded (`record`). A named type stays a name (`Type::Named`) until
    /// its declaration is checked; a value's type is `resolve_value_type`. A
    /// local never names a type
    /// (`ThirBuilder::declare`), so only top-level names are looked up.
    fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        // Types written in types, `& & ... i32`, are a deep recursion.
        source::grow_stack(|| {
            let tree = &self.module().ast;
            let span = tree.types.span(ty);
            let what = match tree.types[ty] {
                // `self`'s type: the owner of the function being checked, which
                // has one (`check_function`). An owner that is not a struct
                // was reported by `scope::collect`.
                ast::Type::Owner => {
                    let checking = self
                        .checking
                        .last()
                        .expect("a declaration is being checked");
                    let declaration =
                        model::program::declaration(self.modules, checking.definition);
                    let owner = match declaration {
                        ast::Decl::Function(ast::Function {
                            owner: Some(owner), ..
                        }) => owner,
                        // An interface's method: `self` is the interface.
                        ast::Decl::Type(_) => {
                            return self.types.intern(Type::Named(checking.definition));
                        }
                        _ => unreachable!("only a type's function and an interface name `self`"),
                    };
                    let owner = self
                        .scope()
                        .struct_named(self.modules, owner.text(&tree.file));
                    return owner.map_or(TypeId::INVALID, |owner| self.named_type(owner, span));
                }
                ast::Type::Name => return self.named_type_at(span),
                ast::Type::Invalid => return TypeId::INVALID, // the parser reported it
                ast::Type::Type => {
                    let message = "`type` is the type of a `const` parameter only: `const T: type`";
                    return self.error(diagnostics::INVALID_TYPE, message, span);
                }
                ast::Type::Path(_) => {
                    return match self.resolve_path(ty) {
                        Some(symbol) => self.type_named_by(symbol, span),
                        None => TypeId::INVALID, // reported
                    };
                }
                ast::Type::Generic { .. } => "generic types",
                ast::Type::Bracketed { name, inner } => {
                    let written = tree.file.slice(name.span);
                    if written != semantic_types::RANGE {
                        let message = format!("`{written}` takes no type in brackets");
                        return self.error(diagnostics::INVALID_TYPE, message, span);
                    }
                    let element = self.resolve_value_type(inner);
                    let range = self.range_type(element, span);
                    self.record(name.span, FactKind::Name(Symbol::Builtin(range)));
                    return range;
                }
                ast::Type::Reference { mutable, target } => {
                    if let ast::Type::Name = tree.types[target]
                        && let Some(view) = self.text_view(mutable, tree.types.span(target), span)
                    {
                        return view;
                    }
                    let resolved = self.resolve_type(target);
                    return self.reference_to(mutable, resolved, tree.types.span(target), span);
                }
                ast::Type::Optional(_) => "optional types",
                ast::Type::RawPointer => return TypeId::RAWPTR,
                // A pointer only names its target, so a struct can point to itself.
                // shortcut: `*[2]Node` or `*Alias` of `Node` in `Node` is a cycle, as
                // an array's element and an alias's target are complete, and so is
                // any cycle through such a pointer; let them stay names too if a
                // program needs it.
                ast::Type::Pointer(target) => {
                    let pointee = self.resolve_type(target);
                    return match self.held_type(pointee, "raw pointers to", span) {
                        Some(target) => self.types.intern(Type::RawPointer {
                            target: Some(target),
                        }),
                        None => TypeId::INVALID,
                    };
                }
                ast::Type::FixedArray {
                    length: ast::Length::Number(number),
                    element,
                } => {
                    let literal = literal::numeric::split(tree.file.slice(number));
                    let value = literal.integer_value();
                    if !literal.suffix.is_empty() || literal.is_float() {
                        let message = "an array length is a whole number without a suffix";
                        return self.error(diagnostics::INVALID_TYPE, message, number);
                    }
                    if !self.length_fits(&value, number) {
                        return TypeId::INVALID;
                    }
                    let element = self.resolve_value_type(element);
                    return self.array_type(element, value[0], span);
                }
                ast::Type::FixedArray {
                    length: ast::Length::Inferred(_),
                    ..
                } => unreachable!("the parser reads `[_]` only in an array literal, checked there"),
                ast::Type::Slice { mutable, element } => {
                    let element = self.resolve_value_type(element);
                    return match self.loan_free_held_type(element, "slices of", span) {
                        Some(element) => self.types.intern(Type::Slice { mutable, element }),
                        None => TypeId::INVALID,
                    };
                }
                ast::Type::Function(_) => "function types",
                ast::Type::Struct(_) => "struct types",
                ast::Type::Enum(_) => "enum types",
                ast::Type::Interface(_) => "interface types",
            };
            self.not_supported(span, what)
        })
    }
}

/// Where the first `const` parameter of `signature`, in `tree`, is written,
/// if it has one.
pub fn const_parameter(tree: &ast::Tree, signature: ast::Signature) -> Option<Span> {
    let mut parameters = tree.children.lists.items(signature.parameters);
    let parameter = parameters.find(|&parameter| tree.children.params[parameter].constant)?;
    Some(tree.children.params.span(parameter))
}

/// Whether the type written `ty` in `tree` names one of `parameters`, the
/// names of a function's `const T: type` parameters, which only its copies
/// know. A path names another module's type, and a type the checker
/// refuses (`resolve_type`), such as `?T`, is reported: neither names one.
/// Its own function, as it recurses.
fn names_type_parameter(tree: &ast::Tree, ty: Id<ast::Type>, parameters: &[&str]) -> bool {
    // Types written in types, `& & ... T`, are a deep recursion.
    source::grow_stack(|| match tree.types[ty] {
        ast::Type::Name => parameters.contains(&tree.file.slice(tree.types.span(ty))),
        ast::Type::Bracketed { inner: written, .. }
        | ast::Type::Reference {
            target: written, ..
        }
        | ast::Type::Pointer(written)
        | ast::Type::FixedArray {
            element: written, ..
        }
        | ast::Type::Slice {
            element: written, ..
        } => names_type_parameter(tree, written, parameters),
        ast::Type::Path(_)
        | ast::Type::Generic { .. }
        | ast::Type::Optional(_)
        | ast::Type::RawPointer
        | ast::Type::Function(_)
        | ast::Type::Struct(_)
        | ast::Type::Enum(_)
        | ast::Type::Interface(_)
        | ast::Type::Owner
        | ast::Type::Type
        | ast::Type::Invalid => false,
    })
}

/// Whether `parameter` of a function in `tree` is `self`, `&self` or
/// `&mut self`: of the type the function belongs to.
pub(crate) fn is_self(tree: &ast::Tree, parameter: ast::Param) -> bool {
    let taken = match tree.types[parameter.ty] {
        ast::Type::Reference { target, .. } => target,
        _ => parameter.ty,
    };
    matches!(tree.types[taken], ast::Type::Owner)
}

#[cfg(test)]
mod tests {
    use model::DefId;
    use model::ast;
    use model::semantic_types::Type;
    use model::semantic_types::TypeId;

    use crate::testing::Reported;
    use crate::testing::reported;
    use crate::testing::with_checker;

    /// The type of every function and type declaration of `text`, as type
    /// text, and the diagnostics.
    fn declared_types(text: &str) -> (Vec<String>, Vec<Reported>) {
        let (found, types, reported) = with_checker(text, |checker, main| {
            let tree = &checker.modules[main.index()].ast;
            let found: Vec<TypeId> = tree
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
        let text_of = |ty: TypeId| match &types[ty] {
            Type::Function {
                parameters, result, ..
            } => format!("fn{parameters:?} -> {result:?}"),
            other => format!("{other:?}"),
        };
        (found.into_iter().map(text_of).collect(), reported)
    }

    #[test]
    fn functions_and_constants() {
        let (found, reported) = declared_types(
            "fn add(a: i32, b: i32) -> i32 { return a; }\nfn main() {}\nfn take(s: &cstr) {}\n",
        );
        assert!(reported.is_empty(), "{reported:?}");
        let i32 = format!("{:?}", TypeId::I32);
        let void = format!("{:?}", TypeId::VOID);
        assert_eq!(found[0], format!("fn[{i32}, {i32}] -> {i32}"));
        assert_eq!(found[1], format!("fn[] -> {void}"));
        assert_eq!(found[2], format!("fn[{:?}] -> {void}", TypeId::CSTR));
    }

    #[test]
    fn errors_are_located() {
        let (found, found_reported) = declared_types(
            "fn a(x: Missing) {}\nfn b(m: utils) {}\nfn c(r: &i32) -> &i32 in q {}\nfn d<T>(x: T) {}\nfn e(x: i32 = 1) {}\nstruct P { x: i32 }\n#[extern(\"f\")]\nfn f(p: P);\nfn ok(p: P) -> P { return p; }\nfn g(x: a) {}\nfn h(s: string) {}\nfn i(&self) {}\nfn P::j(a: i32, mut self) {}\n",
        );
        let expected = reported(&[
            ("T0002", "unknown type `Missing`", "Missing"),
            ("T0021", "`utils` is a module, not a type", "utils"),
            ("T0002", "`q` is not a parameter", "q"),
            ("T0043", "generic functions are not supported yet", "T"),
            ("T0043", "default parameters are not supported yet", "1"),
            (
                "T0043",
                "struct, array, range, slice and `&str` parameters and results of `extern` functions are not supported yet",
                "P",
            ),
            ("T0021", "`a` is a value, not a type", "a"),
            ("T0043", "string types are not supported yet", "string"),
            (
                "P0005",
                "only a type's function takes `self`: `fn Point::name(&self)`",
                "&self",
            ),
            ("P0005", "`self` must be the first parameter", "self"),
        ]);
        assert_eq!(found_reported, expected);
        let invalid = format!("{:?}", TypeId::INVALID);
        assert_eq!(
            found[0],
            format!("fn[{invalid}] -> {:?}", TypeId::VOID),
            "an error stays inside the signature"
        );
        assert_eq!(found[3], "Invalid");
    }

    /// An `extern` function is C's, so it has no body here, and any other
    /// function has one; their calls are not lowered.
    #[test]
    fn only_an_extern_function_has_no_body() {
        let (found, found_reported) =
            declared_types("#[extern(\"f\")]\nfn f(a: i8) -> i8 { return a; }\nfn g();\n");
        assert_eq!(
            found_reported,
            reported(&[
                (
                    "P0005",
                    "an `extern` function is defined in C, so it has no body",
                    "f"
                ),
                (
                    "P0005",
                    "a function needs a body, or `#[extern(\"name\")]` when C defines it",
                    "g"
                ),
            ])
        );
        assert_eq!(found, ["Invalid", "Invalid"]);
    }

    /// `extern` on a function is the one attribute there is.
    #[test]
    fn other_attributes_are_reported() {
        let (_, found_reported) =
            declared_types("#[inline]\nfn f();\n#[extern(\"s\")]\nstruct S {}\n");
        assert_eq!(
            found_reported,
            reported(&[
                ("P0005", "unknown attribute `inline`", "#[inline]"),
                (
                    "P0005",
                    "a function needs a body, or `#[extern(\"name\")]` when C defines it",
                    "f"
                ),
                (
                    "P0005",
                    "`extern` names a C function, so only a function has it",
                    "#[extern(\"s\")]"
                ),
            ])
        );
    }

    /// An `extern` of another shape is reported, and its function still gets
    /// the C rules.
    #[test]
    fn a_malformed_extern_is_reported() {
        let (_, found_reported) = declared_types(
            "struct P { x: i32 }\n#[extern(1)]\nfn a();\n#[extern(\"b\", 2)]\nfn b();\n#[extern(\"c\")]\n#[extern(\"d\")]\nfn c(p: P);\n",
        );
        let shape = "write `#[extern(\"name\")]` once, with the C function's name";
        let expected = reported(&[
            ("P0005", shape, "#[extern(1)]"),
            ("P0005", shape, "#[extern(\"b\", 2)]"),
            ("P0005", shape, "#[extern(\"d\")]"),
            (
                "T0043",
                "struct, array, range, slice and `&str` parameters and results of `extern` functions are not supported yet",
                "P",
            ),
        ]);
        assert_eq!(found_reported, expected);
    }
}
