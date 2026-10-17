//! Signatures: what a declaration's header says, without its body. A
//! function's signature is its function type; a constant's is its type.

use arena::Id;
use ir::types::Type;
use ir::types::TypeId;

use crate::Checker;
use crate::Queries;
use crate::builtin::builtin_type;
use crate::scope::Binding;

impl<Q: Queries> Checker<'_, Q> {
    /// The signature of the module's declaration `declaration`.
    pub fn signature(&mut self, declaration: Id<ast::Decl>) -> TypeId {
        let tree = &self.module.syntax;
        match tree.decls[declaration] {
            ast::Decl::Function(function) => {
                if let Some(parameter) = tree.lists.items(function.type_parameters).next() {
                    return self.not_supported(tree.names.span(parameter), "generic functions");
                }
                if let Some(origin) = tree.lists.items(function.origins).next() {
                    return self.not_supported(tree.names.span(origin), "`from` origins");
                }
                let mut parameters = Vec::with_capacity(function.parameters.len());
                for parameter in tree.lists.items(function.parameters) {
                    let parameter = tree.params[parameter];
                    if let Some(default) = parameter.default {
                        return self.not_supported(tree.exprs.span(default), "default parameters");
                    }
                    parameters.push(self.resolve_type(parameter.ty));
                }
                let result = function
                    .result
                    .map_or(TypeId::VOID, |result| self.resolve_type(result));
                let parameters = parameters.into();
                self.queries
                    .types()
                    .intern(Type::Function { parameters, result })
            }
            ast::Decl::Const { ty: Some(ty), .. } => self.resolve_type(ty),
            // shortcut: the type of `const A = value;` comes from its value,
            // which the body check (R5d4) folds; until then it is reported.
            ast::Decl::Const { name, ty: None, .. } => {
                self.not_supported(name, "constants without a type")
            }
            ast::Decl::Struct(declaration) => {
                self.not_supported(declaration.name, "struct declarations")
            }
            ast::Decl::Enum(declaration) => {
                self.not_supported(declaration.name, "enum declarations")
            }
            ast::Decl::Interface(declaration) => {
                self.not_supported(declaration.name, "interface declarations")
            }
            ast::Decl::Alias(declaration) => self.not_supported(declaration.name, "type aliases"),
        }
    }

    /// The type a type expression of the module's tree stands for.
    pub fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module.syntax;
        let span = tree.types.span(ty);
        let what = match tree.types[ty] {
            ast::Type::Name => {
                let name = tree.file.slice(span);
                let pointer_bits = self.queries.pointer_bits();
                if let Some(builtin) = builtin_type(name, pointer_bits, self.queries.types()) {
                    return builtin;
                }
                match self.scope.get(name) {
                    Some(Binding::Declaration(_)) => "named types",
                    Some(Binding::Module(_)) => {
                        let message = format!("`{name}` is a module, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
                    }
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
            ast::Type::Function { .. } => "function types",
            ast::Type::Struct(_) => "struct types",
            ast::Type::Enum(_) => "enum types",
            ast::Type::Interface(_) => "interface types",
        };
        self.not_supported(span, what)
    }
}

#[cfg(test)]
mod tests {
    use ir::types::Type;
    use ir::types::TypeId;

    use crate::testing::Reported;
    use crate::testing::check;
    use crate::testing::reported;

    /// The signature of every declaration of `text`, as type text, and the
    /// diagnostics.
    fn signatures(text: &str) -> (Vec<String>, Vec<Reported>) {
        let (found, types, reported) = check(text, |checker| {
            let tree = &checker.module.syntax;
            let declarations: Vec<_> = tree.lists.items(tree.declarations).collect();
            let found: Vec<TypeId> = declarations
                .into_iter()
                .map(|declaration| checker.signature(declaration))
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
        let (found, reported) = signatures(
            "fn add(a: i32, b: i32) -> i32 { return a; }\nfn main() {}\nconst Big: u256 = 1;\nconst Ok: bool = true;\n",
        );
        assert!(reported.is_empty(), "{reported:?}");
        let i32 = format!("{:?}", TypeId::I32);
        let void = format!("{:?}", TypeId::VOID);
        assert_eq!(found[0], format!("fn[{i32}, {i32}] -> {i32}"));
        assert_eq!(found[1], format!("fn[] -> {void}"));
        assert_eq!(found[2], "Integer(Integer { signed: false, bits: 256 })");
        assert_eq!(found[3], "Bool");
    }

    #[test]
    fn errors_are_located() {
        let (found, found_reported) = signatures(
            "fn a(x: Missing) {}\nfn b(m: utils) {}\nfn c(r: &i32) {}\nfn d<T>(x: T) {}\nfn e(x: i32 = 1) {}\nstruct P { x: i32 }\nfn f(p: P) {}\nconst N = 1;\n",
        );
        let expected = reported(&[
            ("T0002", "unknown type `Missing`", "Missing"),
            ("T0021", "`utils` is a module, not a type", "utils"),
            ("T0043", "reference types are not supported yet", "&i32"),
            ("T0043", "generic functions are not supported yet", "T"),
            ("T0043", "default parameters are not supported yet", "1"),
            ("T0043", "struct declarations are not supported yet", "P"),
            ("T0043", "named types are not supported yet", "P"),
            (
                "T0043",
                "constants without a type are not supported yet",
                "N",
            ),
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
