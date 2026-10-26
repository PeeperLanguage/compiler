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
//! - `loans::report_loan_errors` (T0037): a place used in a way that a
//!   reference still in use forbids, or a variable whose scope ends while a
//!   reference to it is still in use.
//! - `warn_unreachable_code` (W0001, a warning): code no path reaches, such as code
//!   after a `return`.
//!
//! How they work: they only read the graph. A block's terminator gives the
//! edges out of it (`Terminator::targets`), and `predecessors` the edges in.
//! Whether a block can be reached is found from the entry. The variable state
//! and loan checks are dataflow (`dataflow`): they carry bits along the edges
//! until nothing changes, which also works once loops add edges back.
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

use std::marker::PhantomData;
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

pub mod loans;

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
    let mut end_code = body.lists.items(body.blocks[end].instructions);
    if !end_code.any(|id| body.instructions[id].kind.runs()) {
        let predecessors = predecessors(body);
        // Back from the end through blocks that do nothing: the first thing
        // met on each path is how that path gets there. A branch is met once
        // through each side that leads here.
        let mut visited = vec![false; body.blocks.len()];
        let mut pending = vec![end];
        while let Some(block) = pending.pop() {
            for &predecessor in &predecessors[block.index()] {
                if !reachable[predecessor.index()] {
                    continue;
                }
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
                    let code = body.lists.items(instructions);
                    match code.filter(|&id| body.instructions[id].kind.runs()).last() {
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
/// unset at first, and only a move unsets a variable or takes a part of a
/// place, so a function with neither is not walked.
///
/// Forward dataflow over the reachable blocks: `dataflow` finds the `Facts`
/// at each block's start; the loop walks each block again from them and
/// reports at each use and `store`.
pub fn report_variable_state_errors(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let starts_set = |variable| body.variables[variable].initialized;
    let moves = |id| matches!(body.instructions[id].kind, mir::InstructionKind::Move(_));
    if body.variables.ids().all(starts_set) && !body.instructions.ids().any(moves) {
        return;
    }
    let instructions = |index: usize| {
        let block = &body.blocks[Id::<mir::Block>::from_index(index)];
        body.lists.items(block.instructions)
    };
    let starts = dataflow(
        body.blocks.len(),
        [(Id::from_index(0), Facts::new(body.variables.len()))],
        |block| body.blocks[block].terminator.targets(),
        |block, facts| {
            for instruction in instructions(block.index()) {
                facts.step(&body.instructions[instruction].kind);
            }
        },
        Facts::join,
    );
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
                mir::InstructionKind::Move(ref place) if place.whole_variable().is_none() => {
                    let error = partial_move(file, span, place);
                    errors.push((span.start, error));
                    None
                }
                mir::InstructionKind::Store { ref place, .. }
                    if let Some(variable) = place.whole_variable() =>
                {
                    let assigned = facts.maybe.has(variable) && !body.variables[variable].mutable;
                    assigned.then(|| {
                        let on_every_path = facts.always.has(variable);
                        let error = second_assignment(body, file, variable, span, on_every_path);
                        (variable, error)
                    })
                }
                mir::InstructionKind::Load(ref place)
                | mir::InstructionKind::Move(ref place)
                | mir::InstructionKind::Store { ref place, .. }
                | mir::InstructionKind::Borrow { ref place, .. }
                | mir::InstructionKind::Address(ref place)
                    if !facts.always.has(place.variable) =>
                {
                    let variable = place.variable;
                    // Writing through a reference only reads the variable.
                    let writes_part = matches!(kind, mir::InstructionKind::Store { .. })
                        && !place.is_behind_reference();
                    let written = writes_part.then(|| part(place)[0]);
                    let error = if facts.moved.has(variable) {
                        let block = Id::from_index(index);
                        let moves = move_sites(body, variable, block, position);
                        moved_use(body, file, variable, span, &moves, written)
                    } else {
                        let on_some_path = facts.maybe.has(variable);
                        unset_use(body, file, variable, span, on_some_path, written)
                    };
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

/// Facts carried along a function's edges until nothing changes, which also
/// works once loops add edges back: forward from the entry, or backward from
/// the blocks that leave the function. Gives the facts where each block is
/// entered in that direction (its start forward, its end backward), `None`
/// for a block never entered. `seeds` are the first blocks with their facts;
/// `edges` gives the blocks entered after one; `run` turns the facts where a
/// block is entered into those where it is left; `join` gives the facts
/// where paths meet.
fn dataflow<F: Clone + PartialEq, E: IntoIterator<Item = Id<mir::Block>>>(
    blocks: usize,
    seeds: impl IntoIterator<Item = (Id<mir::Block>, F)>,
    edges: impl Fn(Id<mir::Block>) -> E,
    run: impl Fn(Id<mir::Block>, &mut F),
    join: impl Fn(&F, &F) -> F,
) -> Vec<Option<F>> {
    let mut entered = vec![None; blocks];
    let mut pending = Vec::new();
    for (block, facts) in seeds {
        entered[block.index()] = Some(facts);
        pending.push(block);
    }
    while let Some(block) = pending.pop() {
        let Some(mut facts) = entered[block.index()].clone() else {
            unreachable!("only entered blocks are pending")
        };
        run(block, &mut facts);
        for next in edges(block) {
            let joined = match &entered[next.index()] {
                None => facts.clone(),
                Some(facts_there) => join(facts_there, &facts),
            };
            if entered[next.index()].as_ref() != Some(&joined) {
                entered[next.index()] = Some(joined);
                pending.push(next);
            }
        }
    }
    entered
}

/// The blocks that go straight to each block, by index.
fn predecessors(body: &mir::Body) -> Vec<Vec<Id<mir::Block>>> {
    let mut predecessors = vec![Vec::new(); body.blocks.len()];
    for from in body.blocks.ids() {
        for target in body.blocks[from].terminator.targets() {
            predecessors[target.index()].push(from);
        }
    }
    predecessors
}

/// What is known about each variable at one point of a function, one bit per
/// variable in each set.
#[derive(Clone, PartialEq)]
struct Facts {
    always: Bits<mir::Variable>, // set on every path to here (joined with "and")
    maybe: Bits<mir::Variable>,  // set on some path to here, even if moved since ("or")
    moved: Bits<mir::Variable>,  // moved on some path to here and not set since ("or")
}

impl Facts {
    /// Nothing set or moved, for `variables` variables.
    fn new(variables: usize) -> Facts {
        let none = Bits::new(variables);
        Facts {
            always: none.clone(),
            maybe: none.clone(),
            moved: none,
        }
    }

    /// The facts after `kind` runs: a `store` to a whole variable sets it, a
    /// `move` of one unsets it, and the end of its scope forgets it, so a
    /// loop's next pass declares it anew.
    fn step(&mut self, kind: &mir::InstructionKind) {
        match *kind {
            mir::InstructionKind::Store { ref place, .. }
                if let Some(variable) = place.whole_variable() =>
            {
                self.always.set(variable);
                self.maybe.set(variable);
                self.moved.clear(variable);
            }
            mir::InstructionKind::Move(ref place)
                if let Some(variable) = place.whole_variable() =>
            {
                self.always.clear(variable);
                self.moved.set(variable);
            }
            mir::InstructionKind::ScopeEnd(variable) => {
                self.always.clear(variable);
                self.maybe.clear(variable);
                self.moved.clear(variable);
            }
            _ => {}
        }
    }

    /// The facts where paths with `self` and `other` meet.
    fn join(&self, other: &Facts) -> Facts {
        Facts {
            always: self.always.combine(&other.always, |a, b| a & b),
            maybe: self.maybe.combine(&other.maybe, |a, b| a | b),
            moved: self.moved.combine(&other.moved, |a, b| a | b),
        }
    }
}

/// A set of ids of `T`, one bit per id: only an `Id<T>` goes in, so ids of
/// different things, such as variables and loans, cannot be mixed up.
struct Bits<T> {
    words: Vec<u64>,
    kind: PhantomData<fn() -> T>,
}

// Derived impls would require T itself to be Clone and PartialEq.
impl<T> Clone for Bits<T> {
    fn clone(&self) -> Bits<T> {
        Bits {
            words: self.words.clone(),
            kind: PhantomData,
        }
    }
}
impl<T> PartialEq for Bits<T> {
    fn eq(&self, other: &Bits<T>) -> bool {
        self.words == other.words
    }
}

impl<T> Bits<T> {
    /// No id in it, for ids below `count`.
    fn new(count: usize) -> Bits<T> {
        Bits {
            words: vec![0; count.div_ceil(64)],
            kind: PhantomData,
        }
    }

    fn set(&mut self, id: Id<T>) {
        self.words[id.index() / 64] |= 1 << (id.index() % 64);
    }

    fn clear(&mut self, id: Id<T>) {
        self.words[id.index() / 64] &= !(1 << (id.index() % 64));
    }

    fn has(&self, id: Id<T>) -> bool {
        self.words[id.index() / 64] >> (id.index() % 64) & 1 != 0
    }

    /// The set whose words are `op` of the two sets' words: `|` for the
    /// union, `&` for the ids in both.
    fn combine(&self, other: &Bits<T>, op: fn(u64, u64) -> u64) -> Bits<T> {
        let words = self.words.iter().zip(&other.words);
        Bits {
            words: words.map(|(&a, &b)| op(a, b)).collect(),
            kind: PhantomData,
        }
    }
}

/// Where `variable` was moved on the paths to the instruction at `position`
/// of `block` that do not set it again: searched back only for an error.
fn move_sites(
    body: &mir::Body,
    variable: Id<mir::Variable>,
    block: Id<mir::Block>,
    position: usize,
) -> Vec<Span> {
    let predecessors = predecessors(body);
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
                mir::InstructionKind::Move(ref moved)
                    if moved.whole_variable() == Some(variable) =>
                {
                    Some(Some(body.instructions.span(instruction)))
                }
                mir::InstructionKind::Store { ref place, .. }
                    if place.whole_variable() == Some(variable) =>
                {
                    Some(None)
                }
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
                mir::Terminator::Return { span, .. }
                | mir::Terminator::Panic { span, .. }
                | mir::Terminator::Branch { span, .. } => Some(span),
                mir::Terminator::Goto(_) | mir::Terminator::FallOff(_) => None,
            };
            let spans = body.lists.items(instructions);
            let spans = spans
                .filter(|&instruction| body.instructions[instruction].kind.runs())
                .map(|instruction| body.instructions.span(instruction));
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
/// to there; `on_some_path` when some path did set it. A use that sets a
/// `written` part, such as "a field", needs the whole value set.
fn unset_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    on_some_path: bool,
    written: Option<&str>,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    let here = if on_some_path {
        format!("`{name}` is not set on every path to here")
    } else {
        format!("`{name}` is not set yet here")
    };
    let message = match written {
        Some(part) => format!("cannot set {part} of `{name}` before `{name}` is set"),
        None => format!("`{name}` is used before it is set"),
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
/// of `moves`; a use that sets a `written` part, such as "a field".
fn moved_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    moves: &[Span],
    written: Option<&str>,
) -> Diagnostic {
    let name = file.slice(body.variables.span(variable));
    let message = match written {
        Some(part) => format!("cannot set {part} of `{name}` after `{name}` was moved"),
        None => format!("`{name}` is used after it was moved"),
    };
    let mut error = Diagnostic::new(diagnostics::USED_AFTER_MOVE, message).label(
        file,
        span,
        format!("`{name}` is used here"),
    );
    for &moved in moves {
        error = error.label(file, moved, format!("`{name}` is moved here"));
    }
    // Code runs in the order written except where a loop goes back, so a
    // move written at or after the use reaches it from an earlier pass.
    if moves.iter().any(|moved| moved.start >= span.start) {
        error = error.note("it was moved in an earlier pass of the loop");
    }
    error
        .note("a value moves when used by value, unless its type copies")
        .help(
            format!("set `{name}` again before this use, or stop moving it"),
            Vec::new(),
        )
}

/// The error for a value whose type moves, read by value at `span` from
/// `place`, a part of its variable: a value cannot be moved out of its
/// struct or array, nor out of what a reference or raw pointer points to.
fn partial_move(file: &Arc<SourceFile>, span: Span, place: &mir::Place) -> Diagnostic {
    let (message, help) = if place.is_behind_reference() {
        (
            "cannot move out of a reference or pointer".to_string(),
            "borrow it instead, or read only parts that copy".to_string(),
        )
    } else {
        let [part, whole] = part(place);
        (
            format!("cannot move {part} out of its {whole}"),
            format!("move the whole {whole}, or read only parts that copy"),
        )
    };
    Diagnostic::new(diagnostics::PARTIAL_MOVE, message)
        .label(file, span, "this value's type moves")
        .help(help, Vec::new())
}

/// The part of a value that `place`, not a whole variable, names last, and
/// what holds it: "a field" of a "struct" or "an element" of an "array".
fn part(place: &mir::Place) -> [&'static str; 2] {
    match place.projections.last() {
        Some(mir::Projection::Index(_)) => ["an element", "array"],
        _ => ["a field", "struct"],
    }
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
