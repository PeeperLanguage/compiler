//! Checking names, paths and calls: what a name stands for as a value, a
//! call's arguments, and `print`.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use model::Callee;
use model::DefId;
use model::FunctionCopy;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::constant::Value;
use model::layout::layout;
use model::semantic_types::ConstArgument;
use model::semantic_types::ConstArguments;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Symbol;
use rustc_hash::FxHashSet;
use source::Span;

use crate::builder::ThirBuilder;
use crate::builtin::LayoutFact;
use crate::builtin::layout_function;
use crate::declaration::const_parameter;
use crate::declaration::is_self;
use crate::function::function_declaration;
use crate::scope;

impl ThirBuilder<'_, '_> {
    /// `print(values)`, or `println(values)` when `newline`: each value of a
    /// type `printable` takes. A plain string literal is printed as a `&cstr`,
    /// though it is not one anywhere else.
    // shortcut: a built-in until a library function takes any number of
    // values of any printable type; then delete it with THIR's and MIR's
    // `Print`.
    pub(super) fn check_print(&mut self, newline: bool, values: List<ast::Expr>) -> Expr {
        let tree = &self.checker.module().ast;
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(values.len());
        for value in tree.children.lists.items(values) {
            let span = tree.exprs.span(value);
            let expr = self.check_expr(value, None);
            let ty = expr.ty;
            let types = &self.checker.types;
            // On an error the others are still checked, for their own.
            if !printable(types, ty) {
                if ty != TypeId::INVALID {
                    let message = format!("`{}` cannot be printed", self.checker.type_text(ty));
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                }
                continue;
            }
            let mut printed = self.body.add(expr, span);
            // Printing reads a value, never takes it: one that moves, in a
            // place, is lent instead, a `&mut` reference as `&`.
            if !types.is_copy(ty) && self.start_variable(self.place_root(printed).0).is_some() {
                let lent = match types[ty] {
                    Type::Reference { target, .. } => {
                        let reference = self.body.exprs[printed].clone();
                        let shared = self.checker.reference_type(false, target, span);
                        self.reborrow(reference, span, shared)
                    }
                    _ => self.check_borrow(false, printed, span),
                };
                printed = self.body.add(lent, span);
            }
            checked.push(printed.into());
        }
        if checked.len() < values.len() {
            return Expr::INVALID;
        }
        Expr {
            ty: TypeId::VOID,
            kind: ExprKind::Print {
                newline,
                values: self.body.children.lists.add(&checked),
            },
        }
    }

    /// The name or path written at `span`, which stands for `symbol`, as a
    /// value. `None` when finding the symbol reported an error.
    pub(super) fn check_symbol_value(&mut self, symbol: Option<Symbol>, span: Span) -> Expr {
        let what = match symbol {
            None => return Expr::INVALID, // finding it reported the error
            Some(Symbol::Local(Local::Variable(variable))) => {
                return Expr {
                    ty: self.body.variables[variable].ty,
                    kind: ExprKind::Variable(variable),
                };
            }
            Some(Symbol::Local(Local::Constant(constant))) => {
                return constant_expr(self.body.constants[constant].clone());
            }
            Some(Symbol::Constant(constant)) => {
                return constant_expr(self.checker.constant(constant, span));
            }
            Some(Symbol::Function(_)) => {
                self.checker.not_supported(span, "functions as values");
                return Expr::INVALID;
            }
            Some(Symbol::Type(_) | Symbol::Builtin(_) | Symbol::TypeArgument(_)) => "a type",
            Some(Symbol::Module(_)) => "a module",
        };
        let text = self.checker.module().ast.file.slice(span);
        let message = format!("`{text}` is {what}, not a value");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        Expr::INVALID
    }

    /// `size_of(T)` or `align_of(T)`, written at `span`, which gives `fact` of
    /// the type `T`: a `usize` constant, for the target being built.
    fn check_layout_function(
        &mut self,
        fact: LayoutFact,
        text: &str,
        arguments: List<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let mut given = tree.children.lists.items(arguments);
        let (Some(argument), None) = (given.next(), given.next()) else {
            let message = argument_count_message(text, 1, false, arguments.len());
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
            return Expr::INVALID;
        };
        // A variable is a value; a type of its name is still the type.
        let written = tree.exprs.span(argument);
        if let ast::Expr::Name = tree.exprs[argument]
            && let name = tree.file.slice(written)
            && let Some(&position) = self.innermost.get(name)
            && self.checker.resolve_global(name).is_none()
        {
            let local = Symbol::Local(self.names[position].local);
            self.checker.type_named_by(local, written);
            return Expr::INVALID;
        }
        let ty = self.checker.resolve_type_argument(argument);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let (size, align) = layout(&self.checker.types, ty, self.checker.target);
        let bits = self.checker.target.pointer_bits;
        let value = match fact {
            LayoutFact::Size => size,
            LayoutFact::Align => align,
        };
        Expr {
            ty: self.checker.types.usize,
            kind: ExprKind::Constant(Value::Integer(Limbs::new(bits, &[value]))),
        }
    }

    /// `callee(arguments)`, written at `span`. Only a module's function, by
    /// its name or its path, can be called, or with a dot, `x.name(arguments)`,
    /// a type's function or an interface reference's method, which gets `x`
    /// as its `self`. A `...` parameter takes all the arguments past the
    /// others (`check_variadic`). A function with `const` parameters is
    /// called as its copy for what is written in their places
    /// (`copy_called`).
    pub(super) fn check_call(
        &mut self,
        callee: Id<ast::Expr>,
        arguments: List<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let callee_span = tree.exprs.span(callee);
        let text = tree.file.slice(callee_span);
        let mut receiver = None;
        let found = match tree.exprs[callee] {
            // A built-in function, which no declaration hides.
            ast::Expr::Name if let Some(fact) = layout_function(text) => {
                return self.check_layout_function(fact, text, arguments, span);
            }
            ast::Expr::Name => {
                let symbol = match self.resolve(callee_span) {
                    // Their errors were reported.
                    Some(Symbol::Local(Local::Constant(constant)))
                        if self.body.constants[constant].is_none() =>
                    {
                        None
                    }
                    Some(Symbol::Local(Local::Variable(variable)))
                        if self.body.variables[variable].ty == TypeId::INVALID =>
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
            ast::Expr::Selector { value, field } => {
                self.method(value, field, callee_span)
                    .map(|(place, callee, signature)| {
                        receiver = Some(place);
                        (callee, signature)
                    })
            }
            _ => {
                self.checker
                    .not_supported(callee_span, "calls of computed functions");
                None
            }
        };
        // The written arguments come after the receiver's.
        let first = usize::from(receiver.is_some());
        // The places among the written arguments of what a copy is for.
        let mut const_places = Vec::new();
        let found = match found {
            Some((Callee::Function(function), _))
                if let definition = function.definition
                    && let declared = function_declaration(self.checker.modules, definition)
                    && let tree = &self.checker.modules[definition.module.index()].ast
                    && const_parameter(tree, declared.signature).is_some() =>
            {
                self.copy_called(definition, arguments, first, &mut const_places, span)
            }
            found => found,
        };
        let not_const = |&(place, _): &(usize, Id<ast::Expr>)| !const_places.contains(&place);
        let signature = found.map(|(callee, ty)| (callee, ty, self.checker.types[ty].clone()));
        let Some((
            callee,
            signature,
            Type::Function {
                parameters,
                result,
                variadic,
                ..
            },
        )) = signature
        else {
            let written = tree.children.lists.items(arguments).enumerate();
            for (_, argument) in written.filter(not_const) {
                let argument = match tree.exprs[argument] {
                    ast::Expr::Spread(values) => values,
                    _ => argument,
                };
                self.add_expr(argument, None); // for its own errors
            }
            return Expr::INVALID;
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
            let message = argument_count_message(text, all, variadic, arguments.len());
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(parameters.len());
        if let Some(place) = receiver {
            // `self` takes the value itself; `&self` and `&mut self` borrow it.
            let argument = match self.checker.types[parameters[0]] {
                Type::Reference { mutable, .. } | Type::InterfaceReference { mutable, .. } => {
                    let span = self.body.exprs.span(place);
                    let borrowed = self.check_borrow(mutable, place, span);
                    self.body.add(borrowed, span)
                }
                _ => place,
            };
            checked.push(argument.into());
        }
        let written = tree.children.lists.items(arguments).enumerate();
        let mut given = written.filter(not_const).map(|(_, argument)| argument);
        for (&parameter, argument) in parameters[first..first + fixed].iter().zip(given.by_ref()) {
            let argument = self.add_required(argument, parameter);
            self.reborrow_in_place(argument);
            checked.push(argument.into());
        }
        if variadic {
            let rest: Vec<Id<ast::Expr>> = given.collect();
            let slice = self.check_variadic(&rest, parameters[first + fixed], span);
            self.reborrow_in_place(slice);
            checked.push(slice.into());
        } else {
            for extra in given {
                self.add_expr(extra, None); // for its own errors
            }
        }
        if !arity_matches {
            return Expr::INVALID;
        }
        let arguments = self.body.children.lists.add(&checked);
        // In a constant's value, a call on constants runs now.
        let constant_arguments = || {
            let values = self.body.children.lists.items(arguments).map(|argument| {
                match &self.body.exprs[argument].kind {
                    ExprKind::Constant(value) => Some(value.clone()),
                    _ => None,
                }
            });
            values.collect::<Option<Vec<Value>>>()
        };
        if let Callee::Function(function) = callee
            && self.in_constant
            && result != TypeId::VOID
            && let Some(values) = constant_arguments()
        {
            return match self.checker.evaluate_call(function, &values, span) {
                Some(value) => Expr {
                    ty: result,
                    kind: ExprKind::Constant(value),
                },
                None => Expr::INVALID,
            };
        }
        Expr {
            ty: result,
            kind: ExprKind::Call {
                callee,
                signature,
                arguments,
            },
        }
    }

    /// What the function `symbol`, called at `span`, is: its callee and
    /// type. `None` when it is not a function, reported, or when finding it
    /// reported an error.
    fn function_called(&mut self, symbol: Option<Symbol>, span: Span) -> Option<(Callee, TypeId)> {
        match symbol {
            Some(Symbol::Function(definition)) => {
                let ty = self.checker.function_type(definition, span);
                let function = FunctionCopy {
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

    /// The function `name` of the struct `value` is or points to, or the
    /// method `name` of the interface or interface reference `value` is,
    /// called with a dot written at `span`: the place of the struct, of the
    /// interface, or of what the interface reference views, which the call
    /// gives as `self`, the callee, and its type.
    fn method(
        &mut self,
        value: Id<ast::Expr>,
        name: Ident,
        span: Span,
    ) -> Option<(Id<Expr>, Callee, TypeId)> {
        let value = self.add_expr(value, None);
        let ty = self.body.exprs[value].ty;
        match self.checker.types[ty] {
            Type::InterfaceReference { interface, .. } => {
                return self.interface_method(value, interface, name);
            }
            Type::Interface { .. } => return self.interface_method(value, ty, name),
            _ => {}
        }
        let place = self.through_pointer(value, self.body.exprs.span(value));
        let ty = self.body.exprs[place].ty;
        let Type::Struct { definition, .. } = self.checker.types[ty] else {
            if ty != TypeId::INVALID {
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
            .then_some((place, callee, signature))
    }

    /// The copy of the function `definition` that its call at `span` with
    /// the written `arguments`, after `first` of the receiver, asks for,
    /// and its type. The argument in the place of each `const` parameter,
    /// put in `const_places`, is what the copy is made for: a type for
    /// `const T: type` (`resolve_type_argument`), else a value of the
    /// parameter's type known when compiling. When one is missing, the
    /// function itself and its own type, for `check_call` to report the
    /// count. `None` when one has an error, or the function's signature
    /// has one, reported.
    fn copy_called(
        &mut self,
        definition: DefId,
        arguments: List<ast::Expr>,
        first: usize,
        const_places: &mut Vec<usize>,
        span: Span,
    ) -> Option<(Callee, TypeId)> {
        let modules = self.checker.modules;
        let tree = &modules[definition.module.index()].ast;
        let declared = function_declaration(modules, definition);
        let parameters = tree.children.lists.items(declared.signature.parameters);
        // `self` is never `const`, so a `const` parameter is past the receiver.
        let const_parameters: Vec<(usize, ast::Param)> = (parameters.enumerate())
            .map(|(place, parameter)| (place, tree.children.params[parameter]))
            .filter(|(_, parameter)| parameter.constant)
            .map(|(place, parameter)| (place - first, parameter))
            .collect();
        const_places.extend(const_parameters.iter().map(|&(place, _)| place));
        let written: Vec<Id<ast::Expr>> =
            (self.checker.module().ast.children.lists.items(arguments)).collect();
        let pattern = self.checker.function_type(definition, span);
        if pattern == TypeId::INVALID {
            return None; // reported
        }
        // Too few arguments: `check_call` reports it, by the function's own type.
        if const_parameters
            .last()
            .is_some_and(|&(place, _)| place >= written.len())
        {
            let function = FunctionCopy {
                definition,
                arguments: ConstArguments::NONE,
            };
            return Some((Callee::Function(function), pattern));
        }
        let mut given = Vec::with_capacity(const_parameters.len());
        let mut failed = false;
        for (place, parameter) in const_parameters {
            let argument = written[place];
            if let ast::Type::Type = tree.types[parameter.ty] {
                let ty = self.checker.resolve_type_argument(argument);
                failed |= ty == TypeId::INVALID;
                given.push(ConstArgument::Type(self.checker.types.real(ty)));
                continue;
            }
            let ty = self
                .checker
                .const_value_type(definition, parameter.ty, span);
            match self.check_constant_value(argument, Some(ty)) {
                Some(constant) => given.push(ConstArgument::Value(constant)),
                None => failed = true, // reported
            }
        }
        if failed {
            return None;
        }
        let copy = FunctionCopy {
            definition,
            arguments: self.checker.types.const_arguments(&given),
        };
        let ty = self.checker.copy_type(copy, span);
        Some((Callee::Function(copy), ty))
    }

    /// The method `name` of `interface` that `value`, an interface or a
    /// reference to one, calls, as `method` gives it: its place in the
    /// interface's table. A method taking `self` whole is called only on an
    /// interface, which it takes.
    fn interface_method(
        &mut self,
        value: Id<Expr>,
        interface: TypeId,
        name: Ident,
    ) -> Option<(Id<Expr>, Callee, TypeId)> {
        let (Type::Named(definition) | Type::Interface { definition, .. }) =
            self.checker.types[interface]
        else {
            unreachable!("an interface reference refers to an interface")
        };
        // Checked first, so the place's type is the interface, not its name.
        if self.checker.declared_type(definition, name.span) == TypeId::INVALID {
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
            let meant = scope::nearest(text, std::iter::empty(), names());
            let message = format!("unknown name `{text}` in interface `{head}`");
            self.checker
                .report_unknown(diagnostics::UNDEFINED_NAME, name.span, message, meant);
            return None;
        };
        let Type::Interface { methods, .. } = &self.checker.types[interface] else {
            unreachable!("a checked interface is `Type::Interface`")
        };
        let signature = methods[slot];
        let Type::Function { parameters, .. } = &self.checker.types[signature] else {
            unreachable!("a method's type is a function type")
        };
        let callee = Callee::Method(slot as u32);
        let Type::InterfaceReference { .. } = self.checker.types[self.body.exprs[value].ty] else {
            return Some((value, callee, signature)); // an interface, the place itself
        };
        if !matches!(
            self.checker.types[parameters[0]],
            Type::InterfaceReference { .. }
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
        let declared = function_declaration(modules, function);
        let first = tree
            .children
            .lists
            .items(declared.signature.parameters)
            .next();
        if first.is_some_and(|first| is_self(tree, tree.children.params[first])) {
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

/// The value of a constant (`Checker::constant`) as an expression.
fn constant_expr(constant: Option<Constant>) -> Expr {
    match constant {
        Some(Constant { ty, value }) => Expr {
            ty,
            kind: ExprKind::Constant(value),
        },
        None => Expr::INVALID,
    }
}

/// Whether `print` writes a value of the type `ty`: a number, `bool`,
/// `&cstr` or raw pointer, a struct or array of such values, or a reference
/// to one.
// shortcut: structs and arrays print built in until a trait or interface
// lets each type say how it prints; then this rule and the backend's struct
// and array printing (`llvm` `write_printed`, `printer`) go.
fn printable(types: &Types, ty: TypeId) -> bool {
    // A list, not recursion, with each struct looked at once: structs hold
    // structs as deep as the declarations nest, and can hold one many times.
    let (mut reached, mut pending) = (FxHashSet::default(), vec![ty]);
    while let Some(ty) = pending.pop() {
        match &types[ty] {
            Type::Integer(_) | Type::Float(_) | Type::Bool | Type::CString | Type::Str => {}
            Type::RawPointer { .. } => {}
            Type::Reference { target, .. } => pending.push(*target),
            Type::Struct { fields, .. } => {
                if reached.insert(ty) {
                    pending.extend(fields);
                }
            }
            Type::Array { internals, .. } => pending.push(internals.base),
            _ => return false,
        }
    }
    true
}

/// Why a call of `callee` with `given` arguments is wrong, when it takes
/// `fixed` of them, or at least that many when `variadic`.
fn argument_count_message(callee: &str, fixed: usize, variadic: bool, given: usize) -> String {
    format!(
        "`{callee}` takes {}{fixed} argument{}, but {given} {} given",
        if variadic { "at least " } else { "" },
        if fixed == 1 { "" } else { "s" },
        if given == 1 { "was" } else { "were" },
    )
}
