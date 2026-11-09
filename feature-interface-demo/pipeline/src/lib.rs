//! The driver, in the real compiler's phase order: parse with every
//! feature's rules, check into THIR, lower to MIR, run the path checks,
//! clean up, then run on the interpreter or write LLVM and build natively. It names no feature; `language` gives the rules and the
//! type names.

pub mod dump;

use gateway::check;
use gateway::check::Checker;
use gateway::lower;
use gateway::syntax::ParseError;
use gateway::syntax::Parser;
use model::facts::Target;
use model::mir::Program;

/// A refusal or a warning, written as
/// `error[E0002] at 3:14: expected i32, found bool`.
pub type Error = String;

/// What a compile without errors gives: `then`'s value, and the warnings
/// in source order.
pub struct Output<R> {
    pub value: R,
    pub warnings: Vec<Error>,
}

/// Compiles `source` for `target` through cleanup, and hands `then` the
/// checker (for type and function names) and the program with the id of
/// its `main`. On an error, gives the errors and warnings in source order.
fn compile<R>(
    source: &str,
    target: Target,
    then: impl FnOnce(&Checker<'_>, &Program, u32) -> R,
) -> Result<Output<R>, Vec<Error>> {
    let position = |at: u32| {
        let before = &source[..at as usize];
        let line = before.matches('\n').count() + 1;
        let column = at as usize - before.rfind('\n').map_or(0, |newline| newline + 1) + 1;
        format!("{line}:{column}")
    };
    let syntax_error = |error: ParseError| {
        let at = position(error.span.start);
        format!("error[E{:04}] at {at}: {}", check::SYNTAX.0, error.message)
    };
    let mut parser =
        Parser::new(source, &language::RULES).map_err(|error| vec![syntax_error(error)])?;
    let declarations = parser
        .program()
        .map_err(|error| vec![syntax_error(error)])?;
    let tree = parser.tree;
    let checker = check::check(
        source,
        &tree,
        &declarations,
        &language::type_names(),
        target,
    );
    if !checker.refusals.is_empty() {
        return Err((checker.refusals.iter())
            .map(|refusal| {
                let at = position(refusal.span.start);
                format!("error[E{:04}] at {at}: {}", refusal.code.0, refusal.message)
            })
            .collect());
    }
    let main = checker
        .function_named("main")
        .ok_or_else(|| vec!["no function `main`".to_string()])?;
    let mut program = lower::lower(&checker);
    let report = flow::check(&program, source);
    let write = |kind: &str, diagnostic: &flow::Diagnostic| {
        let at = position(diagnostic.span.start);
        let code = diagnostic.code;
        (
            diagnostic.span.start,
            format!("{kind}[{code}] at {at}: {}", diagnostic.message),
        )
    };
    let errors = report.errors.iter().map(|error| write("error", error));
    let warnings = report
        .warnings
        .iter()
        .map(|warning| write("warning", warning));
    let mut written: Vec<(u32, Error)> = errors.chain(warnings).collect();
    written.sort_by_key(|&(start, _)| start);
    let written = written.into_iter().map(|(_, message)| message).collect();
    if !report.errors.is_empty() {
        return Err(written);
    }
    flow::cleanup::clean(&mut program);
    Ok(Output {
        value: then(&checker, &program, main.0),
        warnings: written, // no error, so only warnings
    })
}

/// Compiles `source` for `target` and gives what its `main` returns.
pub fn run(source: &str, target: Target) -> Result<Output<u64>, Vec<Error>> {
    compile(source, target, |_, program, main| eval::run(program, main))
}

/// Compiles `source` for `target`, builds it with clang and runs it, and
/// gives what its `main` returns.
pub fn run_native(source: &str, target: Target) -> Result<Output<u64>, Vec<Error>> {
    let built = compile(source, target, |_, program, main| {
        llvm::run(&llvm::text(program, main), target)
    })?;
    Ok(Output {
        value: built.value.map_err(|error| vec![error])?,
        warnings: built.warnings,
    })
}

/// Compiles `source` for `target` and gives its LLVM text.
pub fn llvm(source: &str, target: Target) -> Result<Output<String>, Vec<Error>> {
    compile(source, target, |_, program, main| llvm::text(program, main))
}

/// Compiles `source` for `target` and gives its MIR as text.
pub fn mir(source: &str, target: Target) -> Result<Output<String>, Vec<Error>> {
    compile(source, target, dump::text)
}

#[cfg(test)]
mod tests;
