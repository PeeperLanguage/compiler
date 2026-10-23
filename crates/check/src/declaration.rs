//! Module-scope declarations, without their bodies, one check per category:
//! a type declaration's type, a function's type and a constant's type and
//! value.

use arena::Id;
use model::DefId;
use model::ast;
use model::constant::Constant;
use model::semantic_types;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Body;

use crate::Checker;
use crate::builtin::unsupported_type;
use crate::scope;
use crate::scope::report_redeclared;
use crate::statement::ThirBuilder;
use model::Symbol;

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
                let name_of = |field: Id<ast::Field>| tree.fields[field].name;
                for (index, field) in tree.lists.items(fields).enumerate() {
                    let ast::Field { name, ty } = tree.fields[field];
                    let text = name.text(&tree.file);
                    // A struct has few fields, so the ones before are searched.
                    let mut before = tree.lists.items(fields).take(index).map(name_of);
                    if let Some(first) = before.find(|first| first.text(&tree.file) == text) {
                        let diagnostic =
                            report_redeclared(&tree.file, text, "struct", name.span, first.span);
                        self.report(diagnostic);
                    }
                    // A struct held by value is checked here: one that holds
                    // itself, however deep, is a cycle.
                    field_types.push(self.resolve_value_type(ty));
                }
                let fields = field_types.into();
                return self.types.intern(Type::Struct { definition, fields });
            }
            ast::Type::Enum(_) => "enum declarations",
            ast::Type::Interface(_) => "interface declarations",
            _ => unreachable!("the parser gives a type declaration only these types"),
        };
        self.not_supported(written.name.span, what)
    }

    /// The type of the module's function or method `definition`.
    pub(crate) fn check_function(&mut self, definition: DefId) -> TypeId {
        let tree = &self.module().ast;
        let declaration = tree.declaration(definition.index);
        let ast::Decl::Function(ast::Function { signature, .. }) = tree.decls[declaration] else {
            unreachable!("`{definition:?}` is a function declaration")
        };
        if let Some(receiver) = signature.receiver {
            return self.not_supported(tree.params.span(receiver), "methods");
        }
        if let Some(parameter) = tree.lists.items(signature.type_parameters).next() {
            return self.not_supported(tree.names.span(parameter), "generic functions");
        }
        if let Some(origin) = tree.lists.items(signature.origins).next() {
            return self.not_supported(tree.names.span(origin), "`from` origins");
        }
        let mut parameters = Vec::with_capacity(signature.parameters.len());
        for parameter in tree.lists.items(signature.parameters) {
            let parameter = tree.params[parameter];
            if let Some(default) = parameter.default {
                return self.not_supported(tree.exprs.span(default), "default parameters");
            }
            parameters.push(self.resolve_signature_type(parameter.ty));
        }
        let result = signature
            .result
            .map_or(TypeId::VOID, |result| self.resolve_signature_type(result));
        let parameters = parameters.into();
        self.types.intern(Type::Function { parameters, result })
    }

    /// The type and value of the module's constant `definition`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// variables in scope.
    pub(crate) fn check_constant(&mut self, definition: DefId) -> Option<Constant> {
        let declaration = self.module().ast.declaration(definition.index);
        let mut body = Body::default();
        ThirBuilder::new(self, &mut body, TypeId::VOID, None).check_const_statement(declaration)
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

    /// The type of a parameter or result written `ty`.
    ///
    /// TEMPORARY, to be deleted with struct parameters and results (step 4c
    /// of the type plan): struct values do not pass between functions yet.
    fn resolve_signature_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let resolved = self.resolve_value_type(ty);
        if let Type::Struct { .. } = self.types[resolved] {
            let span = self.module().ast.types.span(ty);
            return self.not_supported(span, "struct parameters and results");
        }
        resolved
    }

    /// The type a type expression of the module's tree stands for.
    pub(crate) fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module().ast;
        let span = tree.types.span(ty);
        let what = match tree.types[ty] {
            ast::Type::Name => {
                let name = tree.file.slice(span);
                let pointer_bits = self.pointer_bits;
                if let Some(builtin) = self.types.builtin(name, pointer_bits) {
                    return builtin;
                }
                match self.global(name) {
                    Some(Symbol::Type(definition)) => return self.named_type(definition, span),
                    Some(Symbol::Function(_) | Symbol::Constant(_) | Symbol::Local(_)) => {
                        let message = format!("`{name}` is a value, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
                    }
                    Some(Symbol::Module(_)) => {
                        let message = format!("`{name}` is a module, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
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
            ast::Type::Reference { .. } => "reference types",
            ast::Type::OwnedPointer(_) => "owned pointer types",
            ast::Type::Optional(_) => "optional types",
            ast::Type::RawPointer => "raw pointers",
            ast::Type::FixedArray { .. } => "array types",
            ast::Type::DynamicArray(_) => "dynamic array types",
            ast::Type::Slice(_) => "slice types",
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
        let (found, types, reported) = with_checker(text, |checker| {
            let tree = &checker.module().ast;
            let declarations: Vec<_> = tree.lists.items(tree.top_level).collect();
            let found: Vec<TypeId> = declarations
                .into_iter()
                .enumerate()
                .map(|(index, declaration)| {
                    let definition = DefId {
                        module: checker.current_module,
                        index: index as u32,
                    };
                    // As a compile asks, so a use does not report it again.
                    let written = checker.module().ast.decls[declaration];
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
            Type::Function { parameters, result } => format!("fn{parameters:?} -> {result:?}"),
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
            "fn a(x: Missing) {}\nfn b(m: utils) {}\nfn c(r: &i32) {}\nfn d<T>(x: T) {}\nfn e(x: i32 = 1) {}\nstruct P { x: i32 }\nfn f(p: P) {}\nfn g(x: a) {}\nfn (p: i32) method() {}\nfn h(s: str) {}\n",
        );
        let expected = reported(&[
            ("T0002", "unknown type `Missing`", "Missing"),
            ("T0021", "`utils` is a module, not a type", "utils"),
            ("T0043", "reference types are not supported yet", "&i32"),
            ("T0043", "generic functions are not supported yet", "T"),
            ("T0043", "default parameters are not supported yet", "1"),
            (
                "T0043",
                "struct parameters and results are not supported yet",
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
}
