//! Module-scope declarations, without their bodies: a function's type and a
//! type declaration's type. A constant's type comes with its value
//! (`ModuleChecker::constant`).

use arena::Id;
use model::ast;
use model::semantic_types::Type;
use model::semantic_types::TypeId;

use crate::ModuleChecker;
use crate::builtin::unsupported_type;
use model::Symbol;

impl ModuleChecker<'_, '_> {
    /// The function type of the module's function or method `declaration`.
    pub fn function(&mut self, declaration: Id<ast::Decl>) -> TypeId {
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
            parameters.push(self.resolve_type(parameter.ty));
        }
        let result = signature
            .result
            .map_or(TypeId::VOID, |result| self.resolve_type(result));
        let parameters = parameters.into();
        self.queries
            .types
            .intern(Type::Function { parameters, result })
    }

    /// The type the module's type declaration or alias `declaration`
    /// declares; none is supported yet.
    pub fn type_declaration(&mut self, declaration: Id<ast::Decl>) -> TypeId {
        let tree = &self.module().ast;
        let (name, what) = match tree.decls[declaration] {
            ast::Decl::Type(declaration) => match tree.types[declaration.ty] {
                ast::Type::Struct(_) => (declaration.name, "struct declarations"),
                ast::Type::Enum(_) => (declaration.name, "enum declarations"),
                ast::Type::Interface(_) => (declaration.name, "interface declarations"),
                _ => unreachable!("the parser gives a type declaration only these types"),
            },
            ast::Decl::Alias(declaration) => (declaration.name, "type aliases"),
            _ => unreachable!("`{declaration:?}` is a type declaration"),
        };
        self.not_supported(name.span, what)
    }

    /// The type a type expression of the module's tree stands for.
    pub fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module().ast;
        let span = tree.types.span(ty);
        let what = match tree.types[ty] {
            ast::Type::Name => {
                let name = tree.file.slice(span);
                let pointer_bits = self.queries.pointer_bits;
                if let Some(builtin) = self.queries.types.builtin(name, pointer_bits) {
                    return builtin;
                }
                match self.global(name) {
                    Some(Symbol::Type(_)) => "named types",
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
                .map(
                    |declaration| match checker.module().ast.decls[declaration] {
                        ast::Decl::Function(_) => checker.function(declaration),
                        _ => checker.type_declaration(declaration),
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
            ("T0043", "struct declarations are not supported yet", "P"),
            ("T0043", "named types are not supported yet", "P"),
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
