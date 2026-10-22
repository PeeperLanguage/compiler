//! Path checks: errors that depend on which paths execution can take through
//! one function. Type checking looks at each expression on its own; whether a
//! value is returned, or a variable set, depends on the route taken to a line,
//! and only the MIR's graph of blocks shows every route.
//!
//! What they check, each over one function's MIR:
//!
//! - `report_missing_return` (T0017): a function with a result whose closing `}` can
//!   be reached, and so ends without returning a value. The error names how
//!   each path gets there: a branch side, or a nested block that ends.
//! - `initialization` (T0039 and T0007): a variable used where some path left
//!   it unset, and a variable without `mut` assigned where some path already
//!   set it. This is what lets `let x: i32;` be set once later.
//! - `warn_unreachable_code` (W0001, a warning): code no path reaches, such as code
//!   after a `return`.
//!
//! How they work: they only read the graph. A block's terminator gives the
//! edges out of it (`Terminator::targets`); a check that needs the edges in
//! builds the predecessor lists itself. Whether a block can be reached is
//! found from the entry. `initialization` is dataflow: it carries bits per
//! variable along the edges until nothing changes, which also works once
//! loops add edges back.
//!
//! What they do not do:
//!
//! - No type, name or operator rules, and no constant folding: the checker did
//!   those while building THIR. A function whose checking reported an error
//!   is not lowered at all (`ModuleChecker::check_body`), so these checks never
//!   repeat or follow from an earlier error.
//! - No changes to the MIR, and no fixes: they only report.
//! - No use of constant values to decide which paths exist. `if false { ... }`
//!   still has two sides, as in Go and Rust, so whether code compiles never
//!   depends on a constant's value; a label only says which side the constant
//!   takes.
//! - No errors from blocks that cannot be reached: `warn_unreachable_code` warns
//!   about them once, and the other checks skip them.
//! - No warning that a condition is always true or false: `if DEBUG` with a
//!   constant `DEBUG` is how code is switched on and off, and Go, Rust and Zig
//!   do not warn there either (decided by the owner, 2026-10-17).
//! - No building of the MIR: `lower` in lib.rs does that, and these checks run
//!   after it, called by `compile`.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::constant::Value;
use model::mir;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

/// The most paths one missing-return error names.
const SHOWN_CAUSES: usize = 3;

/// Reports a function, written in `file` and giving a `result`, whose end
/// can be reached without a `return`.
///
/// Lowering ends such a function's last block with `FallOff` at its closing
/// brace, so the question is whether that block can be reached. If it can,
/// the error names the paths that arrive there right after a branch side or a
/// nested block, at most `SHOWN_CAUSES` of them. When the last block itself
/// runs statements of the function body, only the end is named, since one
/// `return` there fixes every path.
pub fn report_missing_return(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    types: &Types,
    result: TypeId,
    diagnostics: &mut Vec<Diagnostic>,
) {
    if result == TypeId::VOID {
        return; // such a function returns at its closing brace
    }
    let end = body
        .blocks
        .ids()
        .find_map(|block| match body.blocks[block].terminator {
            mir::Terminator::FallOff(span) => Some((block, span)),
            _ => None,
        });
    let Some((end, end_span)) = end else {
        return;
    };
    let reachable = reachable(body);
    if !reachable[end.index()] {
        return;
    }
    // (where, label) of each path that arrives at the end.
    let mut causes = Vec::new();
    if body.blocks[end].instructions.is_empty() {
        let mut predecessors = vec![Vec::new(); body.blocks.len()];
        for block in body.blocks.ids().filter(|block| reachable[block.index()]) {
            for target in body.blocks[block].terminator.targets() {
                predecessors[target.index()].push(block);
            }
        }
        // Back from the end through blocks that do nothing: the first thing
        // met on each path is how that path gets there. A branch is met once
        // through each side that leads here.
        let mut visited = vec![false; body.blocks.len()];
        let mut pending = vec![end];
        while let Some(block) = pending.pop() {
            for &predecessor in &predecessors[block.index()] {
                let mir::Block {
                    instructions,
                    ref terminator,
                    ..
                } = body.blocks[predecessor];
                if let mir::Terminator::Branch {
                    condition,
                    span,
                    then,
                    ..
                } = *terminator
                {
                    // A constant condition still has two sides here, as in Go
                    // and Rust, so whether code compiles never depends on a
                    // constant's value; the label only says which side it takes.
                    let always = match condition {
                        mir::Operand::Constant(constant) => {
                            match body.constants[constant.index()].value {
                                Value::Bool(value) => Some(value),
                                _ => None,
                            }
                        }
                        _ => None,
                    };
                    let side = then == block;
                    let label = match (always, side) {
                        (Some(true), true) => "this is always true, so nothing is returned",
                        (Some(false), false) => "this is always false, so nothing is returned",
                        (_, true) => "if this is true, nothing is returned",
                        (_, false) => "if this is false, nothing is returned",
                    };
                    match causes.iter_mut().find(|(cause, _)| *cause == span) {
                        None => causes.push((span, label)),
                        Some(other_side) if always.is_none() => {
                            other_side.1 = "either way, nothing is returned";
                        }
                        Some(other_side) if always == Some(side) => other_side.1 = label,
                        Some(_) => {}
                    }
                } else if !std::mem::replace(&mut visited[predecessor.index()], true) {
                    match body.lists.items(instructions).last() {
                        Some(last) => causes.push((
                            body.instructions.span(last),
                            "this path ends without returning",
                        )),
                        None => pending.push(predecessor),
                    }
                }
            }
        }
    }
    causes.sort_by_key(|&(span, _): &(Span, &str)| span.start);
    let hidden = causes.len().saturating_sub(SHOWN_CAUSES);
    let end_label = format!("function ends without returning `{}`", types.text(result));
    let mut error = Diagnostic::new(
        diagnostics::MISSING_RETURN,
        "not all control paths return a value",
    );
    for &(span, label) in &causes[..causes.len() - hidden] {
        error = error.label(file, span, label);
    }
    error = error
        .label(file, end_span, end_label)
        .note("execution can reach the end of the function without a `return`");
    if hidden > 0 {
        error = error.note(format!("{hidden} more path(s) like these are not shown"));
    }
    let help = if causes.is_empty() {
        "add a `return` at the end of the function"
    } else {
        "add a `return` at the end of the function, or make each path shown return a value"
    };
    diagnostics.push(error.help(help, Vec::new()));
}

/// Reports each variable used where it may not be set yet, and each variable
/// without `mut` assigned where it may already be set, once per variable. Only
/// a `let` without a value can be unset: parameters and other `let`s are set
/// where they are declared, so a function with no such `let` is not walked.
///
/// Forward dataflow over the reachable blocks, with two bits per variable at
/// each block's start: set on every path to it (joined with "and") and set on
/// some path to it (joined with "or"). A `store` sets both bits. The first
/// loop finds the bits at each block's start; the second walks each block
/// again from those bits and reports at each `load` and `store`.
pub fn report_initialization_errors(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    if body
        .variables
        .ids()
        .all(|variable| body.variables[variable].initialized)
    {
        return;
    }
    let words = body.variables.len().div_ceil(64);
    let set = |bits: &mut Bits, variable: Id<mir::Variable>| {
        bits[variable.index() / 64] |= 1 << (variable.index() % 64);
    };
    let has = |bits: &Bits, variable: Id<mir::Variable>| {
        bits[variable.index() / 64] >> (variable.index() % 64) & 1 != 0
    };
    let instructions = |index: usize| {
        let block = &body.blocks[Id::<mir::Block>::from_index(index)];
        body.lists.items(block.instructions)
    };
    // At each block's start: (set on every path, set on some path); `None`
    // for a block not reached.
    let mut starts: Vec<Option<(Bits, Bits)>> = vec![None; body.blocks.len()];
    starts[0] = Some((vec![0; words], vec![0; words]));
    let mut pending = vec![0];
    while let Some(index) = pending.pop() {
        let Some((mut always, mut maybe)) = starts[index].clone() else {
            unreachable!("only reached blocks are pending")
        };
        for instruction in instructions(index) {
            if let mir::InstructionKind::Store { variable, .. } =
                body.instructions[instruction].kind
            {
                set(&mut always, variable);
                set(&mut maybe, variable);
            }
        }
        let block = &body.blocks[Id::<mir::Block>::from_index(index)];
        for target in block.terminator.targets() {
            let joined = match &starts[target.index()] {
                None => (always.clone(), maybe.clone()),
                Some((every, some)) => (
                    every.iter().zip(&always).map(|(a, b)| a & b).collect(),
                    some.iter().zip(&maybe).map(|(a, b)| a | b).collect(),
                ),
            };
            if starts[target.index()].as_ref() != Some(&joined) {
                starts[target.index()] = Some(joined);
                pending.push(target.index());
            }
        }
    }
    let mut reported = vec![false; body.variables.len()];
    let mut errors = Vec::new();
    for (index, start) in starts.into_iter().enumerate() {
        let Some((mut always, mut maybe)) = start else {
            continue;
        };
        for instruction in instructions(index) {
            let span = body.instructions.span(instruction);
            let (variable, error) = match body.instructions[instruction].kind {
                mir::InstructionKind::Load(variable) if !has(&always, variable) => (
                    variable,
                    unset_use(body, file, variable, span, has(&maybe, variable)),
                ),
                mir::InstructionKind::Store { variable, .. } => {
                    let twice = has(&maybe, variable) && !body.variables[variable].mutable;
                    let error = twice.then(|| {
                        second_assignment(body, file, variable, span, has(&always, variable))
                    });
                    set(&mut always, variable);
                    set(&mut maybe, variable);
                    let Some(error) = error else {
                        continue;
                    };
                    (variable, error)
                }
                _ => continue,
            };
            if !std::mem::replace(&mut reported[variable.index()], true) {
                errors.push((span.start, error));
            }
        }
    }
    errors.sort_by_key(|&(start, _)| start);
    diagnostics.extend(errors.into_iter().map(|(_, error)| error));
}

/// One bit per variable.
type Bits = Vec<u64>;

/// Warns once at the first code of each stretch that no path reaches, such as
/// code after a `return`; what that stretch leads to is not warned about
/// again. Code is an instruction, a `return` or a branch; a jump between
/// blocks or the closing brace is not.
pub fn warn_unreachable_code(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let mut seen = reachable(body);
    // (the first code written, its block) of each block not reached.
    let mut starts: Vec<(Span, Id<mir::Block>)> = body
        .blocks
        .ids()
        .filter(|block| !seen[block.index()])
        .filter_map(|block| {
            let mir::Block {
                instructions,
                ref terminator,
                ..
            } = body.blocks[block];
            let terminator = match *terminator {
                mir::Terminator::Return { span, .. } | mir::Terminator::Branch { span, .. } => {
                    Some(span)
                }
                mir::Terminator::Goto(_) | mir::Terminator::FallOff(_) => None,
            };
            let spans = body.lists.items(instructions);
            let spans = spans.map(|instruction| body.instructions.span(instruction));
            // The earliest, and of those the widest: the whole statement.
            let first = spans
                .chain(terminator)
                .min_by_key(|span| (span.start, std::cmp::Reverse(span.end)))?;
            Some((first, block))
        })
        .collect();
    starts.sort_by_key(|&(span, _)| span.start);
    for (span, block) in starts {
        if seen[block.index()] {
            continue; // follows code already warned about
        }
        diagnostics.push(
            Diagnostic::new(diagnostics::UNREACHABLE_CODE, "unreachable code").label(
                file,
                span,
                "this never runs",
            ),
        );
        mark_reachable(body, block, &mut seen);
    }
}

/// The error for `variable`, used at `span` where it is not set on every path
/// to there; `on_some_path` when some path did set it.
fn unset_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    on_some_path: bool,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    let here = if on_some_path {
        format!("`{name}` is not set on every path to here")
    } else {
        format!("`{name}` is not set yet here")
    };
    Diagnostic::new(
        diagnostics::USED_BEFORE_SET,
        format!("`{name}` is used before it is set"),
    )
    .label(file, span, here)
    .label(file, declared, "declared here without a value")
    .help(
        format!(
            "set `{name}` on every path before this use, or give it a value where it is declared"
        ),
        Vec::new(),
    )
}

/// The error for `variable`, without `mut`, assigned at `span` where some path
/// already set it; `on_every_path` when every path did.
fn second_assignment(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    on_every_path: bool,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    let here = if on_every_path {
        format!("`{name}` is already set here")
    } else {
        format!("`{name}` may already be set here")
    };
    Diagnostic::new(
        diagnostics::INVALID_ASSIGNMENT,
        format!("cannot assign twice to immutable `{name}`"),
    )
    .label(file, span, here)
    .label(file, declared, "declared without `mut` here")
    .help(
        format!("write `let mut {name}` to assign it more than once"),
        Vec::new(),
    )
}

/// Which blocks control can reach from the entry, by index.
fn reachable(body: &mir::Body) -> Vec<bool> {
    let mut reachable = vec![false; body.blocks.len()];
    mark_reachable(body, Id::from_index(0), &mut reachable);
    reachable
}

/// Marks in `marked` `from` and every block control can reach from it,
/// stopping at blocks already marked.
fn mark_reachable(body: &mir::Body, from: Id<mir::Block>, marked: &mut [bool]) {
    let mut pending = vec![from];
    while let Some(block) = pending.pop() {
        if !std::mem::replace(&mut marked[block.index()], true) {
            pending.extend(body.blocks[block].terminator.targets());
        }
    }
}
