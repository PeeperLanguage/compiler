//! Checking `let` and `const` statements: the variable or constant each
//! declares, and its value.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::constant::Value;
use model::layout::in_memory;
use model::semantic_types::Constant;
use model::semantic_types::Type;
use model::semantic_types::TypeId;

use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;
use syntax::ast;
use syntax::ast::Ident;

use crate::Checked;
use crate::builder::ThirBuilder;
use crate::function::constant_refusal;

impl<L: Checked> ThirBuilder<'_, '_, L> {
    /// `let mut name: ty = value;`. The variable is declared after its value is
    /// checked, so the value cannot use it.
    pub(super) fn check_let_statement(
        &mut self,
        mutable: bool,
        name: Ident,
        ty: Option<Id<ast::Type<L>>>,
        value: Option<Id<ast::Expr<L>>>,
    ) -> Stmt {
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let initialized = value.is_some();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (ty, value.map(|value| self.add_required(value, ty))),
            (None, Some(value)) => {
                let value = self.add_expr(value, None);
                let ty = self.body.exprs[value].ty;
                if ty == TypeId::VOID {
                    let span = self.body.exprs.span(value);
                    let message = "this expression has no value to bind";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    (TypeId::INVALID, None)
                } else {
                    (ty, Some(value))
                }
            }
            (None, None) => {
                let text = name.text(&self.checker.module().ast.file);
                let message = format!("`{text}` needs a type or a value");
                self.checker
                    .error(diagnostics::MISSING_TYPE, message, name.span);
                (TypeId::INVALID, None)
            }
        };
        let declared_variable = Variable {
            ty,
            mutable,
            initialized,
            scope: self.block,
            hidden: false,
        };
        let text = name.text(&self.checker.module().ast.file);
        if let Some(&position) = self.innermost.get(text)
            && position < self.block_start
            && let Local::Variable(outer) = self.names[position].local
            && self.body.variables[outer].mutable
        {
            // Likely meant as `x = ...`: the outer local stays unchanged.
            let file = &self.checker.module().ast.file;
            self.checker.report(
                Diagnostic::new(
                    diagnostics::HIDDEN_MUTABLE,
                    format!("this `let` hides the outer `mut {text}`"),
                )
                .label(
                    file,
                    name.span,
                    format!("a new `{text}`, only inside this block"),
                )
                .label(
                    file,
                    self.body.variables.span(outer),
                    format!("the outer `{text}`, declared `mut` here"),
                )
                .help(
                    format!("to change the outer `{text}`, write `{text} = ...` without `let`"),
                    Vec::new(),
                ),
            );
        }
        let variable = self.body.add(declared_variable, name.span);
        if let Some(value) = value {
            self.body.keep_temporaries(value, variable);
        }
        self.declare(name, Local::Variable(variable));
        Stmt::Let { variable, value }
    }

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives its type and
    /// value, or `None` after an error.
    pub(crate) fn check_const_statement(
        &mut self,
        declaration: Id<ast::Decl<L>>,
    ) -> Option<Constant> {
        let ast::Decl::Const {
            name, ty, value, ..
        } = self.checker.module().ast.decls[declaration]
        else {
            unreachable!("`{declaration:?}` is a const declaration")
        };
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let Some(value) = value else {
            let text = name.text(&self.checker.module().ast.file);
            let message = format!("constant `{text}` needs a value");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name.span);
            return None;
        };
        self.check_constant_value(value, declared)
    }

    /// `value`, of the type `declared` when given, as a value known at
    /// compile time: a constant's, or a `const` parameter's argument
    /// (`copy_called`). Calls in it run now. `None` after an error, or when
    /// it is known only when the program runs, reported.
    pub(crate) fn check_constant_value(
        &mut self,
        value: Id<ast::Expr<L>>,
        declared: Option<TypeId>,
    ) -> Option<Constant> {
        let span = self.checker.module().ast.exprs.span(value);
        let outer = std::mem::replace(&mut self.in_constant, true);
        let checked = match declared {
            Some(ty) => self.check_required(value, ty),
            None => self.check_expr(value, None),
        };
        self.in_constant = outer;
        self.known_constant(checked, span)
    }

    /// `checked`, worked out with `in_constant`, as a constant; reported at
    /// `span` when it is not one.
    fn known_constant(&mut self, checked: Expr, span: Span) -> Option<Constant> {
        match checked.kind {
            ExprKind::Constant(value) => Some(Constant {
                ty: checked.ty,
                value,
            }),
            ExprKind::Invalid => None,
            _ => {
                let file = self.checker.module().ast.file.clone();
                let unknown = self.body.first_named_variable(&checked.kind);
                let types = &self.checker.types;
                let message = if unknown.is_some() {
                    "the value of a constant must be known at compile time"
                } else if in_memory(types, checked.ty)
                    || types[checked.ty].borrows() == Some(true)
                    || matches!(types[checked.ty], Type::RawPointer { .. })
                {
                    constant_refusal(&types[checked.ty])
                } else {
                    "this value cannot be worked out at compile time yet"
                };
                let mut diagnostic =
                    Diagnostic::new(diagnostics::INVALID_OPERATION, message).label(&file, span, "");
                if let Some(variable) = unknown {
                    let declared = self.body.variables.span(variable);
                    let name = file.slice(declared);
                    let label = format!("`{name}` gets its value only when the program runs");
                    diagnostic = diagnostic.label(&file, declared, label);
                }
                self.checker.report(diagnostic);
                None
            }
        }
    }

    /// Whether the constraints of `signature`, the function being checked,
    /// hold (`constraint_value`). `known`: for a copy, or a function with
    /// no type or `const` parameter; in a function's own check, given
    /// nothing (`own_arguments`), one that names what no call gave is
    /// unknown and holds. One false is reported, for a copy at the call it
    /// was made for: each such call has the error.
    pub(crate) fn check_constraints(&mut self, signature: ast::Signature<L>, known: bool) -> bool {
        let copy = (self.checker.internals.declarations_being_checked.last())
            .expect("a function is being checked")
            .copy();
        let module = copy.definition.module.index();
        let constraints: Vec<Id<ast::Constraint<L>>> = (self.checker.module().ast.children.lists)
            .items(signature.constraints)
            .collect();
        let mut holds = true;
        let mut false_ones = Vec::new();
        for constraint in constraints {
            let reported = self.checker.module_checks[module].diagnostics.len();
            match self.constraint_value(constraint) {
                Some(true) => {}
                Some(false) => false_ones.push(constraint),
                None if known => holds = false, // reported
                None => {
                    let found = &self.checker.module_checks[module].diagnostics[reported..];
                    holds &= !found.iter().any(Diagnostic::is_error);
                }
            }
        }
        let checker = &mut *self.checker;
        let function = model::program::declared_path(checker.modules, copy.definition);
        let copies = &checker.internals.function_copies;
        let call = (copies.get(&copy)).map(|copied| (copied.called_in, copied.called_at));
        let made = call.map_or(String::new(), |_| {
            let name = model::program::copy_name(checker.modules, &checker.types, copy);
            format!(" for `{name}`")
        });
        for constraint in false_ones {
            holds = false;
            let tree = &checker.module().ast;
            let span = tree.children.constraints.span(constraint);
            let text = tree.file.slice(span);
            let message = format!("`{function}` needs `{text}`, which is false{made}");
            let mut diagnostic = Diagnostic::new(diagnostics::INVALID_OPERATION, message);
            if let Some((called_in, called_at)) = call {
                let file = &checker.modules[called_in.index()].ast.file;
                diagnostic = diagnostic.label(file, called_at, "");
            }
            // A copy's is moved to the call's module (`report_from_copy`).
            checker.report(diagnostic.label(&tree.file, span, ""));
        }
        holds
    }

    /// Whether `constraint`, after a function's `where`, holds: its subject,
    /// a type, implements the interface its bound names, or the function its
    /// bound names gives `true` when called at compile time with the subject
    /// first, then the constraint's arguments. `None` when that is not
    /// known: after an error, reported, or for a subject only a copy knows.
    fn constraint_value(&mut self, constraint: Id<ast::Constraint<L>>) -> Option<bool> {
        let tree = &self.checker.module().ast;
        let span = tree.children.constraints.span(constraint);
        let ast::Constraint {
            subject,
            bound,
            arguments,
        } = tree.children.constraints[constraint];
        let bound_span = tree.exprs.span(bound);
        let bound_text = tree.file.slice(bound_span);
        let given: Vec<Id<ast::Expr<L>>> = std::iter::once(subject)
            .chain(tree.children.lists.items(arguments))
            .collect();
        let symbol = match tree.exprs[bound] {
            ast::Expr::Path(path) => self.checker.resolve_path(path),
            _ => self.resolve(bound_span),
        }?;
        match symbol {
            Symbol::Function(_) => {
                let outer = std::mem::replace(&mut self.in_constant, true);
                let found = self.function_called(Some(symbol), bound_span);
                let checked = self.call_found(found, None, bound_span, List::EMPTY, &given, span);
                self.in_constant = outer;
                if checked.ty == TypeId::INVALID {
                    return None; // reported, or only a copy knows it
                }
                if checked.ty != TypeId::BOOL {
                    let ty = self.checker.type_text(checked.ty);
                    let message =
                        format!("`{bound_text}` gives `{ty}`, but a constraint needs `bool`");
                    self.checker
                        .error(diagnostics::TYPE_MISMATCH, message, bound_span);
                    return None;
                }
                match self.known_constant(checked, span)?.value {
                    Value::Bool(holds) => Some(holds),
                    _ => unreachable!("a `bool` constant is a `Value::Bool`"),
                }
            }
            Symbol::Type(definition) => {
                let interface = self.checker.declared_type(definition, bound_span);
                if interface == TypeId::INVALID {
                    return None; // reported
                }
                if !self.checker.is_interface(interface) {
                    let message = format!("`{bound_text}` is not an interface or a function");
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, bound_span);
                    return None;
                }
                if !arguments.is_empty() {
                    let message =
                        format!("`{bound_text}` is an interface, which takes no arguments");
                    self.checker
                        .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
                    return None;
                }
                let ty = self.checker.resolve_type_argument(subject);
                if ty == TypeId::INVALID {
                    return None; // reported, or only a copy knows it
                }
                if self.checker.types[ty].members().is_none() {
                    return Some(false);
                }
                // `None` after reporting what the type lacks.
                self.checker
                    .implementation(ty, interface, span)
                    .map(|_| true)
            }
            _ => {
                let message = format!("`{bound_text}` is not an interface or a function");
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, bound_span);
                None
            }
        }
    }
}
