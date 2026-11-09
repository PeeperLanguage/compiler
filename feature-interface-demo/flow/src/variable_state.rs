//! The variable state check (T0039, T0034, T0035 and T0007): a variable used
//! where some path left it unset or moved it, a move out of a part, and a
//! variable without `mut` set where some path already set it.

use model::mir::Body;
use model::mir::InstructionKind;
use model::mir::Place;
use model::mir::VariableId;

use crate::Bits;
use crate::Diagnostic;
use crate::dataflow;
use crate::slice;

/// Reports each variable used where it may not be set yet or was moved, and
/// each one without `mut` set where it may already be set, once per
/// variable; and each move out of a part. Only a `let` without a value is
/// unset at first, and only a move unsets a variable, so only a variable
/// with one of the two is followed; a function with neither is not walked.
pub(crate) fn report(body: &Body, source: &str, errors: &mut Vec<Diagnostic>) {
    let mut moved = vec![false; body.variables.len()];
    let mut partial_moves = false;
    for instruction in &body.instructions {
        if let InstructionKind::Move(place) = &instruction.kind {
            match place.whole_variable() {
                Some(variable) => moved[variable.0 as usize] = true,
                None => partial_moves = true,
            }
        }
    }
    let mut followed = vec![None; body.variables.len()];
    let mut count = 0;
    for (index, variable) in body.variables.iter().enumerate() {
        if !variable.initialized || moved[index] {
            followed[index] = Some(count);
            count += 1;
        }
    }
    if count == 0 && !partial_moves {
        return;
    }
    let tracked = |variable: VariableId| followed[variable.0 as usize];
    let starts = dataflow(
        body.blocks.len(),
        [(model::mir::BlockId(0), Facts::new(count))],
        |block| body.blocks[block.0 as usize].terminator.targets(),
        |block, facts| {
            for id in &body.blocks[block.0 as usize].instructions {
                facts.step(&body.instructions[id.0 as usize].kind, tracked);
            }
        },
        Facts::join,
    );
    let mut reported = vec![false; body.variables.len()];
    let mut found = Vec::new();
    for (block, start) in body.blocks.iter().zip(starts) {
        let Some(mut facts) = start else {
            continue;
        };
        for id in &block.instructions {
            let instruction = &body.instructions[id.0 as usize];
            let span = instruction.span;
            let name =
                |variable: VariableId| slice(source, body.variables[variable.0 as usize].span);
            let error = match &instruction.kind {
                InstructionKind::Move(place) if place.whole_variable().is_none() => {
                    found.push(Diagnostic {
                        code: "T0035",
                        message: partial_move(place),
                        span,
                    });
                    None
                }
                InstructionKind::Store { place, .. }
                    if let Some(variable) = place.whole_variable()
                        && let Some(bit) = tracked(variable)
                        && facts.maybe.has(bit)
                        && !body.variables[variable.0 as usize].mutable =>
                {
                    let message = format!("cannot assign twice to immutable `{}`", name(variable));
                    Some((variable, "T0007", message))
                }
                InstructionKind::Load(place)
                | InstructionKind::Move(place)
                | InstructionKind::Store { place, .. }
                | InstructionKind::Borrow { place, .. }
                    if let Some(bit) = tracked(place.variable)
                        && !facts.always.has(bit)
                        && !matches!(&instruction.kind, InstructionKind::Store { .. } if place.whole_variable().is_some()) =>
                {
                    let variable = place.variable;
                    let name = name(variable);
                    // Writing through a reference only reads the variable.
                    let writes_part = matches!(&instruction.kind, InstructionKind::Store { .. })
                        && !place.is_behind_reference();
                    let error = match (facts.moved.has(bit), writes_part) {
                        (true, true) => (
                            "T0034",
                            format!("cannot set a field of `{name}` after `{name}` was moved"),
                        ),
                        (true, false) => ("T0034", format!("`{name}` is used after it was moved")),
                        (false, true) => (
                            "T0039",
                            format!("cannot set a field of `{name}` before `{name}` is set"),
                        ),
                        (false, false) => ("T0039", format!("`{name}` is used before it is set")),
                    };
                    Some((variable, error.0, error.1))
                }
                _ => None,
            };
            facts.step(&instruction.kind, tracked);
            if let Some((variable, code, message)) = error
                && !std::mem::replace(&mut reported[variable.0 as usize], true)
            {
                found.push(Diagnostic {
                    code,
                    message,
                    span,
                });
            }
        }
    }
    found.sort_by_key(|error| error.span.start);
    errors.extend(found);
}

/// A move out of `place`, a part of its variable: out of what a reference
/// points to, or out of the struct that holds it.
fn partial_move(place: &Place) -> String {
    match place.is_behind_reference() {
        true => "cannot move out of a reference or pointer".to_string(),
        false => "cannot move a field out of its struct".to_string(),
    }
}

/// What is known about each followed variable at one point of a function,
/// one bit per followed variable in each set.
#[derive(Clone, PartialEq)]
struct Facts {
    /// Set on every path to here (joined with "and").
    always: Bits,
    /// Set on some path to here, even if moved since ("or").
    maybe: Bits,
    /// Moved on some path to here and not set since ("or").
    moved: Bits,
}

impl Facts {
    fn new(count: usize) -> Facts {
        Facts {
            always: Bits::new(count),
            maybe: Bits::new(count),
            moved: Bits::new(count),
        }
    }

    /// The facts after `kind` runs: a `store` to a whole variable sets it,
    /// a `move` of one unsets it, and the end of its scope forgets it, so a
    /// loop's next pass declares it anew.
    fn step(&mut self, kind: &InstructionKind, tracked: impl Fn(VariableId) -> Option<usize>) {
        match kind {
            InstructionKind::Store { place, .. }
                if let Some(bit) = place.whole_variable().and_then(&tracked) =>
            {
                self.always.set(bit);
                self.maybe.set(bit);
                self.moved.clear(bit);
            }
            InstructionKind::Move(place)
                if let Some(bit) = place.whole_variable().and_then(&tracked) =>
            {
                self.always.clear(bit);
                self.moved.set(bit);
            }
            InstructionKind::ScopeEnd(variable) if let Some(bit) = tracked(*variable) => {
                self.always.clear(bit);
                self.maybe.clear(bit);
                self.moved.clear(bit);
            }
            _ => {}
        }
    }

    fn join(&self, other: &Facts) -> Facts {
        Facts {
            always: self.always.combine(&other.always, |a, b| a & b),
            maybe: self.maybe.combine(&other.maybe, |a, b| a | b),
            moved: self.moved.combine(&other.moved, |a, b| a | b),
        }
    }
}
