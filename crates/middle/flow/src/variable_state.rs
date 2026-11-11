//! The variable state check (T0039, T0034, T0035 and T0007): a variable used
//! where some path left it unset or moved it, a move out of a field or
//! element, and a variable without `mut` assigned where some path already
//! set it.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::mir::AccessKind;
use model::mir::InstructionKind;
use model::mir::PlaceRef;
use model::mir::Root;
use model::mir::Slot;
use model::mir::Step;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

/// A slot the check follows, numbered from 0 in slot order. Only named by
/// `Id<Tracked>`.
enum Tracked {}

/// Reports each variable used where it may not be set yet or was moved, and
/// each variable without `mut` assigned where it may already be set, once per
/// variable; and each move out of a field. Only a `let` without a value is
/// unset at first, and only a move unsets a variable or takes a part of a
/// place. So only a slot with one of the two is tracked: any other is set
/// wherever it is in scope, and the checker refuses a second assignment to
/// one without `mut`. A function with neither is not walked.
///
/// A slot declared with a value is set from the start and again where its
/// storage ends (`Dead`), as the next pass of a loop declares it anew: its
/// value may be built in place, a part at a time, with no whole write.
///
/// Forward dataflow over the reachable blocks: `dataflow` finds the `Facts`
/// at each block's start; the loop walks each block again from them and
/// reports at each use and whole write.
pub fn report_variable_state_errors(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let starts_set = |slot| body.slots[slot].initialized;
    let moves = |id| matches!(body.instructions[id].kind, InstructionKind::Moved { .. });
    if body.slots.ids().all(starts_set) && !body.instructions.ids().any(moves) {
        return;
    }
    let (tracked, tracked_count) = tracked_slots(body);
    let tracked = |slot: Id<Slot>| tracked[slot.index()];
    let mut entry = Facts::new(tracked_count);
    for slot in body.slots.ids().filter(|&slot| starts_set(slot)) {
        if let Some(followed) = tracked(slot) {
            entry.declare(followed, true);
        }
    }
    let instructions = |block: Id<mir::Block>| body.lists.items(body.blocks[block].instructions);
    let starts = dataflow(
        body.blocks.len(),
        [(Id::from_index(0), entry)],
        |block| body.blocks[block].terminator.targets(),
        |block, facts| {
            for instruction in instructions(block) {
                facts.step(body, &body.instructions[instruction].kind, tracked);
            }
        },
        Facts::join,
    );
    let mut reported = vec![false; body.slots.len()];
    let mut errors = Vec::new();
    for (block, start) in body.blocks.ids().zip(starts) {
        let Some(mut facts) = start else {
            continue;
        };
        for (position, instruction) in instructions(block).enumerate() {
            let span = body.instructions.span(instruction);
            let kind = &body.instructions[instruction].kind;
            if let InstructionKind::Moved {
                place, lent_again, ..
            } = kind
                && place.as_place_ref().whole_slot().is_none()
            {
                let place = place.as_place_ref();
                errors.push((span.start, partial_move(file, span, place, *lent_again)));
            }
            kind.accesses(|access| {
                let writes = match access.kind {
                    AccessKind::Write => true,
                    AccessKind::Read | AccessKind::Borrow { .. } | AccessKind::Address => false,
                    AccessKind::Move => return, // the read before it is the use
                    AccessKind::End => return,
                };
                let Root::Slot(slot) = access.place.root else {
                    return;
                };
                let Some(followed) = tracked(slot) else {
                    return;
                };
                let error = if writes && access.place.whole_slot().is_some() {
                    // A slot declared with a value: the checker refuses a
                    // second assignment to one without `mut`.
                    let declared = &body.slots[slot];
                    let assigned =
                        facts.maybe.has(followed) && !declared.mutable && !declared.initialized;
                    assigned.then(|| {
                        let set = if facts.always.has(followed) {
                            "is already set here"
                        } else {
                            "may already be set here"
                        };
                        second_assignment(body, file, slot, span, set)
                    })
                } else if !facts.always.has(followed) {
                    // Writing through a reference only reads the variable.
                    let writes_part = writes && !access.place.is_behind_pointer();
                    let written = writes_part.then(|| part_and_holder(access.place)[0]);
                    Some(if facts.moved.has(followed) {
                        let moves = move_sites(body, slot, block, position);
                        moved_use(body, file, slot, span, &moves, written)
                    } else {
                        let state = if facts.maybe.has(followed) {
                            "is not set on every path to here"
                        } else {
                            "is not set yet here"
                        };
                        unset_use(body, file, slot, span, state, written)
                    })
                } else {
                    None
                };
                if let Some(error) = error
                    && !std::mem::replace(&mut reported[slot.index()], true)
                {
                    errors.push((span.start, error));
                }
            });
            facts.step(body, kind, tracked);
        }
    }
    errors.sort_by_key(|&(start, _)| start);
    diagnostics.extend(errors.into_iter().map(|(_, error)| error));
}

/// Each slot's number if the check follows it: its `let` has no value, or a
/// `Moved` takes it whole somewhere. Also how many are followed.
fn tracked_slots(body: &mir::Body) -> (Vec<Option<Id<Tracked>>>, usize) {
    let mut moved = vec![false; body.slots.len()];
    for id in body.instructions.ids() {
        body.instructions[id].kind.accesses(|access| {
            if let (AccessKind::Move, Some(slot)) = (access.kind, access.place.whole_slot()) {
                moved[slot.index()] = true;
            }
        });
    }
    let mut count = 0;
    let tracked = (body.slots.ids())
        .map(|slot| {
            let follows = !body.slots[slot].initialized || moved[slot.index()];
            follows.then(|| {
                count += 1;
                Id::from_index(count - 1)
            })
        })
        .collect();
    (tracked, count)
}

/// What is known about each tracked slot at one point of a function, one bit
/// per tracked slot in each set.
#[derive(Clone, PartialEq)]
struct Facts {
    always: Bits<Tracked>, // set on every path to here (joined with "and")
    maybe: Bits<Tracked>,  // set on some path to here, even if moved since ("or")
    moved: Bits<Tracked>,  // moved on some path to here and not set since ("or")
}

impl Facts {
    /// Nothing set or moved, for `count` tracked slots.
    fn new(count: usize) -> Facts {
        let none = Bits::new(count);
        Facts {
            always: none.clone(),
            maybe: none.clone(),
            moved: none,
        }
    }

    /// `followed` where it is declared: set when declared with a value, else
    /// unset; never moved.
    fn declare(&mut self, followed: Id<Tracked>, set: bool) {
        if set {
            self.always.set(followed);
            self.maybe.set(followed);
        } else {
            self.always.clear(followed);
            self.maybe.clear(followed);
        }
        self.moved.clear(followed);
    }

    /// The facts after `kind` runs in `body`, `tracked` giving each slot's
    /// number if it is tracked: a whole write sets it, a `Moved` of all of it
    /// unsets it, and the end of its storage declares it anew for the next
    /// pass of a loop.
    fn step(
        &mut self,
        body: &mir::Body,
        kind: &InstructionKind,
        tracked: impl Fn(Id<Slot>) -> Option<Id<Tracked>>,
    ) {
        kind.accesses(|access| {
            let Some(slot) = access.place.whole_slot() else {
                return;
            };
            let Some(followed) = tracked(slot) else {
                return;
            };
            match access.kind {
                AccessKind::Write => self.declare(followed, true),
                AccessKind::Move => {
                    self.always.clear(followed);
                    self.moved.set(followed);
                }
                AccessKind::End => self.declare(followed, body.slots[slot].initialized),
                AccessKind::Read | AccessKind::Borrow { .. } | AccessKind::Address => {}
            }
        });
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

/// Where `slot` was moved on the paths to the instruction at `position` of
/// `block` that do not set it again or end it: searched back only for an
/// error.
fn move_sites(
    body: &mir::Body,
    slot: Id<Slot>,
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
        // The last move, whole write or end of `slot` before `end`: the move
        // is a site; the others end the path.
        let last = before.iter().rev().find_map(|&instruction| {
            let mut last = None;
            body.instructions[instruction].kind.accesses(|access| {
                if access.place.whole_slot() != Some(slot) {
                    return;
                }
                match access.kind {
                    AccessKind::Move => last = Some(Some(body.instructions.span(instruction))),
                    AccessKind::Write | AccessKind::End => last = Some(None),
                    AccessKind::Read | AccessKind::Borrow { .. } | AccessKind::Address => {}
                }
            });
            last
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

/// The error for `slot`, used at `span` where it is not set on every path to
/// there, as `state` says. A use that sets a `written` part, such as "a
/// field", needs the whole value set.
fn unset_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    slot: Id<Slot>,
    span: Span,
    state: &str,
    written: Option<&str>,
) -> Diagnostic {
    let declared = body.slots.span(slot);
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

/// The error for `slot`, used at `span` where some path moved it at one of
/// `moves`; a use that sets a `written` part, such as "a field".
fn moved_use(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    slot: Id<Slot>,
    span: Span,
    moves: &[Span],
    written: Option<&str>,
) -> Diagnostic {
    let name = file.slice(body.slots.span(slot));
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

/// The error for a value that moves, read by value at `span` from `place`, a
/// part of its slot: a value cannot be moved out of its struct or array, nor
/// out of what a reference or raw pointer points to. A `&mut` or `&mut []`
/// there can be lent again instead, with `lent_again` after it.
fn partial_move(
    file: &Arc<SourceFile>,
    span: Span,
    place: PlaceRef,
    lent_again: Option<&str>,
) -> Diagnostic {
    let (message, help) = if place.is_behind_pointer() {
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
    let text = file.slice(span);
    let help = lent_again.map_or(help, |step| {
        format!("lend it again with `&mut {text}{step}`, or `&{text}{step}` to only read")
    });
    Diagnostic::new(diagnostics::PARTIAL_MOVE, message)
        .label(file, span, "this value's type moves")
        .help(help, Vec::new())
}

/// The part of a value that `place`, not a whole slot, names last, and what
/// holds it: "a field" of a "struct" or "an element" of an "array".
fn part_and_holder(place: PlaceRef) -> [&'static str; 2] {
    match place.steps.last() {
        Some(Step::Index { .. }) => ["an element", "array"],
        _ => ["a field", "struct"],
    }
}

/// The error for `slot`, without `mut`, assigned at `span` where some path
/// already set it, as `set` says.
fn second_assignment(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    slot: Id<Slot>,
    span: Span,
    set: &str,
) -> Diagnostic {
    let declared = body.slots.span(slot);
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
