//! A function's constraints, after `where`: what its type parameters and
//! `const` values must satisfy. They are part of its signature, so they are
//! asked wherever a signature is checked: a function's own, a copy's, an
//! interface method's, with or without a body.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::constant;
use model::semantic_types;
use model::thir;
use tree::ast;

use crate::Checker;
use crate::builder;

impl Checker<'_> {
    /// Whether the constraints of `signature`, after `where`, hold for what
    /// is being checked, whose parameters are of the types `parameters`
    /// (`constraint_value`). Its `const` values are declared as in a body.
    /// In a function's own check, given nothing (`own_arguments`), one that
    /// names what no call gave is unknown and holds. One false is reported,
    /// for a copy at the call it was made for: each such call has the error.
    pub(crate) fn constraints_hold(
        &mut self,
        signature: ast::Signature,
        parameters: &[semantic_types::TypeId],
    ) -> bool {
        let checking = (self.internals.declarations_being_checked.last())
            .expect("a declaration is being checked");
        let (arguments, copy) = (checking.arguments, checking.copy());
        let known = self.in_copy() || arguments == ConstArguments::NONE;
        let module = copy.definition.module.index();
        let tree = &self.module().ast;
        let constraints: Vec<Id<ast::Constraint>> =
            tree.children.lists.items(signature.constraints).collect();
        let span = tree.children.constraints.span(constraints[0]);
        let mut body = thir::Body::default();
        let mut builder = builder::ThirBuilder::new(self, &mut body, semantic_types::TypeId::VOID);
        let block = thir::Block {
            parent: None,
            statements: List::EMPTY,
        };
        let scope = builder.body.add(block, span);
        builder.declare_parameters(signature, arguments, parameters, scope);
        let mut holds = true;
        let mut false_ones = Vec::new();
        for constraint in constraints {
            let reported = builder.checker.module_checks[module].diagnostics.len();
            match builder.constraint_value(constraint) {
                Some(true) => {}
                Some(false) => false_ones.push(constraint),
                None if known => holds = false, // reported
                None => {
                    let found = &builder.checker.module_checks[module].diagnostics[reported..];
                    holds &= !found.iter().any(Diagnostic::is_error);
                }
            }
        }
        let used =
            (self.internals.copies.get(&copy)).map(|copied| (copied.used_in, copied.used_at));
        let made = used.map_or(String::new(), |_| {
            let name = model::program::copy_name(self.modules, &self.types, copy);
            format!(" for `{name}`")
        });
        for constraint in false_ones {
            holds = false;
            let tree = &self.module().ast;
            let span = tree.children.constraints.span(constraint);
            let text = tree.file.slice(span);
            let message = format!("`{text}` is false{made}");
            let mut diagnostic = Diagnostic::new(diagnostics::INVALID_OPERATION, message);
            if let Some((used_in, used_at)) = used {
                let file = &self.modules[used_in.index()].ast.file;
                diagnostic = diagnostic.label(file, used_at, "");
            }
            // A copy's is moved to the use's module (`report_from_copy`).
            self.report(diagnostic.label(&tree.file, span, ""));
        }
        holds
    }
}

impl builder::ThirBuilder<'_, '_> {
    /// Whether `constraint`, after a function's `where`, holds: its subject,
    /// a type, implements the interface its bound names, or the function its
    /// bound names gives `true` when called at compile time with the subject
    /// first, then the constraint's arguments. `None` when that is not
    /// known: after an error, reported, or for a subject only a copy knows.
    fn constraint_value(&mut self, constraint: Id<ast::Constraint>) -> Option<bool> {
        let tree = &self.checker.module().ast;
        let span = tree.children.constraints.span(constraint);
        let ast::Constraint {
            subject,
            bound,
            arguments,
        } = tree.children.constraints[constraint];
        let bound_span = tree.exprs.span(bound);
        let bound_text = tree.file.slice(bound_span);
        let given: Vec<Id<ast::Expr>> = std::iter::once(subject)
            .chain(tree.children.lists.items(arguments))
            .collect();
        let symbol = match tree.exprs[bound] {
            ast::Expr::Path(path) => self.checker.resolve_path(path),
            _ => self.resolve(bound_span),
        }?;
        match symbol {
            thir::Symbol::Function(_) => {
                let outer = std::mem::replace(&mut self.in_constant, true);
                let found = self.function_called(Some(symbol), bound_span);
                let checked = self.call_found(found, None, bound_span, List::EMPTY, &given, span);
                self.in_constant = outer;
                if checked.ty == semantic_types::TypeId::INVALID {
                    return None; // reported, or only a copy knows it
                }
                if checked.ty != semantic_types::TypeId::BOOL {
                    let ty = self.checker.type_text(checked.ty);
                    let message =
                        format!("`{bound_text}` gives `{ty}`, but a constraint needs `bool`");
                    self.checker
                        .error(diagnostics::TYPE_MISMATCH, message, bound_span);
                    return None;
                }
                match self.known_constant(checked, span)?.value {
                    constant::Value::Bool(holds) => Some(holds),
                    _ => unreachable!("a `bool` constant is a `Value::Bool`"),
                }
            }
            thir::Symbol::Type(definition) => {
                let interface = self.checker.declared_type(definition, bound_span);
                if interface == semantic_types::TypeId::INVALID {
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
                let ty = self.written_type_argument(subject);
                if ty == semantic_types::TypeId::INVALID {
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
