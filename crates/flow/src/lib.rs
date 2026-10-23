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
//!   is not lowered at all (`Checker::check_body`), so these checks never
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
//! - No building of the MIR: the `lower` crate does that, and these checks run
//!   after it, called by `compile`.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::DefId;
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
pub fn report_missing_return<'n>(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    types: &Types,
    result: TypeId,
    name_of: impl Fn(DefId) -> &'n str + Copy,
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
    let end_label = format!(
        "function ends without returning `{}`",
        types.text(result, name_of)
    );
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

/// Reports each variable used where it may not be set yet or was moved, and
/// each variable without `mut` assigned where it may already be set, once per
/// variable; and each move out of a field. Only a `let` without a value is
/// unset at first, and only a variable whose type moves can be moved, so a
/// function with neither is not walked.
///
/// Forward dataflow over the reachable blocks (`Facts` at each block's
/// start). The first loop finds the facts at each block's start; the second
/// walks each block again from them and reports at each use and `store`.
pub fn report_variable_state_errors(
    body: &mir::Body,
    types: &Types,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    if body.variables.ids().all(|variable| {
        let mir::Variable {
            ty, initialized, ..
        } = body.variables[variable];
        initialized && types[ty].is_copy()
    }) {
        return;
    }
    let instructions = |index: usize| {
        let block = &body.blocks[Id::<mir::Block>::from_index(index)];
        body.lists.items(block.instructions)
    };
    // `None` for a block not reached.
    let mut starts: Vec<Option<Facts>> = vec![None; body.blocks.len()];
    starts[0] = Some(Facts::new(body.variables.len()));
    let mut pending = vec![0];
    while let Some(index) = pending.pop() {
        let Some(mut facts) = starts[index].clone() else {
            unreachable!("only reached blocks are pending")
        };
        for instruction in instructions(index) {
            facts.step(&body.instructions[instruction].kind);
        }
        let block = &body.blocks[Id::<mir::Block>::from_index(index)];
        for target in block.terminator.targets() {
            let joined = match &starts[target.index()] {
                None => facts.clone(),
                Some(start) => start.join(&facts),
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
        let Some(mut facts) = start else {
            continue;
        };
        for (position, instruction) in instructions(index).enumerate() {
            let span = body.instructions.span(instruction);
            let kind = &body.instructions[instruction].kind;
            let error = match *kind {
                mir::InstructionKind::MoveField { .. } => {
                    errors.push((span.start, partial_move(file, span)));
                    None
                }
                mir::InstructionKind::Load(variable)
                | mir::InstructionKind::Move(variable)
                | mir::InstructionKind::LoadField { variable, .. }
                | mir::InstructionKind::StoreField { variable, .. }
                    if !has(&facts.always, variable) =>
                {
                    let writes_field = matches!(kind, mir::InstructionKind::StoreField { .. });
                    let error = if has(&facts.moved, variable) {
                        let block = Id::from_index(index);
                        let moves = move_sites(body, variable, block, position);
                        moved_use(body, file, variable, span, &moves, writes_field)
                    } else {
                        let on_some_path = has(&facts.maybe, variable);
                        unset_use(body, file, variable, span, on_some_path, writes_field)
                    };
                    Some((variable, error))
                }
                mir::InstructionKind::Store { variable, .. }
                    if has(&facts.maybe, variable) && !body.variables[variable].mutable =>
                {
                    let on_every_path = has(&facts.always, variable);
                    let error = second_assignment(body, file, variable, span, on_every_path);
                    Some((variable, error))
                }
                _ => None,
            };
            facts.step(kind);
            if let Some((variable, error)) = error
                && !std::mem::replace(&mut reported[variable.index()], true)
            {
                errors.push((span.start, error));
            }
        }
    }
    errors.sort_by_key(|&(start, _)| start);
    diagnostics.extend(errors.into_iter().map(|(_, error)| error));
}

/// What is known about each variable at one point of a function, one bit per
/// variable in each set.
#[derive(Clone, PartialEq)]
struct Facts {
    always: Bits, // set on every path to here (joined with "and")
    maybe: Bits,  // set on some path to here, even if moved since ("or")
    moved: Bits,  // moved on some path to here and not set since ("or")
}

impl Facts {
    /// Nothing set or moved, for `variables` variables.
    fn new(variables: usize) -> Facts {
        let none = vec![0; variables.div_ceil(64)];
        Facts {
            always: none.clone(),
            maybe: none.clone(),
            moved: none,
        }
    }

    /// The facts after `kind` runs: a `store` sets its variable, a `move`
    /// unsets it.
    fn step(&mut self, kind: &mir::InstructionKind) {
        match *kind {
            mir::InstructionKind::Store { variable, .. } => {
                set(&mut self.always, variable);
                set(&mut self.maybe, variable);
                clear(&mut self.moved, variable);
            }
            mir::InstructionKind::Move(variable) => {
                clear(&mut self.always, variable);
                set(&mut self.moved, variable);
            }
            _ => {}
        }
    }

    /// The facts where paths with `self` and `other` meet.
    fn join(&self, other: &Facts) -> Facts {
        let with = |a: &Bits, b: &Bits, op: fn(u64, u64) -> u64| {
            a.iter().zip(b).map(|(&a, &b)| op(a, b)).collect()
        };
        Facts {
            always: with(&self.always, &other.always, |a, b| a & b),
            maybe: with(&self.maybe, &other.maybe, |a, b| a | b),
            moved: with(&self.moved, &other.moved, |a, b| a | b),
        }
    }
}

/// One bit per variable.
type Bits = Vec<u64>;

/// Turns `variable`'s bit on in `bits`.
fn set(bits: &mut Bits, variable: Id<mir::Variable>) {
    bits[variable.index() / 64] |= 1 << (variable.index() % 64);
}

/// Turns `variable`'s bit off in `bits`.
fn clear(bits: &mut Bits, variable: Id<mir::Variable>) {
    bits[variable.index() / 64] &= !(1 << (variable.index() % 64));
}

/// Whether `variable`'s bit is on in `bits`.
fn has(bits: &Bits, variable: Id<mir::Variable>) -> bool {
    bits[variable.index() / 64] >> (variable.index() % 64) & 1 != 0
}

/// Where `variable` was moved on the paths to the instruction at `position`
/// of `block` that do not set it again: searched back only for an error.
fn move_sites(
    body: &mir::Body,
    variable: Id<mir::Variable>,
    block: Id<mir::Block>,
    position: usize,
) -> Vec<Span> {
    let mut predecessors = vec![Vec::new(); body.blocks.len()];
    for from in body.blocks.ids() {
        for target in body.blocks[from].terminator.targets() {
            predecessors[target.index()].push(from);
        }
    }
    let mut seen = vec![false; body.blocks.len()];
    let mut sites = Vec::new();
    let mut pending = vec![(block, Some(position))];
    while let Some((block, end)) = pending.pop() {
        let instructions: Vec<_> = body.lists.items(body.blocks[block].instructions).collect();
        let before = &instructions[..end.unwrap_or(instructions.len())];
        // The last move or store of `variable` before `end`: the move is a site;
        // a store ends the path.
        let last = before.iter().rev().find_map(|&instruction| {
            match body.instructions[instruction].kind {
                mir::InstructionKind::Move(moved) if moved == variable => {
                    Some(Some(body.instructions.span(instruction)))
                }
                mir::InstructionKind::Store {
                    variable: stored, ..
                } if stored == variable => Some(None),
                _ => None,
            }
        });
        match last {
            Some(site) => sites.extend(site),
            None => {
                for &from in &predecessors[block.index()] {
                    if !std::mem::replace(&mut seen[from.index()], true) {
                        pending.push((from, None));
                    }
                }
            }
        }
    }
    sites.sort_by_key(|span| span.start);
    sites.dedup();
    sites
}

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
/// to there; `on_some_path` when some path did set it. A use that
/// `writes_field` sets one of its fields, which needs the whole struct set.
fn unset_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    on_some_path: bool,
    writes_field: bool,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    let here = if on_some_path {
        format!("`{name}` is not set on every path to here")
    } else {
        format!("`{name}` is not set yet here")
    };
    let message = if writes_field {
        format!("cannot set a field of `{name}` before `{name}` is set")
    } else {
        format!("`{name}` is used before it is set")
    };
    Diagnostic::new(diagnostics::USED_BEFORE_SET, message)
        .label(file, span, here)
    .label(file, declared, "declared here without a value")
    .help(
        format!(
            "set `{name}` on every path before this use, or give it a value where it is declared"
        ),
        Vec::new(),
    )
}

/// The error for `variable`, used at `span` where some path moved it at one
/// of `moves`; a use that `writes_field` sets one of its fields.
fn moved_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    moves: &[Span],
    writes_field: bool,
) -> Diagnostic {
    let name = file.slice(body.variables.span(variable));
    let message = if writes_field {
        format!("cannot set a field of `{name}` after `{name}` was moved")
    } else {
        format!("`{name}` is used after it was moved")
    };
    let mut error = Diagnostic::new(diagnostics::USED_AFTER_MOVE, message).label(
        file,
        span,
        format!("`{name}` is used here"),
    );
    for &moved in moves {
        error = error.label(file, moved, format!("`{name}` is moved here"));
    }
    error.note("a struct moves when used by value").help(
        format!("set `{name}` again before this use, or stop moving it"),
        Vec::new(),
    )
}

/// The error for a field whose type moves, read by value at `span`: a field
/// cannot be moved out of its struct.
fn partial_move(file: &Arc<SourceFile>, span: Span) -> Diagnostic {
    Diagnostic::new(
        diagnostics::PARTIAL_MOVE,
        "cannot move a field out of its struct",
    )
    .label(file, span, "this field's type moves")
    .help(
        "move the whole struct, or read only fields that copy",
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
