//! The variable state check (T0039, T0034, T0035 and T0007): a variable used
//! where some path left it unset or moved it, a move out of a field or
//! element, and a variable without `mut` assigned where some path already
//! set it.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

/// A variable the check follows, numbered from 0 in variable order. Only
/// named by `Id<Tracked>`.
enum Tracked {}

/// Reports each variable used where it may not be set yet or was moved, and
/// each variable without `mut` assigned where it may already be set, once per
/// variable; and each move out of a field. Only a `let` without a value is
/// unset at first, and only a move unsets a variable or takes a part of a
/// place. So only a variable with one of the two is tracked: any other is set
/// wherever it is in scope, and the checker refuses a second assignment to
/// one without `mut`. A function with neither is not walked.
///
/// Forward dataflow over the reachable blocks: `dataflow` finds the `Facts`
/// at each block's start; the loop walks each block again from them and
/// reports at each use and `store`.
pub fn report_variable_state_errors(
    body: &mir::Body,
    types: &Types,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let starts_set = |variable| body.variables[variable].initialized;
    let moves = |id| matches!(body.instructions[id].kind, mir::InstructionKind::Move(_));
    if body.variables.ids().all(starts_set) && !body.instructions.ids().any(moves) {
        return;
    }
    let (tracked, tracked_count) = tracked_variables(body);
    let tracked = |variable: Id<mir::Variable>| tracked[variable.index()];
    let instructions = |block: Id<mir::Block>| body.lists.items(body.blocks[block].instructions);
    let starts = dataflow(
        body.blocks.len(),
        [(Id::from_index(0), Facts::new(tracked_count))],
        |block| body.blocks[block].terminator.targets(),
        |block, facts| {
            for instruction in instructions(block) {
                facts.step(&body.instructions[instruction].kind, tracked);
            }
        },
        Facts::join,
    );
    let mut reported = vec![false; body.variables.len()];
    let mut errors = Vec::new();
    for (block, start) in body.blocks.ids().zip(starts) {
        let Some(mut facts) = start else {
            continue;
        };
        for (position, instruction) in instructions(block).enumerate() {
            let span = body.instructions.span(instruction);
            let kind = &body.instructions[instruction].kind;
            let error = match kind {
                mir::InstructionKind::Move(place) if place.whole_variable().is_none() => {
                    let error =
                        partial_move(file, span, place, &types[body.instructions[instruction].ty]);
                    errors.push((span.start, error));
                    None
                }
                mir::InstructionKind::Store { place, .. }
                    if let Some(variable) = place.whole_variable()
                        && let Some(followed) = tracked(variable) =>
                {
                    let assigned = facts.maybe.has(followed) && !body.variables[variable].mutable;
                    assigned.then(|| {
                        let set = if facts.always.has(followed) {
                            "is already set here"
                        } else {
                            "may already be set here"
                        };
                        (variable, second_assignment(body, file, variable, span, set))
                    })
                }
                mir::InstructionKind::Load(place)
                | mir::InstructionKind::Length(place)
                | mir::InstructionKind::Move(place)
                | mir::InstructionKind::Store { place, .. }
                | mir::InstructionKind::Borrow { place, .. }
                | mir::InstructionKind::Address(place)
                    if let Some(followed) = tracked(place.variable)
                        && !facts.always.has(followed) =>
                {
                    let variable = place.variable;
                    // Writing through a reference only reads the variable.
                    let writes_part = matches!(kind, mir::InstructionKind::Store { .. })
                        && !place.is_behind_reference();
                    let written = writes_part.then(|| part_and_holder(place)[0]);
                    let error = if facts.moved.has(followed) {
                        let moves = move_sites(body, variable, block, position);
                        moved_use(body, file, variable, span, &moves, written)
                    } else {
                        let state = if facts.maybe.has(followed) {
                            "is not set on every path to here"
                        } else {
                            "is not set yet here"
                        };
                        unset_use(body, file, variable, span, state, written)
                    };
                    Some((variable, error))
                }
                _ => None,
            };
            facts.step(kind, tracked);
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
/// Each variable's number if the check follows it: its `let` has no value,
/// or a `move` takes it whole somewhere. Also how many are followed.
fn tracked_variables(body: &mir::Body) -> (Vec<Option<Id<Tracked>>>, usize) {
    let mut moved = vec![false; body.variables.len()];
    for id in body.instructions.ids() {
        if let mir::InstructionKind::Move(place) = &body.instructions[id].kind
            && let Some(variable) = place.whole_variable()
        {
            moved[variable.index()] = true;
        }
    }
    let mut count = 0;
    let tracked = (body.variables.ids())
        .map(|variable| {
            let follows = !body.variables[variable].initialized || moved[variable.index()];
            follows.then(|| {
                count += 1;
                Id::from_index(count - 1)
            })
        })
        .collect();
    (tracked, count)
}

/// What is known about each tracked variable at one point of a function, one
/// bit per tracked variable in each set.
#[derive(Clone, PartialEq)]
struct Facts {
    always: Bits<Tracked>, // set on every path to here (joined with "and")
    maybe: Bits<Tracked>,  // set on some path to here, even if moved since ("or")
    moved: Bits<Tracked>,  // moved on some path to here and not set since ("or")
}
impl Facts {
    /// Nothing set or moved, for `count` tracked variables.
    fn new(count: usize) -> Facts {
        let none = Bits::new(count);
        Facts {
            always: none.clone(),
            maybe: none.clone(),
            moved: none,
        }
    }

    /// The facts after `kind` runs, `tracked` giving each variable's number
    /// if it is tracked: a `store` to a whole variable sets it, a `move` of
    /// one unsets it, and the end of its scope forgets it, so a loop's next
    /// pass declares it anew.
    fn step(
        &mut self,
        kind: &mir::InstructionKind,
        tracked: impl Fn(Id<mir::Variable>) -> Option<Id<Tracked>>,
    ) {
        match kind {
            mir::InstructionKind::Store { place, .. }
                if let Some(followed) = place.whole_variable().and_then(&tracked) =>
            {
                self.always.set(followed);
                self.maybe.set(followed);
                self.moved.clear(followed);
            }
            mir::InstructionKind::Move(place)
                if let Some(followed) = place.whole_variable().and_then(&tracked) =>
            {
                self.always.clear(followed);
                self.moved.set(followed);
            }
            mir::InstructionKind::ScopeEnd(variable) if let Some(followed) = tracked(*variable) => {
                self.always.clear(followed);
                self.maybe.clear(followed);
                self.moved.clear(followed);
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
            match &body.instructions[instruction].kind {
                mir::InstructionKind::Move(moved) if moved.whole_variable() == Some(variable) => {
                    Some(Some(body.instructions.span(instruction)))
                }
                mir::InstructionKind::Store { place, .. }
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
/// to there, as `state` says. A use that sets a `written` part, such as "a
/// field", needs the whole value set.
fn unset_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    state: &str,
    written: Option<&str>,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    let message = match written {
        Some(part) => format!("cannot set {part} of `{name}` before `{name}` is set"),
        None => format!("`{name}` is used before it is set"),
    };
    Diagnostic::new(diagnostics::USED_BEFORE_SET, message)
        .label(file, span, format!("`{name}` {state}"))
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
/// The error for a value of the type `moved`, which moves, read by value at
/// `span` from `place`, a part of its variable: a value cannot be moved out
/// of its struct or array, nor out of what a reference or raw pointer points
/// to. A `&mut` or `&mut []` there can be lent again instead.
fn partial_move(
    file: &Arc<SourceFile>,
    span: Span,
    place: &mir::Place,
    moved: &Type,
) -> Diagnostic {
    let (message, help) = if place.is_behind_reference() {
        (
            "cannot move out of a reference or pointer".to_string(),
            "borrow it instead, or read only parts that copy".to_string(),
        )
    } else {
        let [part, whole] = part_and_holder(place);
        (
            format!("cannot move {part} out of its {whole}"),
            format!("move the whole {whole}, or read only parts that copy"),
        )
    };
    // As the checker lends one again (`ThirBuilder::reborrow`).
    let lent_through = match *moved {
        Type::Reference { mutable: true, .. } | Type::InterfaceReference { mutable: true, .. } => {
            Some(".*")
        }
        Type::Slice { mutable: true, .. } => Some("[..]"),
        _ => None,
    };
    let text = file.slice(span);
    let help = lent_through.map_or(help, |step| {
        format!("lend it again with `&mut {text}{step}`, or `&{text}{step}` to only read")
    });
    Diagnostic::new(diagnostics::PARTIAL_MOVE, message)
        .label(file, span, "this value's type moves")
        .help(help, Vec::new())
}
/// The part of a value that `place`, not a whole variable, names last, and
/// what holds it: "a field" of a "struct" or "an element" of an "array".
fn part_and_holder(place: &mir::Place) -> [&'static str; 2] {
    match place.projections.last() {
        Some(mir::Projection::Index(_)) => ["an element", "array"],
        _ => ["a field", "struct"],
    }
}
/// The error for `variable`, without `mut`, assigned at `span` where some path
/// already set it, as `set` says.
fn second_assignment(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    variable: Id<mir::Variable>,
    span: Span,
    set: &str,
) -> Diagnostic {
    let declared = body.variables.span(variable);
    let name = file.slice(declared);
    Diagnostic::new(
        diagnostics::INVALID_ASSIGNMENT,
        format!("cannot assign twice to immutable `{name}`"),
    )
    .label(file, span, format!("`{name}` {set}"))
    .label(file, declared, "declared without `mut` here")
    .help(
        format!("write `let mut {name}` to assign it more than once"),
        Vec::new(),
    )
}
