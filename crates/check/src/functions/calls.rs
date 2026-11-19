//! Checking calls: of a function, a method or an interface's method, and
//! their arguments.

use std::num;

use arena::Id;
use arena::List;
use model::Callee;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::constant;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::builder;
use crate::generics;
use crate::scope;

impl builder::ThirBuilder<'_, '_> {
    /// `callee(arguments)`, written at `span`; a pipe gives its value as the
    /// first of `arguments`. Only a module's function, by
    /// its name or its path, can be called, or with a dot, `x.name(arguments)`,
    /// a type's function or an interface reference's method, which gets `x`
    /// as its `self`. A `...` parameter takes all the arguments past the
    /// others (`check_variadic`). A function with `const` parameters is
    /// called as its copy for what is written in their places
    /// (`copy_called`).
    pub(crate) fn check_call(
        &mut self,
        callee: Id<ast::Expr>,
        type_arguments: List<ast::Type>,
        arguments: &[Id<ast::Expr>],
        span: Span,
    ) -> thir::Expr {
        let tree = &self.checker.module().ast;
        let callee_span = tree.exprs.span(callee);
        let mut receiver = None;
        let found = match tree.exprs[callee] {
            ast::Expr::Name => {
                let symbol = match self.resolve(callee_span) {
                    // Their errors were reported.
                    Some(thir::Symbol::Local(thir::Local::Constant(constant)))
                        if self.body.constants[constant].is_none() =>
                    {
                        None
                    }
                    Some(thir::Symbol::Local(thir::Local::Variable(variable)))
                        if self.body.variables[variable].ty == semantic_types::TypeId::INVALID =>
                    {
                        None
                    }
                    symbol => symbol,
                };
                self.function_called(symbol, callee_span)
            }
            ast::Expr::Path(path) => {
                let symbol = self.checker.resolve_path(path);
                self.function_called(symbol, callee_span)
            }
            ast::Expr::Selector { value, field } => match self.method(value, field, callee_span) {
                Some(Some((place, callee, signature))) => {
                    receiver = Some(place);
                    Some((callee, signature))
                }
                // Each copy finds the function: the arguments are checked
                // for their own errors only.
                Some(None) => {
                    self.call_found(None, None, callee_span, type_arguments, arguments, span);
                    return thir::Expr {
                        ty: semantic_types::TypeId::UNDECIDED,
                        kind: thir::ExprKind::Undecided,
                    };
                }
                None => None,
            },
            _ => {
                self.checker
                    .not_supported(callee_span, "calls of computed functions");
                None
            }
        };
        self.call_found(
            found,
            receiver,
            callee_span,
            type_arguments,
            arguments,
            span,
        )
    }

    /// The call at `span` of `found`, the callee written at `callee_span`
    /// and its type, with `type_arguments` and the written `arguments`,
    /// after `receiver`'s for a method (`check_call`). `None` was reported.
    pub(crate) fn call_found(
        &mut self,
        found: Option<(Callee, semantic_types::TypeId)>,
        receiver: Option<Id<thir::Expr>>,
        callee_span: Span,
        type_arguments: List<ast::Type>,
        arguments: &[Id<ast::Expr>],
        span: Span,
    ) -> thir::Expr {
        let tree = &self.checker.module().ast;
        let text = tree.file.slice(callee_span);
        // The written arguments come after the receiver's.
        let first = usize::from(receiver.is_some());
        // The places among the written arguments of what a copy is for.
        let mut const_places = Vec::new();
        // The written arguments checked to find a type parameter, by place.
        let mut checked_early: Vec<Option<thir::Expr>> = Vec::new();
        let type_parameters = match found {
            Some((Callee::Function(function), _)) => {
                model::program::function_declaration(self.checker.modules, function.definition)
                    .signature
                    .type_parameters
                    .len()
            }
            _ => 0,
        };
        let mut type_arguments = type_arguments;
        if type_parameters == 0 && !type_arguments.is_empty() && found.is_some() {
            let message = format!("`{text}` takes no type arguments");
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, callee_span);
            type_arguments = List::EMPTY;
        }
        let found = match found {
            Some((Callee::Function(function), _))
                if let definition = function.definition
                    && let declared =
                        model::program::function_declaration(self.checker.modules, definition)
                    && let tree = &self.checker.modules[definition.module.index()].ast
                    && generics::first_copy_parameter(tree, declared.signature).is_some() =>
            {
                let early = &mut checked_early;
                let copied = self.copy_called(
                    definition,
                    type_arguments,
                    arguments,
                    first,
                    &mut const_places,
                    early,
                    span,
                );
                // The function's name ends its callee. Its own type, for too
                // few arguments, and a type with an error are of no copy.
                if let Some((Callee::Function(copy), ty)) = copied
                    && copy.arguments != ConstArguments::NONE
                    && ty != semantic_types::TypeId::INVALID
                {
                    self.checker.record_copy(callee_span.end, copy, ty);
                }
                copied
            }
            found => found,
        };
        let not_const = |&(place, _): &(usize, Id<ast::Expr>)| !const_places.contains(&place);
        let signature = found.map(|(callee, ty)| (callee, ty, self.checker.types[ty].clone()));
        let Some((
            callee,
            signature,
            semantic_types::Type::Function {
                parameters,
                result,
                variadic,
                ..
            },
        )) = signature
        else {
            let written = arguments.iter().copied().enumerate();
            let unchecked = |&(place, _): &(usize, Id<ast::Expr>)| {
                checked_early.get(place).is_none_or(Option::is_none)
            };
            for (_, argument) in written.filter(not_const).filter(unchecked) {
                let argument = match tree.exprs[argument] {
                    ast::Expr::Spread(values) => values,
                    _ => argument,
                };
                self.add_expr(argument, None); // for its own errors
            }
            return thir::Expr::INVALID;
        };
        let fixed = parameters.len() - usize::from(variadic) - first;
        // A `const` parameter is not among the type's, but takes an argument.
        let all = fixed + const_places.len();
        let arity_matches = if variadic {
            arguments.len() >= all
        } else {
            arguments.len() == all
        };
        if !arity_matches {
            let message =
                crate::argument_count_message(text, "argument", all, variadic, arguments.len());
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<num::NonZeroU32> = Vec::with_capacity(parameters.len());
        if let Some(place) = receiver {
            // `self` takes the value itself; `&self` and `&mut self` borrow it.
            let argument = match self.checker.types[parameters[0]] {
                semantic_types::Type::Reference { mutable, .. }
                | semantic_types::Type::InterfaceReference { mutable, .. } => {
                    let span = self.body.exprs.span(place);
                    let borrowed = self.check_borrow(mutable, place);
                    self.body.add(borrowed, span)
                }
                _ => place,
            };
            checked.push(argument.into());
        }
        let written = arguments.iter().copied().enumerate();
        let mut given = written.filter(not_const);
        for (&parameter, (place, argument)) in
            parameters[first..first + fixed].iter().zip(given.by_ref())
        {
            let argument = match checked_early.get_mut(place).and_then(Option::take) {
                Some(checked) => {
                    let required = self.require(checked, argument, parameter);
                    self.body.add(required, tree.exprs.span(argument))
                }
                None => self.add_required(argument, parameter),
            };
            self.reborrow_in_place(argument);
            checked.push(argument.into());
        }
        if variadic {
            let rest: Vec<Id<ast::Expr>> = given.map(|(_, argument)| argument).collect();
            let slice = self.check_variadic(&rest, parameters[first + fixed], span);
            self.reborrow_in_place(slice);
            checked.push(slice.into());
        } else {
            for (_, extra) in given {
                self.add_expr(extra, None); // for its own errors
            }
        }
        if !arity_matches {
            return thir::Expr::INVALID;
        }
        // A call given an open type, as in a generic body's own check, runs
        // in each copy only.
        let runs_now = !matches!(callee, Callee::Function(function)
            if self.checker.types.arguments_open(function.arguments));
        if let Callee::Function(function) = callee
            && runs_now
            && model::program::is_builtin(self.checker.modules, function.definition)
        {
            // Its answer is known now, wherever the call is.
            let Some(value) = self.checker.evaluate_call(function, &[], span) else {
                return thir::Expr::INVALID; // reported
            };
            let ty = match value {
                constant::Value::Bool(_) => semantic_types::TypeId::BOOL,
                _ => self.checker.types.usize,
            };
            let kind = thir::ExprKind::Constant(value);
            return thir::Expr { ty, kind };
        }
        let arguments = self.body.children.lists.add(&checked);
        // In a constant's value, a call on constants runs now.
        let constant_arguments = || {
            let values = self.body.children.lists.items(arguments).map(|argument| {
                match &self.body.exprs[argument].kind {
                    thir::ExprKind::Constant(value) => Some(value.clone()),
                    _ => None,
                }
            });
            values.collect::<Option<Vec<constant::Value>>>()
        };
        if !runs_now && self.in_constant {
            return thir::Expr {
                ty: result,
                kind: thir::ExprKind::Undecided,
            };
        }
        if let Callee::Function(function) = callee
            && runs_now
            && self.in_constant
            && result != semantic_types::TypeId::VOID
            && let Some(values) = constant_arguments()
        {
            return match self.checker.evaluate_call(function, &values, span) {
                Some(value) => thir::Expr {
                    ty: result,
                    kind: thir::ExprKind::Constant(value),
                },
                None => thir::Expr::INVALID,
            };
        }
        thir::Expr {
            ty: result,
            kind: thir::ExprKind::Call {
                callee,
                signature,
                arguments,
            },
        }
    }

    /// What the function `symbol`, called at `span`, is: its callee and
    /// type. `None` when it is not a function, reported, or when finding it
    /// reported an error.
    pub(crate) fn function_called(
        &mut self,
        symbol: Option<thir::Symbol>,
        span: Span,
    ) -> Option<(Callee, semantic_types::TypeId)> {
        match symbol {
            Some(thir::Symbol::Function(definition)) => {
                let ty = self.checker.function_type(definition, span);
                let function = DeclarationCopy {
                    definition,
                    arguments: ConstArguments::NONE,
                };
                Some((Callee::Function(function), ty))
            }
            Some(_) => {
                let text = self.checker.module().ast.file.slice(span);
                let message = format!("`{text}` is not a function");
                self.checker.error(diagnostics::NOT_CALLABLE, message, span);
                None
            }
            None => None,
        }
    }

    /// The function `name` of the type `value` is or points to, or the
    /// method `name` of the interface or interface reference `value` is,
    /// called with a dot written at `span`: the place of the value, of the
    /// interface, or of what the interface reference views, which the call
    /// gives as `self`, the callee, and its type. An interface's own
    /// function (`fn Shape::name`) is found before its methods; a name
    /// cannot be both (`scope::collect`). `None` when reported; `Some(None)`
    /// for a value of an open type (`Types::is_open`), whose function each
    /// copy finds.
    fn method(
        &mut self,
        value: Id<ast::Expr>,
        name: ast::Ident,
        span: Span,
    ) -> Option<Option<(Id<thir::Expr>, Callee, semantic_types::TypeId)>> {
        let value = self.add_expr(value, None);
        let ty = self.body.exprs[value].ty;
        let interface = match self.checker.types[ty] {
            semantic_types::Type::InterfaceReference { interface, .. } => Some(interface),
            semantic_types::Type::Interface { .. } => Some(ty),
            _ => None,
        };
        if let Some(interface) = interface
            && let Some(definition) = self.checker.types[interface].definition()
            && let text = name.text(&self.checker.module().ast.file)
            && (self.checker.module_checks[definition.module.index()].scope)
                .get(Some(definition), text)
                .is_none()
        {
            return self.interface_method(value, interface, name).map(Some);
        }
        let place = self.through_pointer(value, self.body.exprs.span(value));
        let ty = self.body.exprs[place].ty;
        if self.checker.types.is_open(ty) && self.checker.types[ty].definition().is_none() {
            return Some(None);
        }
        let Some(definition) = self.checker.types[ty].definition() else {
            if ty != semantic_types::TypeId::INVALID {
                let message = format!("`{}` has no functions", self.checker.type_text(ty));
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, name.span);
            }
            return None;
        };
        let head = self.checker.type_text(ty);
        let symbol = self.checker.member(
            definition.module,
            Some(definition),
            "type",
            &head,
            name.span,
        )?;
        let (callee, signature) = self.function_called(Some(symbol), span)?;
        let Callee::Function(function) = callee else {
            unreachable!("a type's member is a function")
        };
        self.takes_self(function.definition, span)
            .then_some(Some((place, callee, signature)))
    }

    /// The method `name` of `interface` that `value`, an interface or a
    /// reference to one, calls, as `method` gives it: its place in the
    /// interface's table. A method taking `self` whole is called only on an
    /// interface, which it takes.
    fn interface_method(
        &mut self,
        value: Id<thir::Expr>,
        interface: semantic_types::TypeId,
        name: ast::Ident,
    ) -> Option<(Id<thir::Expr>, Callee, semantic_types::TypeId)> {
        let (semantic_types::Type::Named { definition, .. }
        | semantic_types::Type::Interface { definition, .. }) = self.checker.types[interface]
        else {
            unreachable!("an interface reference refers to an interface")
        };
        // Checked first, so the place's type is the interface, not its name.
        if self.checker.declared_type(definition, name.span) == semantic_types::TypeId::INVALID {
            return None;
        }
        let modules = self.checker.modules;
        let file = &modules[definition.module.index()].ast.file;
        let text = name.text(&self.checker.module().ast.file);
        let names = || {
            model::program::interface_methods(modules, definition)
                .expect("an interface declaration has methods")
                .map(|method| method.name.text(file))
        };
        let head = self.checker.type_text(interface);
        let Some(slot) = names().position(|method| method == text) else {
            // Another module's private ones cannot be called from here.
            let elsewhere = definition.module != self.checker.current_module();
            let functions = (self.checker.module_checks[definition.module.index()].scope)
                .symbols(Some(definition))
                .map(|(function, _)| function)
                .filter(|function| !elsewhere || model::program::is_exported(function));
            let meant = scope::nearest(text, names(), functions);
            let message = format!("unknown name `{text}` in interface `{head}`");
            self.checker
                .report_unknown(diagnostics::UNDEFINED_NAME, name.span, message, meant);
            return None;
        };
        let semantic_types::Type::Interface { methods, .. } = &self.checker.types[interface] else {
            unreachable!("a checked interface is `Type::Interface`")
        };
        let signature = methods[slot];
        let semantic_types::Type::Function { parameters, .. } = &self.checker.types[signature]
        else {
            unreachable!("a method's type is a function type")
        };
        let callee = Callee::Method(slot as u32);
        let semantic_types::Type::InterfaceReference { .. } =
            self.checker.types[self.body.exprs[value].ty]
        else {
            return Some((value, callee, signature)); // an interface, the place itself
        };
        if !matches!(
            self.checker.types[parameters[0]],
            semantic_types::Type::InterfaceReference { .. }
        ) {
            let message =
                format!("`{text}` takes `self` whole, so it cannot be called through `&{head}`");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name.span);
            return None;
        }
        let place = self.through_pointer(value, self.body.exprs.span(value));
        Some((place, callee, signature))
    }

    /// Whether the function `function`, called with a dot at `span`, takes
    /// `self` first; reported when it does not.
    fn takes_self(&mut self, function: DefId, span: Span) -> bool {
        let modules = self.checker.modules;
        let tree = &modules[function.module.index()].ast;
        let declared = model::program::function_declaration(modules, function);
        let first = tree
            .children
            .lists
            .items(declared.signature.parameters)
            .next();
        if first.is_some_and(|first| tree.is_self(tree.children.params[first])) {
            return true;
        }
        let path = model::program::declared_path(modules, function);
        let name = declared.name.text(&tree.file);
        let message = format!("`{name}` takes no `self`: call it as `{path}(...)`");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        false
    }
}
