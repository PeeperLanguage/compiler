//! Module-scope declarations, without their bodies, one check per category:
//! a type declaration's type, a function's type and a constant's type and
//! value.

use arena::Id;
use model::DefId;
use model::ast;
use model::constant::Constant;
use model::layout::layout;
use model::semantic_types;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Body;
use model::thir::FactKind;
use model::thir::Symbol;
use source::Span;

use crate::Checker;
use crate::builder::ThirBuilder;
use crate::builtin::unsupported_type;
use crate::scope;
use crate::scope::report_redeclared;

impl Checker<'_> {
    /// The type the module's type declaration or alias `definition` declares:
    /// a struct's type (`Type::Struct`), or the type an alias names. Enums,
    /// interfaces and type parameters are not supported yet.
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
                    // itself, however deep, is a cycle. A reference or slice
                    // is rejected before its type is, which may be this struct.
                    let borrows = matches!(
                        tree.types[ty],
                        ast::Type::Reference { .. } | ast::Type::Slice { .. }
                    );
                    let mut field_type = TypeId::INVALID;
                    if !borrows {
                        field_type = self.resolve_value_type(ty);
                    }
                    if borrows || self.types[field_type].borrows().is_some() {
                        let span = tree.types.span(ty);
                        let what = "references and slices in struct fields";
                        field_type = self.not_supported(span, what);
                    }
                    field_types.push(field_type);
                }
                // Invalid when a field is, as an array is when its element
                // is, so no later phase lays out a field it cannot.
                if field_types.contains(&TypeId::INVALID) {
                    return TypeId::INVALID;
                }
                let fields = field_types.into();
                let ty = self.types.intern(Type::Struct { definition, fields });
                return self.fits_target(ty, written.name.span);
            }
            ast::Type::Enum(_) => "enum declarations",
            ast::Type::Interface(_) => "interface declarations",
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
                "extern" if function => continue,
                "extern" => "`extern` names a C function, so only a function has it".to_owned(),
                _ => format!("unknown attribute `{name}`"),
            };
            let span = tree.children.attributes.span(attribute);
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
    }

    /// The type of the module's function or method `definition`.
    pub(crate) fn check_function(&mut self, definition: DefId) -> TypeId {
        let tree = &self.module().ast;
        let declaration = tree.declaration(definition.index);
        let ast::Decl::Function(ast::Function {
            name,
            signature,
            body,
            ..
        }) = tree.decls[declaration]
        else {
            unreachable!("`{definition:?}` is a function declaration")
        };
        if let Some(receiver) = signature.receiver {
            return self.not_supported(tree.children.params.span(receiver), "methods");
        }
        if let Some(parameter) = tree.children.lists.items(signature.type_parameters).next() {
            return self.not_supported(tree.children.names.span(parameter), "generic functions");
        }
        let symbol = project::extern_symbol(self.modules, definition);
        if let Err(span) = symbol {
            let message = "write `#[extern(\"name\")]` once, with the C function's name";
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
        // A malformed `extern` is still read as one, so its signature gets
        // the C rules.
        let external = symbol != Ok(None);
        let mut parameters = Vec::with_capacity(signature.parameters.len());
        for parameter in tree.children.lists.items(signature.parameters) {
            let parameter = tree.children.params[parameter];
            if let Some(default) = parameter.default {
                return self.not_supported(tree.exprs.span(default), "default parameters");
            }
            parameters.push(self.resolve_signature_type(parameter.ty, external));
        }
        let result = signature.result.map_or(TypeId::VOID, |result| {
            self.resolve_signature_type(result, external)
        });
        let Some(origins) = self.result_origins(&signature, &parameters, result) else {
            return TypeId::INVALID;
        };
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
        let parameters = parameters.into();
        self.types.intern(Type::Function {
            parameters,
            result,
            origins,
        })
    }

    /// The positions of the parameters a reference `result` of `signature`
    /// may point into, in order: those `in` names, else every reference
    /// parameter (every `&mut` one for a `&mut` result). `None` when an
    /// error was reported.
    fn result_origins(
        &mut self,
        signature: &ast::Signature,
        parameters: &[TypeId],
        result: TypeId,
    ) -> Option<Box<[u32]>> {
        let tree = &self.module().ast;
        let file = &tree.file;
        let mut names = tree.children.lists.items(signature.origins);
        let Some(mutable) = self.types[result].borrows() else {
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
        };
        // `Some(mutable)` for a reference or slice parameter.
        let reference =
            |types: &semantic_types::Types, position: usize| types[parameters[position]].borrows();
        let mut origins: Vec<u32> = Vec::new();
        for origin in names {
            let span = tree.children.names.span(origin);
            let name = file.slice(span);
            let position = tree
                .children
                .lists
                .items(signature.parameters)
                .position(|parameter| {
                    tree.children.params[parameter]
                        .name
                        .is_some_and(|written| written.text(file) == name)
                });
            let error = match position {
                None => Some((
                    diagnostics::UNDEFINED_NAME,
                    format!("`{name}` is not a parameter"),
                )),
                Some(position) => match reference(&self.types, position) {
                    None => Some((
                        diagnostics::INVALID_RETURN,
                        format!(
                            "`{name}` is not a reference or a slice, so a result cannot borrow from it"
                        ),
                    )),
                    Some(false) if mutable => Some((
                        diagnostics::INVALID_RETURN,
                        format!("a `&mut` result cannot borrow from shared reference `{name}`"),
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
                    reference(&self.types, position).is_some_and(|source| source || !mutable)
                })
                .map(|position| position as u32)
                .collect();
            if origins.is_empty() {
                let kind = if mutable {
                    "`&mut` or `[&mut]`"
                } else {
                    "reference or slice"
                };
                let message = format!("a {kind} result needs a {kind} parameter to borrow from");
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
        match self.types[resolved] {
            Type::Named(definition) => {
                let span = self.module().ast.types.span(ty);
                self.declared_type(definition, span)
            }
            _ => resolved,
        }
    }

    /// The type of a parameter or result written `ty`, of a C function when
    /// `external`.
    fn resolve_signature_type(&mut self, ty: Id<ast::Type>, external: bool) -> TypeId {
        let resolved = self.resolve_value_type(ty);
        if external
            && matches!(
                self.types[resolved],
                Type::Struct { .. } | Type::Array { .. } | Type::Range { .. } | Type::Slice { .. }
            )
        {
            // shortcut: structs, arrays, ranges and slices pass as LLVM
            // values, which the C ABI does not match; allow structs and arrays
            // when its aggregate rules are written (a range or slice has no C
            // type).
            let span = self.module().ast.types.span(ty);
            let what =
                "struct, array, range and slice parameters and results of `extern` functions";
            return self.not_supported(span, what);
        }
        if external
            && let Type::Integer(integer) = self.types[resolved]
            && integer.bits > self.target.widest_c_integer()
        {
            let (span, target) = (self.module().ast.types.span(ty), self.target.name);
            let widest = self.target.widest_c_integer();
            let message = format!(
                "C has no `{integer}` on {target}, whose integers have at most {widest} bits"
            );
            return self.error(diagnostics::INVALID_TYPE, message, span);
        }
        resolved
    }

    /// `&target`, or `&mut target` when `mutable`, written or borrowed at
    /// `span`: invalid when `target` is, reported when it is a reference.
    /// Its target is the real type, so `&Meters` is `&f64` under an alias.
    pub(crate) fn reference_type(&mut self, mutable: bool, target: TypeId, span: Span) -> TypeId {
        match self.types[target] {
            Type::Invalid => target,
            Type::Reference { .. } => self.not_supported(span, "references to references"),
            Type::Slice { .. } => self.not_supported(span, "references to slices"),
            _ => {
                let target = self.types.real(target);
                self.types.intern(Type::Reference { mutable, target })
            }
        }
    }

    /// `[length]element`, written at `span`: invalid when `element` is,
    /// reported when it is a reference. Its element is the real type, as a
    /// reference's target.
    pub(crate) fn array_type(&mut self, element: TypeId, length: u64, span: Span) -> TypeId {
        match self.types[element] {
            Type::Invalid => element,
            // shortcut: the loan check follows no reference held in an
            // array, as in a struct field; allow both when it does.
            Type::Reference { .. } => self.not_supported(span, "arrays of references"),
            Type::Slice { .. } => self.not_supported(span, "arrays of slices"),
            _ => {
                let element = self.types.real(element);
                let ty = self.types.intern(Type::Array { element, length });
                self.fits_target(ty, span)
            }
        }
    }

    /// `ty`, made at `span`, or invalid after an error there when a value of
    /// it takes more bytes than the target allows, which no copy of it could
    /// count.
    fn fits_target(&mut self, ty: TypeId, span: Span) -> TypeId {
        let (size, _) = layout(&self.types, ty, self.target);
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

    /// `*target`, written or taken at `span`: invalid when `target`
    /// is, reported when it is a reference. Its target is the real type, as
    /// a reference's.
    pub(crate) fn raw_pointer_type(&mut self, target: TypeId, span: Span) -> TypeId {
        match self.types[target] {
            Type::Invalid => target,
            // shortcut: no loan follows a reference stored behind a raw
            // pointer; allow it when lifetimes are checked (ownership step 4).
            Type::Reference { .. } => self.not_supported(span, "raw pointers to references"),
            Type::Slice { .. } => self.not_supported(span, "raw pointers to slices"),
            _ => self.types.intern(Type::RawPointer {
                target: Some(self.types.real(target)),
            }),
        }
    }

    /// The type a type expression of the module's tree stands for, its names
    /// recorded (`record`). A named type stays a name (`Type::Named`); a
    /// value's type is `resolve_value_type`. A local never names a type
    /// (`ThirBuilder::declare`), so only top-level names are looked up.
    fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module().ast;
        let span = tree.types.span(ty);
        let what = match tree.types[ty] {
            ast::Type::Name => {
                let name = tree.file.slice(span);
                let symbol = self.global(name);
                if let Some(symbol) = symbol {
                    self.record(span, FactKind::Name(symbol));
                }
                match symbol {
                    Some(Symbol::Builtin(builtin)) => return builtin,
                    Some(Symbol::Type(definition)) => return self.named_type(definition, span),
                    Some(Symbol::Function(_) | Symbol::Constant(_) | Symbol::Local(_)) => {
                        let message = format!("`{name}` is a value, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
                    }
                    Some(Symbol::Module(_)) => {
                        let message = format!("`{name}` is a module, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
                    }
                    None if name == semantic_types::RANGE => {
                        let message = format!("`{name}` needs the type of its values: `{name}[T]`");
                        return self.error(diagnostics::INVALID_TYPE, message, span);
                    }
                    None if let Some(what) = unsupported_type(name) => what,
                    None => {
                        let symbols = self.visible_symbols();
                        let types =
                            symbols.filter(|&(_, symbol)| matches!(symbol, Symbol::Type(_)));
                        let builtin = semantic_types::builtin_names();
                        let meant =
                            scope::nearest(name, builtin, types.map(|(declared, _)| declared));
                        let message = format!("unknown type `{name}`");
                        self.report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
                        return TypeId::INVALID;
                    }
                }
            }
            ast::Type::Invalid => return TypeId::INVALID, // the parser reported it
            ast::Type::Path(_) => "types of other modules",
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
                let target = self.resolve_value_type(target);
                return self.reference_type(mutable, target, span);
            }
            ast::Type::Optional(_) => "optional types",
            ast::Type::RawPointer => return TypeId::RAWPTR,
            ast::Type::Pointer(target) => {
                let target = self.resolve_value_type(target);
                return self.raw_pointer_type(target, span);
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
            ast::Type::DynamicArray(_) => "dynamic array types",
            // Invalid when `element` is, reported when it borrows. Its
            // element is the real type, as an array's.
            ast::Type::Slice { mutable, element } => {
                let element = self.resolve_value_type(element);
                return match self.types[element] {
                    Type::Invalid => element,
                    // shortcut: as for arrays, the loan check follows no
                    // reference held in a slice's elements.
                    Type::Reference { .. } => self.not_supported(span, "slices of references"),
                    Type::Slice { .. } => self.not_supported(span, "slices of slices"),
                    _ => {
                        let element = self.types.real(element);
                        self.types.intern(Type::Slice { mutable, element })
                    }
                };
            }
            ast::Type::Function(_) => "function types",
            ast::Type::Struct(_) => "struct types",
            ast::Type::Enum(_) => "enum types",
            ast::Type::Interface(_) => "interface types",
        };
        self.not_supported(span, what)
    }
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
            "fn add(a: i32, b: i32) -> i32 { return a; }\nfn main() {}\nfn take(s: cstr) {}\n",
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
            "fn a(x: Missing) {}\nfn b(m: utils) {}\nfn c(r: &i32) -> &i32 in q {}\nfn d<T>(x: T) {}\nfn e(x: i32 = 1) {}\nstruct P { x: i32 }\n#[extern(\"f\")]\nfn f(p: P);\nfn ok(p: P) -> P { return p; }\nfn g(x: a) {}\nfn (p: i32) method() {}\nfn h(s: str) {}\n",
        );
        let expected = reported(&[
            ("T0002", "unknown type `Missing`", "Missing"),
            ("T0021", "`utils` is a module, not a type", "utils"),
            ("T0002", "`q` is not a parameter", "q"),
            ("T0043", "generic functions are not supported yet", "T"),
            ("T0043", "default parameters are not supported yet", "1"),
            (
                "T0043",
                "struct, array, range and slice parameters and results of `extern` functions are not supported yet",
                "P",
            ),
            ("T0021", "`a` is a value, not a type", "a"),
            ("T0043", "methods are not supported yet", "p: i32"),
            ("T0043", "string types are not supported yet", "str"),
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
                "struct, array, range and slice parameters and results of `extern` functions are not supported yet",
                "P",
            ),
        ]);
        assert_eq!(found_reported, expected);
    }
}
