//! A function's body built to checked MIR (THIR, lowering, the path checks),
//! and calls run at compile time on it (`evaluate_call`).

use diagnostics::Diagnostic;
use eval::interpreter::Failure;
use model::DefId;
use model::ast;
use model::constant::Value;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use source::Span;

use crate::Checker;
use crate::State;

/// How far the body of a function is built (`Checker::build_function`).
pub(crate) enum Built {
    NotYet,
    /// Built once, so what its checks found is reported.
    Reported,
    /// Built for a run at compile time and kept for the next run: its MIR,
    /// `None` when it has no body or had an error.
    Kept(Option<Box<mir::Body>>),
}

impl Checker<'_> {
    /// Builds the body of the function `definition`, asked for at `used_at`,
    /// into `thir`, only to run it at compile time when `to_run`: checks it
    /// (`check_body`), lowers it to MIR and runs the path checks. `None` when it has no body
    /// or its signature had an error; else its MIR, `None` when it had an
    /// error. What it finds is reported by its first build only: a later one
    /// (the compile's, after a run at compile time, or the other way round)
    /// finds the same and drops it.
    pub fn build_function(
        &mut self,
        definition: DefId,
        used_at: Span,
        thir: &mut thir::Body,
        to_run: bool,
    ) -> Option<Option<mir::Body>> {
        let (module, index) = (definition.module.index(), definition.index as usize);
        let ty = self.function_type(definition, used_at);
        // Decided now: a run at compile time inside this build builds it too.
        let first = matches!(self.module_checks[module].bodies[index], Built::NotYet);
        if first {
            self.module_checks[module].bodies[index] = Built::Reported;
        }
        let reported = self.module_checks[module].diagnostics.len();
        let checked = self.check_body(definition, used_at, thir, to_run);
        let mir = checked.then(|| self.lower_function(definition, ty, thir));
        let found = &mut self.module_checks[module].diagnostics;
        let failed = found[reported..].iter().any(Diagnostic::is_error);
        if !first {
            found.truncate(reported);
        }
        mir.map(|mir| mir.filter(|_| !failed))
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
        let Type::Function {
            ref parameters,
            result,
            ..
        } = self.types[ty]
        else {
            unreachable!("a checked body has a function type")
        };
        let mir = lower::lower(thir, parameters.len(), result, &self.types)?;
        let file = &modules[module].ast.file;
        // Reported here, then added: a call's origins are read from any
        // module's checks, this one's too.
        let mut found = Vec::new();
        let diagnostics = &mut found;
        flow::report_variable_state_errors(&mir, file, diagnostics);
        let module_checks = &self.module_checks;
        let types = &self.types;
        let origins_of = |definition: DefId| {
            let State::Done(ty) =
                module_checks[definition.module.index()].declared_types[definition.index as usize]
            else {
                unreachable!("a call is checked, and so its function")
            };
            match types[ty] {
                Type::Function { ref origins, .. } => &**origins,
                // No origins would let a returned reference dangle unseen.
                _ => unreachable!("a call to a function with a signature error is not lowered"),
            }
        };
        flow::loans::report_loan_errors(&mir, types, definition, &origins_of, file, diagnostics);
        flow::warn_unreachable_code(&mir, file, diagnostics);
        // Last: it points at the closing brace.
        flow::report_missing_return(
            &mir,
            file,
            types,
            result,
            |definition| {
                let (name, file) = project::declared_name(modules, definition);
                name.text(file)
            },
            diagnostics,
        );
        self.module_checks[module].diagnostics.append(&mut found);
        Some(mir)
    }

    /// What the call of `function` with `arguments`, written at `span` in a
    /// constant's value, gives: its MIR, and that of each function it may
    /// call, is built and run (`eval::interpreter::run`). `None` when it cannot run,
    /// reported unless a function it runs had an error, which was.
    pub(crate) fn evaluate_call(
        &mut self,
        function: DefId,
        arguments: &[Value],
        span: Span,
    ) -> Option<Value> {
        // Every function the run may reach, built first: the run then only
        // reads the table. Their modules are what the value depends on. One
        // still being checked is left out: only a run that calls it is a
        // cycle, not one that only may.
        let (mut reached, mut pending) = (Vec::new(), vec![function]);
        while let Some(next) = pending.pop() {
            if reached.contains(&next) {
                continue;
            }
            reached.push(next);
            let (module, index) = (next.module.index(), next.index as usize);
            if !matches!(self.module_checks[module].bodies[index], Built::Kept(_)) {
                if self
                    .checking
                    .iter()
                    .any(|checking| checking.definition == next)
                {
                    continue;
                }
                let mut thir = thir::Body::default();
                let mir = self.build_function(next, span, &mut thir, true).flatten();
                self.module_checks[module].bodies[index] = Built::Kept(mir.map(Box::new));
            }
            let Built::Kept(Some(ref mir)) = self.module_checks[module].bodies[index] else {
                continue;
            };
            let calls = mir.instructions.ids().map(|id| &mir.instructions[id].kind);
            pending.extend(calls.filter_map(|kind| match *kind {
                mir::InstructionKind::Call { function, .. } => Some(function),
                _ => None,
            }));
        }
        let module = self.current_module().index();
        let ran = &mut self.module_checks[module].ran_modules;
        for definition in reached {
            if !ran.contains(&definition.module) {
                ran.push(definition.module);
            }
        }
        let mir_of = |definition: DefId| match self.module_checks[definition.module.index()].bodies
            [definition.index as usize]
        {
            Built::Kept(ref mir) => mir.as_deref(),
            Built::NotYet | Built::Reported => None,
        };
        // Where the run stopped (`None`: at this call), and why.
        let (place, message) = 'failed: {
            let (callee, place) = match mir_of(function) {
                Some(body) => {
                    match eval::interpreter::run(function, body, arguments, &self.types, &mir_of) {
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
                    }
                }
                None => (function, None),
            };
            if let Some(start) =
                (self.checking.iter()).position(|checking| checking.definition == callee)
            {
                self.report_cycle(start, span);
                return None;
            }
            if function_declaration(self.modules, callee).body.is_some() {
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
                let (_, callee_file) = project::declared_name(self.modules, function);
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
    modules: &[project::Module],
    definition: DefId,
) -> &ast::Function {
    let tree = &modules[definition.module.index()].ast;
    match &tree.decls[tree.declaration(definition.index)] {
        ast::Decl::Function(function) => function,
        _ => unreachable!("`{definition:?}` is a function"),
    }
}
