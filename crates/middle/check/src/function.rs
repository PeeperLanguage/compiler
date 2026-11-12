//! A function's body built to checked MIR (THIR, lowering, the path checks),
//! and calls run at compile time on it (`evaluate_call`).

use std::sync::Arc;

use diagnostics::Diagnostic;
use eval::interpreter::Failure;
use model::DefId;
use model::FunctionCopy;
use model::ModuleId;
use model::ast;
use model::constant::Value;
use model::mir;
use model::semantic_types::ConstArguments;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use rustc_hash::FxHashSet;
use source::Span;

use crate::COPY_DEPTH_LIMIT;
use crate::Checker;
use crate::Checking;
use crate::Copied;

/// How far the body of a function is built (`Checker::build_function`),
/// once it is.
pub(crate) enum Built {
    /// Built once, so what its checks found is reported.
    Reported,
    /// Built for a run at compile time and kept for the next run: its MIR
    /// of core concepts, `None` when it has no body or had an error.
    Kept(Option<Box<mir::Body>>),
}

impl Checker<'_> {
    /// Builds the body of the function `copy`, asked for at `used_at`, into
    /// `thir`, only to run it at compile time when `to_run`: checks it
    /// (`check_body`), lowers it to MIR and runs the path checks. `None` when
    /// it has no body or its signature had an error; else its MIR, `None`
    /// when it had an error, naming the program's debug types
    /// (`debug_types`). What it finds is reported by its first build only:
    /// a later one (the compile's, after a run at compile time, or the other
    /// way round) finds the same and drops it.
    pub fn build_function(
        &mut self,
        copy: FunctionCopy,
        used_at: Span,
        thir: &mut thir::Body,
        to_run: bool,
    ) -> Option<Option<mir::Body>> {
        let module = copy.definition.module.index();
        let ty = self.copy_type(copy, used_at);
        // Decided now: a run at compile time inside this build builds it too.
        let first = !self.internals.built_bodies.contains_key(&copy);
        if first {
            self.internals.built_bodies.insert(copy, Built::Reported);
        }
        let reported = self.module_checks[module].diagnostics.len();
        let checked = self.check_body(copy, used_at, thir, to_run);
        let mir = checked.then(|| self.lower_function(copy.definition, ty, thir));
        let found = &mut self.module_checks[module].diagnostics;
        let failed = found[reported..].iter().any(Diagnostic::is_error);
        if !first {
            found.truncate(reported);
        } else if copy.arguments != ConstArguments::NONE {
            let into = self.internals.function_copies[&copy].reported_in;
            self.report_from_copy(copy, reported, into);
        }
        mir.map(|mir| mir.filter(|_| !failed))
    }

    /// The type of `copy`, asked for at `used_at`: a plain function's
    /// (`function_type`), or, for a function with `const T: type`
    /// parameters, the type of its copy for the types it is given, made the
    /// first time a call asks for it and kept for the compile to build its
    /// body (`next_copy`). Invalid when the function's own signature, or
    /// the copy's, has an error, or when copies are made inside each other
    /// too deep (`COPY_DEPTH_LIMIT`), reported at `used_at`.
    pub fn copy_type(&mut self, copy: FunctionCopy, used_at: Span) -> TypeId {
        let pattern = self.function_type(copy.definition, used_at);
        if copy.arguments == ConstArguments::NONE || pattern == TypeId::INVALID {
            return pattern;
        }
        if let Some(copied) = self.internals.function_copies.get(&copy) {
            return copied.signature;
        }
        let made_inside = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a call is being checked")
            .copy();
        let (depth, reported_in) = match self.internals.function_copies.get(&made_inside) {
            Some(inside) => (inside.depth + 1, inside.reported_in),
            None => (1, self.current_module()),
        };
        if depth > COPY_DEPTH_LIMIT {
            // Reported once: the body that made the copy has the error.
            if std::mem::replace(&mut self.internals.copies_too_deep, true) {
                return TypeId::INVALID;
            }
            let name = model::program::declared_path(self.modules, copy.definition);
            let message = format!(
                "`{name}` makes a new copy of itself inside each copy, without end: \
                 copies nest {COPY_DEPTH_LIMIT} deep here"
            );
            return self.error(diagnostics::CIRCULAR_DEPENDENCY, message, used_at);
        }
        self.internals.function_copies.insert(
            copy,
            Copied {
                signature: TypeId::INVALID,
                depth,
                reported_in,
                called_in: self.current_module(),
                called_at: used_at,
            },
        );
        let module = copy.definition.module.index();
        let reported = self.module_checks[module].diagnostics.len();
        let checking = Checking {
            definition: copy.definition,
            arguments: copy.arguments,
            used_at,
            records_facts: true,
        };
        let signature =
            self.checking_in(checking, |checker| checker.check_function(copy.definition));
        let caller = self.current_module();
        self.report_from_copy(copy, reported, caller);
        let copied = self
            .internals
            .function_copies
            .get_mut(&copy)
            .expect("the copy was just made");
        copied.signature = signature;
        if signature != TypeId::INVALID {
            self.internals.copies_to_build.push(copy);
        }
        signature
    }

    /// A copy of a function with `const T: type` parameters whose body the
    /// compile has not built (`build_function`), the newest first; building
    /// one may make more. None once copies nested too deep.
    pub fn next_copy(&mut self) -> Option<FunctionCopy> {
        self.internals
            .copies_to_build
            .pop()
            .filter(|_| !self.internals.copies_too_deep)
    }

    /// Moves what checking `copy` reported in its function's module, past
    /// the first `reported` diagnostics, to the module `into`, each marking
    /// the call it was made for. Another copy's check may find the same in
    /// the function's text: the module keeps the first.
    fn report_from_copy(&mut self, copy: FunctionCopy, reported: usize, into: ModuleId) {
        let modules = self.modules;
        let definition = copy.definition;
        let found = &mut self.module_checks[definition.module.index()].diagnostics;
        if found.len() == reported {
            return;
        }
        let found: Vec<Diagnostic> = found.drain(reported..).collect();
        let Copied {
            called_in,
            called_at,
            ..
        } = self.internals.function_copies[&copy];
        let name = self.types.copy_text(
            model::program::declared_path(modules, definition),
            copy.arguments,
            |definition| model::program::declared_text(modules, definition),
        );
        let file = &modules[called_in.index()].ast.file;
        let into = &mut self.module_checks[into.index()].diagnostics;
        for diagnostic in found {
            let place = |diagnostic: &Diagnostic| {
                let label = diagnostic.labels.first();
                label.map(|label| (Arc::as_ptr(&label.file), label.span))
            };
            let same = |earlier: &Diagnostic| {
                earlier.code == diagnostic.code
                    && earlier.message == diagnostic.message
                    && place(earlier) == place(&diagnostic)
            };
            if !into.iter().any(same) {
                let message = format!("in `{name}`, the copy made for this call");
                into.push(diagnostic.label(file, called_at, message));
            }
        }
    }

    /// The MIR of the checked THIR `thir` of the function `definition`, of
    /// type `ty`, path-checked; `None` when it has an error.
    fn lower_function(
        &mut self,
        definition: DefId,
        ty: TypeId,
        thir: &thir::Body,
    ) -> Option<mir::Body> {
        let modules = self.modules;
        let module = definition.module.index();
        let Type::Function { result, .. } = &self.types[ty] else {
            unreachable!("a checked body has a function type")
        };
        let debug_types = &mut self.debug_types;
        let mir = lower::lower(thir, ty, &self.types, self.target, modules, debug_types)?;
        let file = &modules[module].ast.file;
        let diagnostics = &mut self.module_checks[module].diagnostics;
        let types = &self.types;
        flow::variable_state::report_variable_state_errors(&mir, file, diagnostics);
        let pointer_bytes = u64::from(self.target.pointer_bits / 8);
        flow::loans::report_loan_errors(&mir, pointer_bytes, file, diagnostics);
        flow::unreachable::warn_unreachable_code(&mir, file, diagnostics);
        // Last: it points at the closing brace.
        let result_text = || {
            types.text(*result, |definition| {
                model::program::declared_text(modules, definition)
            })
        };
        flow::missing_return::report_missing_return(&mir, file, result_text, diagnostics);
        Some(mir)
    }

    /// What the call of `function` with `arguments`, written at `span` in a
    /// constant's value, gives: its MIR, and that of each function it may
    /// call, is built and run (`eval::interpreter::run_function`). `None` when it cannot run,
    /// reported unless a function it runs had an error, which was.
    pub(crate) fn evaluate_call(
        &mut self,
        function: FunctionCopy,
        arguments: &[Value],
        span: Span,
    ) -> Option<Value> {
        // Every function the run may reach, built first: the run then only
        // reads the table. Their modules are what the value depends on. One
        // still being checked is left out: only a run that calls it is a
        // cycle, not one that only may.
        let (mut reached, mut pending) = (FxHashSet::default(), vec![function]);
        while let Some(next) = pending.pop() {
            if self.internals.copies_too_deep {
                return None; // reported
            }
            if !reached.insert(next) {
                continue;
            }
            if !matches!(self.internals.built_bodies.get(&next), Some(Built::Kept(_))) {
                if self
                    .internals
                    .declarations_being_checked
                    .iter()
                    .any(|checking| checking.copy() == next)
                {
                    continue;
                }
                let mut thir = thir::Body::default();
                let body = self.build_function(next, span, &mut thir, true).flatten();
                self.internals
                    .built_bodies
                    .insert(next, Built::Kept(body.map(Box::new)));
            }
            let Some(Built::Kept(Some(body))) = self.internals.built_bodies.get(&next) else {
                continue;
            };
            pending.extend(body.named_functions());
        }
        let module = self.current_module().index();
        let ran = &mut self.module_checks[module].ran_modules;
        for copy in reached {
            if !ran.contains(&copy.definition.module) {
                ran.push(copy.definition.module);
            }
        }
        let ty = self.copy_type(function, span);
        let mir_of = |copy: FunctionCopy| match self.internals.built_bodies.get(&copy) {
            Some(Built::Kept(mir)) => mir.as_deref(),
            None | Some(Built::Reported) => None,
        };
        // Where the run stopped (`None`: at this call), and why.
        let (place, message) = 'failed: {
            let (callee, place) = match mir_of(function) {
                Some(body) => {
                    match eval::interpreter::run_function(
                        function.definition,
                        body,
                        arguments,
                        ty,
                        &self.types,
                        self.target,
                        &mir_of,
                    ) {
                        Ok(value) => return value,
                        Err(Failure::Stopped {
                            message,
                            function,
                            span,
                        }) => break 'failed (Some((function, span)), message),
                        Err(Failure::NoMir {
                            callee,
                            function,
                            span,
                        }) => (callee, Some((function, span))),
                        Err(Failure::Exited(_)) => unreachable!("a constant's call cannot exit"),
                    }
                }
                None => (function, None),
            };
            // Another copy of a function being checked is no cycle.
            if let Some(start) = (self.internals.declarations_being_checked.iter())
                .position(|checking| checking.copy() == callee)
            {
                self.report_cycle(start, span);
                return None;
            }
            if function_declaration(self.modules, callee.definition)
                .body
                .is_some()
            {
                return None; // its error was reported
            }
            let message = "an `extern` function cannot run at compile time";
            (place, message.to_string())
        };
        let diagnostic = Diagnostic::new(
            diagnostics::EVALUATION_FAILED,
            "this call cannot be evaluated at compile time",
        );
        let file = &self.module().ast.file;
        let diagnostic = match place {
            Some((function, stopped)) => {
                let (_, callee_file) = model::program::declared_name(self.modules, function);
                (diagnostic.label(file, span, "")).label(callee_file, stopped, message)
            }
            None => diagnostic.label(file, span, message),
        };
        self.report(diagnostic);
        None
    }
}

/// The function `definition`, as written.
pub(crate) fn function_declaration(
    modules: &[model::program::Module],
    definition: DefId,
) -> &ast::Function {
    match model::program::declaration(modules, definition) {
        ast::Decl::Function(function) => function,
        _ => unreachable!("`{definition:?}` is a function"),
    }
}
