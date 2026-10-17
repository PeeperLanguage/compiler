//! Signatures: what a declaration's header says, without its body. A
//! function's signature is its function type. A constant has none: its type
//! comes with its value (`Checker::constant`).

use arena::Id;
use model::syntax;
use model::types::Type;
use model::types::TypeId;

use crate::Checker;
use crate::Queries;
use crate::builtin::unsupported_type;
use model::Binding;

impl<Q: Queries> Checker<'_, Q> {
    /// The signature of the module's declaration `declaration`, which is not
    /// a constant.
    pub fn signature(&mut self, declaration: Id<syntax::Decl>) -> TypeId {
        let tree = &self.module.syntax;
        match tree.decls[declaration] {
            syntax::Decl::Function(function) => {
                if let Some(receiver) = function.receiver {
                    return self.not_supported(tree.params.span(receiver), "methods");
                }
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
            syntax::Decl::Const { .. } => unreachable!("a constant's type comes with its value"),
            syntax::Decl::Struct(declaration) => {
                self.not_supported(declaration.name.span, "struct declarations")
            }
            syntax::Decl::Enum(declaration) => {
                self.not_supported(declaration.name.span, "enum declarations")
            }
            syntax::Decl::Interface(declaration) => {
                self.not_supported(declaration.name.span, "interface declarations")
            }
            syntax::Decl::Alias(declaration) => {
                self.not_supported(declaration.name.span, "type aliases")
            }
        }
    }

    /// The type a type expression of the module's tree stands for.
    pub fn resolve_type(&mut self, ty: Id<syntax::Type>) -> TypeId {
        let tree = &self.module.syntax;
        let span = tree.types.span(ty);
        let what = match tree.types[ty] {
            syntax::Type::Name => {
                let name = tree.file.slice(span);
                let pointer_bits = self.queries.pointer_bits();
                if let Some(builtin) = self.queries.types().builtin(name, pointer_bits) {
                    return builtin;
                }
                match self.global(name) {
                    Some(Binding::Type(_)) => "named types",
                    Some(Binding::Function(_) | Binding::Constant(_)) => {
                        let message = format!("`{name}` is a value, not a type");
                        return self.error(diagnostics::NOT_A_TYPE, message, span);
                    }
                    Some(Binding::Module(_)) => {
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
            syntax::Type::Invalid => return TypeId::INVALID, // the parser reported it
            syntax::Type::Path(_) => "types of other modules",
            syntax::Type::Generic { .. } => "generic types",
            syntax::Type::Reference { .. } => "reference types",
            syntax::Type::OwnedPointer(_) => "owned pointer types",
            syntax::Type::Optional(_) => "optional types",
            syntax::Type::RawPointer => "raw pointers",
            syntax::Type::FixedArray { .. } => "array types",
            syntax::Type::DynamicArray(_) => "dynamic array types",
            syntax::Type::Slice(_) => "slice types",
            syntax::Type::Function { .. } => "function types",
            syntax::Type::Struct(_) => "struct types",
            syntax::Type::Enum(_) => "enum types",
            syntax::Type::Interface(_) => "interface types",
        };
        self.not_supported(span, what)
    }
}

#[cfg(test)]
mod tests {
    use model::types::Type;
    use model::types::TypeId;

    use crate::testing::Reported;
    use crate::testing::reported;
    use crate::testing::with_checker;

    /// The signature of every declaration of `text`, as type text, and the
    /// diagnostics.
    fn signatures(text: &str) -> (Vec<String>, Vec<Reported>) {
        let (found, types, reported) = with_checker(text, |checker| {
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
        let (found, found_reported) = signatures(
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
