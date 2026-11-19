//! Type and `const` parameters: finding the types a copy is made for, from
//! what is written or from a call's arguments; the copies themselves
//! (`copies`) and the constraints after `where` (`constraints`).

mod constraints;
mod copies;

use arena::Id;
use arena::List;
use model::Callee;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::Checker;
use crate::builder;

impl Checker<'_> {
    /// The value type the argument `argument` is read as where a parameter
    /// takes a type: a type the parser read (`ast::Expr::Type`), or a value
    /// spelled like a type, a name, a path, `&x`, `&mut x` or `range[x]`.
    /// Anything else is reported as not a type.
    pub(crate) fn resolve_type_argument(
        &mut self,
        argument: Id<ast::Expr>,
    ) -> semantic_types::TypeId {
        let resolved = self.expression_type(argument);
        let span = self.module().ast.exprs.span(argument);
        let resolved = self.type_argument(resolved, span);
        self.value_type_at(resolved, span)
    }

    /// `ty`, a type argument written or found at `span`, when a copy can be
    /// made for it; an owned interface is reported.
    pub(crate) fn type_argument(
        &mut self,
        ty: semantic_types::TypeId,
        span: Span,
    ) -> semantic_types::TypeId {
        if self.is_interface(ty) {
            return self.not_supported(span, "owned interfaces as type arguments");
        }
        ty
    }
}

impl builder::ThirBuilder<'_, '_> {
    /// The type `argument`, written in the body for a type (a `const T:
    /// type` argument, a constraint's subject), names. A local of its name
    /// is a value, reported; a type of its name elsewhere is still the type.
    pub(crate) fn written_type_argument(
        &mut self,
        argument: Id<ast::Expr>,
    ) -> semantic_types::TypeId {
        let tree = &self.checker.module().ast;
        let written = tree.exprs.span(argument);
        if let ast::Expr::Name = tree.exprs[argument]
            && let name = tree.file.slice(written)
            && let Some(&position) = self.innermost.get(name)
            && self.checker.resolve_global(name).is_none()
        {
            let local = thir::Symbol::Local(self.names[position].local);
            return self.checker.type_named_by(local, written);
        }
        self.checker.resolve_type_argument(argument)
    }

    /// The copy of the function `definition` that its call at `span` with
    /// the written `arguments`, after `first` of the receiver, asks for,
    /// and its type. The argument in the place of each `const` parameter,
    /// put in `const_places`, is what the copy is made for: a type for
    /// `const T: type` (`resolve_type_argument`), else a value of the
    /// parameter's type known when compiling. A type parameter, `<T>`,
    /// takes no argument: its type is written at the call, in
    /// `type_arguments`, or found from the arguments of the parameters
    /// whose types name it, each matched against its parameter's type in the
    /// function's own type (`Types::find_parameters`), checked here, once,
    /// until every type parameter is found, and left in `checked_early` at
    /// its place for `check_call` to take. When a `const` one is missing, the
    /// function itself and its own type, for `check_call` to report the
    /// count. `None` when one has an error, or the function's signature
    /// has one, reported.
    #[expect(clippy::too_many_arguments, reason = "each is one fact of the call")]
    pub(crate) fn copy_called(
        &mut self,
        definition: DefId,
        type_arguments: List<ast::Type>,
        arguments: &[Id<ast::Expr>],
        first: usize,
        const_places: &mut Vec<usize>,
        checked_early: &mut Vec<Option<thir::Expr>>,
        span: Span,
    ) -> Option<(Callee, semantic_types::TypeId)> {
        let modules = self.checker.modules;
        let tree = &modules[definition.module.index()].ast;
        let declared = model::program::function_declaration(modules, definition);
        let parameters = tree.children.lists.items(declared.signature.parameters);
        // The parameters past the receiver, each with its written argument's place.
        let written_parameters: Vec<(usize, ast::Param)> = (parameters.skip(first))
            .map(|parameter| tree.children.params[parameter])
            .enumerate()
            .collect();
        let const_parameters = (written_parameters.iter().copied())
            .filter(|(_, parameter)| parameter.constant)
            .collect::<Vec<_>>();
        const_places.extend(const_parameters.iter().map(|&(place, _)| place));
        let pattern = self.checker.function_type(definition, span);
        if pattern == semantic_types::TypeId::INVALID {
            return None; // reported
        }
        // Too few arguments: `check_call` reports it, by the function's own type.
        let required = (written_parameters.iter())
            .filter(|(_, parameter)| {
                !matches!(tree.types[parameter.ty], ast::Type::Variadic { .. })
            })
            .count();
        if arguments.len() < required {
            let function = DeclarationCopy {
                definition,
                arguments: ConstArguments::NONE,
            };
            return Some((Callee::Function(function), pattern));
        }
        // Each written parameter's type in the function's own type, which
        // leaves out the `const` ones; `None` for those.
        let semantic_types::Type::Function {
            parameters: pattern_parameters,
            ..
        } = &self.checker.types[pattern]
        else {
            unreachable!("a function's type is a function type")
        };
        let mut pattern_parameters = pattern_parameters.iter().skip(first).copied();
        let parameter_types: Vec<Option<semantic_types::TypeId>> = (written_parameters.iter())
            .map(|(_, parameter)| (!parameter.constant).then(|| pattern_parameters.next()))
            .map(Option::flatten)
            .collect();
        // Its type parameters, `<T>`, which take no argument of their own:
        // the types written at the call, or else found from the arguments.
        let names: Vec<&str> = (tree.children.lists)
            .items(declared.signature.type_parameters)
            .map(|parameter| tree.file.slice(tree.children.names.span(parameter)))
            .collect();
        let calling = &self.checker.module().ast;
        let written_types: Vec<Id<ast::Type>> =
            calling.children.lists.items(type_arguments).collect();
        let function = model::program::declared_path(modules, definition);
        let mut failed = false;
        let mut types_found = vec![None; names.len()];
        if !written_types.is_empty() {
            if written_types.len() == names.len() {
                for (found, &written) in types_found.iter_mut().zip(&written_types) {
                    *found = Some(self.checker.resolve_value_type(written));
                }
            } else {
                let (count, given) = (names.len(), written_types.len());
                let message =
                    crate::argument_count_message(&function, "type argument", count, false, given);
                self.checker
                    .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
                failed = true;
            }
        } else if !names.is_empty() {
            checked_early.resize(arguments.len(), None);
            for (&(place, parameter), &parameter_type) in
                written_parameters.iter().zip(&parameter_types)
            {
                // The rest are checked as the copy's parameters, so a
                // literal takes the found type: `Max(small, 1)`.
                if types_found.iter().all(Option::is_some) {
                    break;
                }
                // shortcut: the arguments of a `...T` do not say what `T` is,
                // as `check_variadic` checks them; find it from them when a
                // function needs it.
                let Some(parameter_type) = parameter_type else {
                    continue; // `const`
                };
                if place >= arguments.len()
                    || matches!(tree.types[parameter.ty], ast::Type::Variadic { .. })
                    || !(self.checker.types)
                        .parameters_named(parameter_type, definition, names.len())
                        .contains(&true)
                {
                    continue;
                }
                let checked = self.check_expr(arguments[place], None);
                if checked.ty == semantic_types::TypeId::VOID {
                    let written = self.checker.module().ast.exprs.span(arguments[place]);
                    let message = "this expression has no value to pass";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, written);
                    failed = true;
                }
                failed |= checked.ty == semantic_types::TypeId::INVALID; // reported
                (self.checker.types).find_parameters(
                    parameter_type,
                    checked.ty,
                    definition,
                    &mut types_found,
                );
                checked_early[place] = Some(checked);
            }
        }
        // A type parameter no parameter's type names, only the call can give.
        let mut named = vec![false; names.len()];
        for &ty in parameter_types.iter().flatten() {
            let found = (self.checker.types).parameters_named(ty, definition, names.len());
            for (named, found) in named.iter_mut().zip(found) {
                *named |= found;
            }
        }
        let mut given = Vec::with_capacity(names.len() + const_parameters.len());
        for ((name, found), named) in names.iter().zip(types_found).zip(named) {
            match found {
                Some(ty) => {
                    let ty = self.checker.type_argument(ty, span);
                    failed |= ty == semantic_types::TypeId::INVALID;
                    given.push(semantic_types::ConstArgument::Type(ty));
                }
                None if failed => {}
                None => {
                    let hint = if named {
                        String::new()
                    } else {
                        format!(": write it at the call, `{function}::<...>(...)`")
                    };
                    let message = format!(
                        "the arguments of `{function}` do not say what its `{name}` is{hint}"
                    );
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    failed = true;
                }
            }
        }
        for (place, parameter) in const_parameters {
            let argument = arguments[place];
            if let ast::Type::Type = tree.types[parameter.ty] {
                let ty = self.written_type_argument(argument);
                failed |= ty == semantic_types::TypeId::INVALID;
                given.push(semantic_types::ConstArgument::Type(
                    self.checker.types.real(ty),
                ));
                continue;
            }
            let ty = self
                .checker
                .const_value_type(definition, parameter.ty, span);
            match self.check_constant_value(argument, Some(ty)) {
                Some(thir::CheckedConstant::Known(constant)) => {
                    given.push(semantic_types::ConstArgument::Value(constant));
                }
                // Reported, or each copy gives it. shortcut: given a value
                // each copy gives, no copy is made and the call stays
                // silent; give it its type once `ConstArguments` can hold a
                // per-copy value.
                Some(thir::CheckedConstant::PerCopy(_)) | None => failed = true,
            }
        }
        if failed {
            return None;
        }
        let copy = DeclarationCopy {
            definition,
            arguments: self.checker.types.const_arguments(&given),
        };
        // Given an open type, as in a generic body's own check, no copy is
        // made: the call is of the function's own type, given what it gives.
        // Nor is one in an own check, which is never lowered, for a call run
        // only when the program runs; one run at compile time is made, to run.
        let ty = if self.checker.types.arguments_open(copy.arguments)
            || (!self.in_constant && self.checker.in_own_check())
        {
            (self.checker.types).substitute(pattern, definition, copy.arguments)
        } else {
            self.checker.copy_type(copy, span)
        };
        Some((Callee::Function(copy), ty))
    }
}

/// Where the first parameter of `signature`, in `tree`, that a copy is
/// made for is written, if it has one: a type parameter, `<T>`, or a
/// `const` parameter.
pub fn first_copy_parameter(tree: &ast::Tree, signature: ast::Signature) -> Option<Span> {
    if let Some(parameter) = tree.children.lists.items(signature.type_parameters).next() {
        return Some(tree.children.names.span(parameter));
    }
    let mut parameters = tree.children.lists.items(signature.parameters);
    let parameter = parameters.find(|&parameter| tree.children.params[parameter].constant)?;
    Some(tree.children.params.span(parameter))
}
