//! The variable state check (T0039, T0034, T0035 and T0007): a variable used
//! where some path left it unset or moved it, a move out of a field or
//! element, and a variable without `mut` assigned where some path already
//! set it.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

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
