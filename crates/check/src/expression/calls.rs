//! Checking names, paths and calls: what a name stands for as a value, a
//! call's arguments, and `print`.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ast;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Symbol;
use source::Span;

use crate::builder::ThirBuilder;
use crate::builtin::UNSUPPORTED_FUNCTIONS;
use crate::expression::TypeExpectation;
use crate::scope;

impl ThirBuilder<'_, '_> {
    /// `print(values)`, or `println(values)` when `newline`: each value of a
    /// type `printable` takes. A plain string literal is printed as a `cstr`,
    /// though it is not one anywhere else.
    // shortcut: a built-in until a library function takes any number of
    // values of any printable type; then delete it with THIR's and MIR's
    // `Print`.
    pub(super) fn check_print(&mut self, newline: bool, values: List<ast::Expr>) -> Expr {
        let tree = &self.checker.module().ast;
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(values.len());
        for value in tree.children.lists.items(values) {
            let span = tree.exprs.span(value);
            let expr = match tree.exprs[value] {
                ast::Expr::String { c_string: false } => self.c_string(span),
                _ => self.check_expr(value, TypeExpectation::Hint(None)),
            };
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
            if !types.is_copy(ty) && self.place_root(printed).is_some() {
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

    /// Reports `name`, written at `span`, which nothing in scope declares:
    /// a built-in function, not supported yet, or an unknown name, with the
    /// nearest name that is declared.
    pub(crate) fn report_undeclared(&mut self, name: &str, span: Span) {
        if UNSUPPORTED_FUNCTIONS.contains(&name) {
            self.checker.not_supported(span, "built-in functions");
            return;
        }
        let locals = self.names.iter().map(|&(declared, _)| declared);
        let globals = self.checker.visible_symbols().map(|(declared, _)| declared);
        let meant = scope::nearest(name, locals, globals);
        let message = format!("unknown name `{name}`");
        self.checker
            .report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
    }

    /// What the path `module::name` written as the type expression `path`
    /// stands for, reporting a path that names nothing or is not supported.
    /// Another module's name is reachable only if it starts with an upper case
    /// letter, except in the prelude.
    pub(super) fn resolve_path(&mut self, path: Id<ast::Type>) -> Option<Symbol> {
        let tree = &self.checker.module().ast;
        let span = tree.types.span(path);
        // A `Type::Generic` alone, as `Box<T>`, is one segment.
        let segments: Vec<Id<ast::Type>> = match tree.types[path] {
            ast::Type::Path(segments) => tree.children.lists.items(segments).collect(),
            _ => vec![path],
        };
        if !segments
            .iter()
            .all(|&segment| matches!(tree.types[segment], ast::Type::Name))
        {
            self.checker
                .not_supported(span, "paths with type arguments");
            return None;
        }
        let [module, member] = segments[..] else {
            self.checker
                .not_supported(span, "paths of more than two names");
            return None;
        };
        let (module_span, member_span) = (tree.types.span(module), tree.types.span(member));
        let (module_name, member_name) =
            (tree.file.slice(module_span), tree.file.slice(member_span));
        let imported = match self.checker.global(module_name) {
            Some(symbol @ Symbol::Module(imported)) => {
                self.checker.record(module_span, FactKind::Name(symbol));
                imported
            }
            Some(Symbol::Type(_)) => {
                self.checker.not_supported(span, "paths into types");
                return None;
            }
            Some(_) => {
                let message = format!("`{module_name}` is not a module");
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, module_span);
                return None;
            }
            None => {
                self.report_undeclared(module_name, module_span);
                return None;
            }
        };
        let members = &self.checker.module_checks[imported.index()].scope;
        let Some(symbol) = members.get(member_name) else {
            let exported = members.symbols().map(|(declared, _)| declared);
            let meant = scope::nearest(
                member_name,
                std::iter::empty(),
                exported.filter(|declared| scope::is_exported(declared)),
            );
            let message = format!("unknown name `{member_name}` in module `{module_name}`");
            self.checker
                .report_unknown(diagnostics::UNDEFINED_NAME, member_span, message, meant);
            return None;
        };
        self.checker.record(member_span, FactKind::Name(symbol));
        if !scope::is_exported(member_name) {
            let file = &tree.file;
            self.checker.report(
                Diagnostic::new(
                    diagnostics::NOT_EXPORTED,
                    format!("`{member_name}` is not exported from `{module_name}`"),
                )
                .label(file, member_span, "")
                .note(
                    "names that start with an upper case letter are exported; others are private",
                ),
            );
            return None;
        }
        Some(symbol)
    }

    /// `callee(arguments)`, written at `span`. Only a module's function, by
    /// its name or its path, can be called.
    pub(super) fn check_call(
        &mut self,
        callee: Id<ast::Expr>,
        arguments: List<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let callee_span = tree.exprs.span(callee);
        let text = tree.file.slice(callee_span);
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
            ast::Expr::Path(path) => self.resolve_path(path),
            ast::Expr::Selector { .. } => {
                self.checker.not_supported(callee_span, "method calls");
                None
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
            (function, self.checker.types[ty].clone())
        });
        let Some((
            function,
            Type::Function {
                parameters, result, ..
            },
        )) = signature
        else {
            for argument in tree.children.lists.items(arguments) {
                self.add_expr(argument, TypeExpectation::Hint(None)); // for its own errors
            }
            return Expr::INVALID;
        };
        let arity_matches = parameters.len() == arguments.len();
        if !arity_matches {
            let message = format!(
                "`{text}` takes {} argument{}, but {} {} given",
                parameters.len(),
                if parameters.len() == 1 { "" } else { "s" },
                arguments.len(),
                if arguments.len() == 1 { "was" } else { "were" },
            );
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(arguments.len());
        for (index, argument) in tree.children.lists.items(arguments).enumerate() {
            let argument = match parameters.get(index) {
                Some(&parameter) => self.add_expr(argument, TypeExpectation::Required(parameter)),
                None => self.add_expr(argument, TypeExpectation::Hint(None)),
            };
            self.lend_variable_again(argument);
            checked.push(argument.into());
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
                arguments,
            },
        }
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
/// `cstr` or raw pointer, a struct or array of such values, or a reference
/// to one.
// shortcut: structs and arrays print built in until a trait or interface
// lets each type say how it prints; then this rule and the backend's struct
// and array printing (`llvm` `write_printed`, `array_printer`) go.
fn printable(types: &Types, ty: TypeId) -> bool {
    match types[ty] {
        Type::Integer(_) | Type::Float(_) | Type::Bool | Type::CString => true,
        Type::RawPointer { .. } => true,
        Type::Reference { target, .. } => printable(types, target),
        Type::Struct { ref fields, .. } => fields.iter().all(|&field| printable(types, field)),
        Type::Array { element, .. } => printable(types, element),
        _ => false,
    }
}
