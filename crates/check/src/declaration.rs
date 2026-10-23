//! Module-scope declarations, without their bodies, one check per category:
//! a type declaration's type, a function's type and a constant's type and
//! value.

use arena::Id;
use model::ast;
use model::constant::Constant;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Body;

use crate::Checker;
use crate::builtin::unsupported_type;
use crate::scope::report_redeclared;
use crate::statement::ThirBuilder;
use model::Symbol;

impl Checker<'_> {
    /// The type written after the name of the module's type declaration or
    /// alias `declaration`: a struct's fields (`Type::Struct`), or the type an
    /// alias names. Enums, interfaces and type parameters are not supported
    /// yet.
    pub(crate) fn check_type_declaration(&mut self, declaration: Id<ast::Decl>) -> TypeId {
        let tree = &self.module().ast;
        let (written, is_alias) = match tree.decls[declaration] {
            ast::Decl::Type(written) => (written, false),
            ast::Decl::Alias(written) => (written, true),
            _ => unreachable!("`{declaration:?}` is a type declaration"),
        };
        if !written.type_parameters.is_empty() {
            return self.not_supported(written.name.span, "generic types");
        }
        if is_alias {
            return self.resolve_type(written.ty);
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
                    let ty = self.resolve_type(ty);
                    // A type held by value holds what it holds: one that holds
                    // itself, however deep, is a cycle.
                    if let Type::Named(held) = self.types[ty] {
                        self.declared_type(held);
                    }
                    field_types.push(ty);
                }
                return self.types.intern(Type::Struct(field_types.into()));
            }
            ast::Type::Enum(_) => "enum declarations",
            ast::Type::Interface(_) => "interface declarations",
            _ => unreachable!("the parser gives a type declaration only these types"),
        };
        self.not_supported(written.name.span, what)
    }

    /// The type of the module's function or method `declaration`.
    pub(crate) fn check_function(&mut self, declaration: Id<ast::Decl>) -> TypeId {
        let tree = &self.module().ast;
        let ast::Decl::Function(ast::Function { signature, .. }) = tree.decls[declaration] else {
            unreachable!("`{declaration:?}` is a function declaration")
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
            parameters.push(self.resolve_value_type(parameter.ty));
        }
        let result = signature
            .result
            .map_or(TypeId::VOID, |result| self.resolve_value_type(result));
        let parameters = parameters.into();
        self.types.intern(Type::Function { parameters, result })
    }

    /// The type and value of the module's constant `declaration`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// variables in scope.
    pub(crate) fn check_constant(&mut self, declaration: Id<ast::Decl>) -> Option<Constant> {
        let mut body = Body::default();
        ThirBuilder::new(self, &mut body, TypeId::VOID, None).check_const_statement(declaration)
    }

    /// The type of a value whose type is written `ty`. Values of a type
    /// declaration's type are not supported until MIR and LLVM have them.
    pub(crate) fn resolve_value_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let resolved = self.resolve_type(ty);
        if let Type::Named(_) = self.types[resolved] {
            let span = self.module().ast.types.span(ty);
            let what = format!("values of `{}`", self.type_text(resolved));
            return self.not_supported(span, &what);
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
                    Some(Symbol::Type(definition)) => return self.named_type(definition),
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
                        let message = format!("unknown type `{name}`");
                        return self.error(diagnostics::UNDEFINED_NAME, message, span);
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
                .map(
                    |(index, declaration)| match checker.module().ast.decls[declaration] {
                        ast::Decl::Function(_) => checker.check_function(declaration),
                        // As a compile asks, so a use does not report it again.
                        _ => checker.declared_type(DefId {
                            module: checker.current_module,
                            index: index as u32,
                        }),
                    },
                )
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
            ("T0043", "values of `P` are not supported yet", "P"),
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
