//! Checking names, paths and calls: what a name stands for as a value, a
//! call's arguments, and `print`.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use model::DefId;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::constant::Value;
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
use crate::declaration::is_self;

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
            Some(Symbol::Type(_) | Symbol::Builtin(_)) => "a type",
            Some(Symbol::Module(_)) => "a module",
        };
        let text = self.checker.module().ast.file.slice(span);
        let message = format!("`{text}` is {what}, not a value");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        Expr::INVALID
    }

    /// `callee(arguments)`, written at `span`. Only a module's function, by
    /// its name or its path, can be called, or a type's function with a dot,
    /// `x.name(arguments)`, which gives `x` as its `self`. A `...` parameter
    /// takes all the arguments past the others (`check_variadic`).
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
        let symbol = match tree.exprs[callee] {
            ast::Expr::Name => match self.resolve(callee_span) {
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
            },
            ast::Expr::Path(path) => self.checker.resolve_path(path),
            ast::Expr::Selector { value, field } => {
                let method = self.method(value, field);
                receiver = method.map(|(place, _)| place);
                method.map(|(_, symbol)| symbol)
            }
            _ => {
                self.checker
                    .not_supported(callee_span, "calls of computed functions");
                None
            }
        };
        let function = match symbol {
            Some(Symbol::Function(function)) => Some(function),
            Some(_) => {
                let message = format!("`{text}` is not a function");
                self.checker
                    .error(diagnostics::NOT_CALLABLE, message, callee_span);
                None
            }
            None => None,
        };
        let signature = function.map(|function| {
            let ty = self.checker.function_type(function, callee_span);
            (function, ty, self.checker.types[ty].clone())
        });
        let signature = signature
            .filter(|&(function, ..)| receiver.is_none() || self.takes_self(function, callee_span));
        let Some((
            function,
            signature,
            Type::Function {
                parameters,
                result,
                variadic,
                ..
            },
        )) = signature
        else {
            for argument in tree.children.lists.items(arguments) {
                let argument = match tree.exprs[argument] {
                    ast::Expr::Spread(values) => values,
                    _ => argument,
                };
                self.add_expr(argument, None); // for its own errors
            }
            return Expr::INVALID;
        };
        // The written arguments come after the receiver's.
        let first = usize::from(receiver.is_some());
        let fixed = parameters.len() - usize::from(variadic) - first;
        let arity_matches = if variadic {
            arguments.len() >= fixed
        } else {
            arguments.len() == fixed
        };
        if !arity_matches {
            let message = format!(
                "`{text}` takes {}{fixed} argument{}, but {} {} given",
                if variadic { "at least " } else { "" },
                if fixed == 1 { "" } else { "s" },
                arguments.len(),
                if arguments.len() == 1 { "was" } else { "were" },
            );
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(parameters.len());
        if let Some(place) = receiver {
            // `self` takes the value itself; `&self` and `&mut self` borrow it.
            let argument = match self.checker.types[parameters[0]] {
                Type::Reference { mutable, .. } => {
                    let span = self.body.exprs.span(place);
                    let borrowed = self.check_borrow(mutable, place, span);
                    self.body.add(borrowed, span)
                }
                _ => place,
            };
            checked.push(argument.into());
        }
        let mut given = tree.children.lists.items(arguments);
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
                match self.body.exprs[argument].kind {
                    ExprKind::Constant(ref value) => Some(value.clone()),
                    _ => None,
                }
            });
            values.collect::<Option<Vec<Value>>>()
        };
        if self.in_constant
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
                function,
                signature,
                arguments,
            },
        }
    }

    /// The function `name` of the struct `value` is or points to, called with
    /// a dot, and the place of that struct, which the call gives as `self`.
    fn method(&mut self, value: Id<ast::Expr>, name: Ident) -> Option<(Id<Expr>, Symbol)> {
        let value = self.add_expr(value, None);
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
        Some((place, symbol))
    }

    /// Whether the function `function`, called with a dot at `span`, takes
    /// `self` first; reported when it does not.
    fn takes_self(&mut self, function: DefId, span: Span) -> bool {
        let modules = self.checker.modules;
        let tree = &modules[function.module.index()].ast;
        let ast::Decl::Function(declared) = project::declaration(modules, function) else {
            unreachable!("`{function:?}` is a function declaration")
        };
        let first = tree
            .children
            .lists
            .items(declared.signature.parameters)
            .next();
        if first.is_some_and(|first| is_self(tree, tree.children.params[first])) {
            return true;
        }
        let path = project::declared_path(modules, function);
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
        match types[ty] {
            Type::Integer(_) | Type::Float(_) | Type::Bool | Type::CString | Type::Str => {}
            Type::RawPointer { .. } => {}
            Type::Reference { target, .. } => pending.push(target),
            Type::Struct { ref fields, .. } => {
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
