//! A function's body built to checked MIR (THIR, lowering, the path checks),
//! and calls run at compile time on it (`evaluate_call`).

use diagnostics::Diagnostic;
use eval::interpreter;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::constant;
use model::mir;
use model::semantic_types;
use model::thir;
use rustc_hash::FxHashSet;
use source::Span;

use crate::Checked;
use crate::Checker;

/// How far the body of a function is built (`Checker::build_function`),
/// once it is.
pub(crate) enum Built {
    /// Built once, so what its checks found is reported.
    Reported,
    /// Built for a run at compile time and kept for the next run: its MIR
    /// of core concepts, `None` when it has no body or had an error.
    Kept(Option<Box<mir::Body>>),
}

impl<L: Checked> Checker<'_, L> {
    /// Builds the body of the function `copy`, asked for at `used_at`, into
    /// `thir`, recording its facts when `records_facts` (not when it is
    /// built only to run at compile time, `evaluate_call`): checks it
    /// (`check_body`), lowers it to MIR and runs the path checks. `None` when
    /// it has no body or its signature had an error; else its MIR, `None`
    /// when it had an error, naming the program's debug types
    /// (`debug_types`). What it finds is reported by its first build only:
    /// a later one (the compile's, after a run at compile time, or the other
    /// way round) finds the same and drops it.
    pub fn build_function(
        &mut self,
        copy: DeclarationCopy,
        used_at: Span,
        thir: &mut thir::Body,
        records_facts: bool,
    ) -> Option<Option<mir::Body>> {
        let module = copy.definition.module.index();
        let ty = self.copy_type(copy, used_at);
        // Decided now: a run at compile time inside this build builds it too.
        let first = !self.internals.built_bodies.contains_key(&copy);
        if first {
            self.internals.built_bodies.insert(copy, Built::Reported);
        }
        let reported = self.module_checks[module].diagnostics.len();
        let checked = self.check_body(copy, used_at, thir, records_facts);
        let mir = checked.then(|| self.lower_function(copy.definition, ty, thir));
        let found = &mut self.module_checks[module].diagnostics;
        let failed = found[reported..].iter().any(Diagnostic::is_error);
        if !first {
            found.truncate(reported);
        } else if copy.arguments != ConstArguments::NONE {
            let into = self.internals.copies[&copy].reported_in;
            self.report_from_copy(copy, reported, into);
        }
        mir.map(|mir| mir.filter(|_| !failed))
    }

    /// The MIR of the checked THIR `thir` of the function `definition`, of
    /// type `ty`, path-checked; `None` when it has an error.
    fn lower_function(
        &mut self,
        definition: DefId,
        ty: semantic_types::TypeId,
        thir: &thir::Body,
    ) -> Option<mir::Body> {
        let modules = self.modules;
        let module = definition.module.index();
        let semantic_types::Type::Function { result, .. } = &self.types[ty] else {
            unreachable!("a checked body has a function type")
        };
        let debug_types = &mut self.debug_types;
        let mir = lower::lower(thir, ty, &self.types, self.target, modules, debug_types)?;
        let file = &modules[module].ast.file;
        let diagnostics = &mut self.module_checks[module].diagnostics;
        let types = &self.types;
        flow::variable_state::report_variable_state_errors(&mir, file, diagnostics);
        let pointer_bytes = self.target.pointer_bytes();
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

    /// What the call of `function` with `arguments`, written at `span`,
    /// gives when it runs while compiling: a `#[builtin]` function's answer
    /// (`builtin_value`), else its MIR, and that of each function it may
    /// call, is built and run (`eval::interpreter::run_function`). `None`
    /// when it cannot run, reported unless a function it runs had an
    /// error, which was.
    pub(crate) fn evaluate_call(
        &mut self,
        function: DeclarationCopy,
        arguments: &[constant::Value],
        span: Span,
    ) -> Option<constant::Value> {
        if model::program::is_builtin(self.modules, function.definition) {
            return self.builtin_value(function, span);
        }
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
                let body = self.build_function(next, span, &mut thir, false).flatten();
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
        let semantic_types::Type::Function {
            parameters, result, ..
        } = &self.types[ty]
        else {
            unreachable!("a body is a function's")
        };
        let shape = |ty: semantic_types::TypeId| match &self.types[ty] {
            semantic_types::Type::Bool => interpreter::ConstantShape::Bool,
            &semantic_types::Type::Integer(integer) => interpreter::ConstantShape::Integer {
                bits: integer.bits,
                signed: integer.signed,
            },
            &semantic_types::Type::Float(float) => interpreter::ConstantShape::Float(float.class()),
            semantic_types::Type::RawPointer { .. } => interpreter::ConstantShape::RawPointer,
            semantic_types::Type::CString => interpreter::ConstantShape::CString,
            semantic_types::Type::Str => interpreter::ConstantShape::Str,
            other => interpreter::ConstantShape::Refused(constant_refusal(other)),
        };
        let shaped_arguments: Vec<(&constant::Value, interpreter::ConstantShape)> = (arguments
            .iter())
        .zip(parameters)
        .map(|(argument, &ty)| (argument, shape(ty)))
        .collect();
        let result = shape(*result);
        let mir_of = |copy: DeclarationCopy| match self.internals.built_bodies.get(&copy) {
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
                        &shaped_arguments,
                        result,
                        self.target,
                        &mir_of,
                    ) {
                        Ok(value) => return value,
                        Err(interpreter::Failure::Stopped {
                            message,
                            function,
                            span,
                        }) => break 'failed (Some((function, span)), message),
                        Err(interpreter::Failure::NoMir {
                            callee,
                            function,
                            span,
                        }) => (callee, Some((function, span))),
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
            if model::program::function_declaration(self.modules, callee.definition)
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

/// Why a constant cannot hold a value of the type `ty`.
pub(crate) fn constant_refusal(ty: &semantic_types::Type) -> &'static str {
    match ty {
        semantic_types::Type::Struct { .. } => "a constant cannot hold a struct yet",
        semantic_types::Type::Array { .. } => "a constant cannot hold an array yet",
        semantic_types::Type::Range { .. } => "a constant cannot hold a range yet",
        _ if ty.borrows() == Some(true) => {
            "a constant cannot hold a mutable reference, as all its uses would share one value"
        }
        _ => interpreter::ADDRESS_NOT_KNOWN,
    }
}
