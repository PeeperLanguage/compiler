//! The loan check (T0037): a place used in a way that a reference to it
//! forbids while that reference is still in use, and a variable whose scope
//! ends while a reference to it is still in use.
//!
//! A loan is one `Borrow`. A holder is a variable or an instruction value of
//! reference type: it holds the loans it may refer through. A loan is in use
//! after a point when some holder that holds it is used again later, so a
//! borrow lasts until the last use of the reference, not to its block's end.
//!
//! Two dataflow passes, then one walk:
//!
//! 1. Forward: the loans each holder may hold. A `Borrow` holds its own
//!    loan and, when it goes through a reference (`&mut r.*`), the loans of
//!    `r`, so what `r` refers to stays borrowed while the new reference is in
//!    use. Copying a reference copies its loans; a `store` to a variable
//!    replaces them.
//! 2. Backward: the holders used again later (liveness).
//! 3. Each access, checked against the loans of the holders in use after it.
//!
//! A shared loan forbids changing, moving, ending and `&mut` borrowing its
//! place; a `&mut` loan forbids every access. Places conflict when one's
//! steps start with the other's (`p` and `p.x`, not `p.x` and `p.y`).
//! Changing a variable or ending its scope only touches its own storage, so
//! a loan reached through the reference stored there is not affected.

use std::collections::VecDeque;
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

/// A variable or instruction value of reference type, numbered from 0 in
/// that order. Only named by `Id<Holder>`.
enum Holder {}

/// One `Borrow`, numbered from 0 in instruction order. Only named by
/// `Id<Loan>`.
enum Loan {}

/// Reports each access that a reference still in use forbids, once per
/// loan; a body without a `Borrow` has no loan and is not walked.
pub fn report_loan_errors(
    body: &mir::Body,
    types: &Types,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let borrows: Vec<Id<mir::Instruction>> = body
        .instructions
        .ids()
        .filter(|&id| {
            matches!(
                body.instructions[id].kind,
                mir::InstructionKind::Borrow { .. }
            )
        })
        .collect();
    if borrows.is_empty() {
        return;
    }
    let variables = body.variables.ids().map(|id| body.variables[id].ty);
    let values = body.instructions.ids().map(|id| body.instructions[id].ty);
    let mut holder_count = 0;
    let mut holders = Vec::with_capacity(variables.len() + values.len());
    for ty in variables.chain(values) {
        match types[ty] {
            Type::Reference { .. } => {
                holders.push(Some(Id::from_index(holder_count)));
                holder_count += 1;
            }
            _ => holders.push(None),
        }
    }
    let loans = Loans {
        body,
        borrows,
        holders,
        holder_count,
    };
    loans.report(file, diagnostics);
}

/// One function's loans and holders.
struct Loans<'b> {
    body: &'b mir::Body,
    borrows: Vec<Id<mir::Instruction>>, // each loan's `Borrow`, by `Id<Loan>`
    /// The holder of each variable, then of each instruction's value;
    /// `None` for one that is not a reference.
    holders: Vec<Option<Id<Holder>>>,
    holder_count: usize,
}

impl Loans<'_> {
    fn variable_holder(&self, variable: Id<mir::Variable>) -> Option<Id<Holder>> {
        self.holders[variable.index()]
    }

    fn value_holder(&self, value: Id<mir::Instruction>) -> Option<Id<Holder>> {
        self.holders[self.body.variables.len() + value.index()]
    }

    fn operand_holder(&self, operand: &mir::Operand) -> Option<Id<Holder>> {
        match *operand {
            mir::Operand::Value(value) => self.value_holder(value),
            mir::Operand::Constant(_) | mir::Operand::Argument(_) => None,
        }
    }

    /// The holders the instruction `id` uses: the variable of a place it
    /// reads or goes through, and each operand.
    fn uses(&self, id: Id<mir::Instruction>, mut used: impl FnMut(Id<Holder>)) {
        let kind = &self.body.instructions[id].kind;
        let (place, operands): (Option<&mir::Place>, &[mir::Operand]) = match *kind {
            mir::InstructionKind::Load(ref place)
            | mir::InstructionKind::Move(ref place)
            | mir::InstructionKind::Borrow { ref place, .. } => (Some(place), &[]),
            // Setting a whole variable does not read it.
            mir::InstructionKind::Store {
                ref place,
                ref value,
            } => (
                place.whole_variable().is_none().then_some(place),
                std::slice::from_ref(value),
            ),
            mir::InstructionKind::Binary {
                ref left,
                ref right,
                ..
            } => {
                for operand in [left, right] {
                    if let Some(holder) = self.operand_holder(operand) {
                        used(holder);
                    }
                }
                (None, &[])
            }
            mir::InstructionKind::Unary { ref value, .. }
            | mir::InstructionKind::Widen(ref value)
            | mir::InstructionKind::Field { ref value, .. } => (None, std::slice::from_ref(value)),
            mir::InstructionKind::Call { ref arguments, .. }
            | mir::InstructionKind::Aggregate(ref arguments) => (None, arguments),
            mir::InstructionKind::Phi(ref incoming) => {
                for (_, operand) in incoming {
                    if let Some(holder) = self.operand_holder(operand) {
                        used(holder);
                    }
                }
                (None, &[])
            }
            mir::InstructionKind::ScopeEnd(_) => (None, &[]),
        };
        let place = place.and_then(|place| self.variable_holder(place.variable));
        let operands = operands
            .iter()
            .filter_map(|operand| self.operand_holder(operand));
        place.into_iter().chain(operands).for_each(used);
    }

    /// The holders the instruction `id` gives a new value: its own value,
    /// and a whole variable it stores to.
    fn defines(&self, id: Id<mir::Instruction>) -> [Option<Id<Holder>>; 2] {
        let stored = match self.body.instructions[id].kind {
            mir::InstructionKind::Store { ref place, .. } => place
                .whole_variable()
                .and_then(|variable| self.variable_holder(variable)),
            _ => None,
        };
        [self.value_holder(id), stored]
    }

    /// `held`, the loans of each holder, after the instruction `id`.
    fn step_held(&self, held: &mut [Bits<Loan>], id: Id<mir::Instruction>) {
        // The holder given new loans, the holder whose loans it takes, and
        // a loan of its own. A reference read from a variable, or borrowed
        // through one, holds that variable's loans.
        let (target, source, own) = match self.body.instructions[id].kind {
            mir::InstructionKind::Store {
                ref place,
                ref value,
            } => (
                place
                    .whole_variable()
                    .and_then(|variable| self.variable_holder(variable)),
                self.operand_holder(value),
                None,
            ),
            mir::InstructionKind::Load(ref place) | mir::InstructionKind::Move(ref place) => (
                self.value_holder(id),
                self.variable_holder(place.variable),
                None,
            ),
            mir::InstructionKind::Borrow { ref place, .. } => {
                let through = place.is_behind_reference();
                let loan = self
                    .borrows
                    .partition_point(|borrow| borrow.index() < id.index());
                let loan = Id::<Loan>::from_index(loan);
                (
                    self.value_holder(id),
                    through
                        .then(|| self.variable_holder(place.variable))
                        .flatten(),
                    Some(loan),
                )
            }
            _ => (self.value_holder(id), None, None),
        };
        let Some(target) = target else {
            return;
        };
        held[target.index()] = match source {
            Some(source) => held[source.index()].clone(),
            None => Bits::new(self.borrows.len()),
        };
        if let Some(loan) = own {
            held[target.index()].set(loan);
        }
    }

    /// `live`, the holders used later, before the instruction `id` runs.
    fn step_live(&self, live: &mut Bits<Holder>, id: Id<mir::Instruction>) {
        for holder in self.defines(id).into_iter().flatten() {
            live.clear(holder);
        }
        self.uses(id, |holder| live.set(holder));
    }

    fn report(&self, file: &Arc<SourceFile>, diagnostics: &mut Vec<Diagnostic>) {
        let body = self.body;
        let instructions =
            |block: Id<mir::Block>| body.lists.items(body.blocks[block].instructions);
        let no_holders = Bits::new(self.holder_count);
        let no_loans = vec![Bits::new(self.borrows.len()); self.holder_count];
        let starts = dataflow(
            body.blocks.len(),
            [(Id::from_index(0), no_loans)],
            |block| body.blocks[block].terminator.targets(),
            |block, held| {
                for id in instructions(block) {
                    self.step_held(held, id);
                }
            },
            |a: &Vec<Bits<Loan>>, b: &Vec<Bits<Loan>>| {
                let pairs = a.iter().zip(b);
                pairs.map(|(a, b)| a.combine(b, |a, b| a | b)).collect()
            },
        );
        let predecessors = predecessors(body);
        let exits = body.blocks.ids();
        let exits = exits.filter(|&block| body.blocks[block].terminator.targets().next().is_none());
        let ends = dataflow(
            body.blocks.len(),
            exits.map(|block| (block, no_holders.clone())),
            |block| predecessors[block.index()].iter().copied(),
            |block, live| {
                let ids: Vec<_> = instructions(block).collect();
                for &id in ids.iter().rev() {
                    self.step_live(live, id);
                }
            },
            |a: &Bits<Holder>, b: &Bits<Holder>| a.combine(b, |a, b| a | b),
        );
        let mut reported = Bits::new(self.borrows.len());
        let mut errors = Vec::new();
        for ((block, start), end) in body.blocks.ids().zip(starts).zip(ends) {
            let Some(mut held) = start else {
                continue; // not reached
            };
            let ids: Vec<_> = instructions(block).collect();
            // The holders used later, after each instruction.
            let mut live = end.unwrap_or_else(|| no_holders.clone());
            let mut live_after = vec![no_holders.clone(); ids.len()];
            for (position, &id) in ids.iter().enumerate().rev() {
                live_after[position] = live.clone();
                self.step_live(&mut live, id);
            }
            for (position, &id) in ids.iter().enumerate() {
                let error = self.conflict(id, &held, &live_after[position], &mut reported, file);
                if let Some((holder, error)) = error {
                    let later = self.next_use(holder, block, position);
                    let error = match later {
                        Some(later) => error.label(file, later, "reference used later here"),
                        None => error,
                    };
                    errors.push((body.instructions.span(id).start, error));
                }
                self.step_held(&mut held, id);
            }
        }
        errors.sort_by_key(|&(start, _)| start);
        diagnostics.extend(errors.into_iter().map(|(_, error)| error));
    }

    /// The first loan not yet `reported` that the access `id` breaks, held
    /// in `held` by a holder `live` after it other than one it sets: that
    /// holder, and the error without where the holder is used later.
    fn conflict(
        &self,
        id: Id<mir::Instruction>,
        held: &[Bits<Loan>],
        live: &Bits<Holder>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let ended;
        // The place, what happens to it, whether that only reads it, and
        // whether it replaces or ends the place's own storage.
        let (place, event, reads, replaces) = match self.body.instructions[id].kind {
            mir::InstructionKind::Load(ref place) => (place, "read", true, false),
            mir::InstructionKind::Move(ref place) => (place, "moved", false, false),
            mir::InstructionKind::Store { ref place, .. } => (place, "changed", false, true),
            mir::InstructionKind::Borrow {
                ref place,
                mutable: false,
            } => (place, "borrowed", true, false),
            mir::InstructionKind::Borrow {
                ref place,
                mutable: true,
            } => (place, "borrowed as `&mut`", false, false),
            mir::InstructionKind::ScopeEnd(variable) => {
                ended = mir::Place::whole(variable);
                (&ended, "gone", false, true)
            }
            _ => return None,
        };
        let defined = self.defines(id);
        let holders = (0..self.holder_count).map(Id::from_index);
        let holders =
            holders.filter(|&holder| live.has(holder) && !defined.contains(&Some(holder)));
        for holder in holders {
            let loans = (0..self.borrows.len()).map(Id::from_index);
            let held = &held[holder.index()];
            for loan in loans.filter(|&loan| held.has(loan) && !reported.has(loan)) {
                let borrow = self.borrows[loan.index()];
                let mir::InstructionKind::Borrow {
                    place: ref loaned,
                    mutable,
                } = self.body.instructions[borrow].kind
                else {
                    unreachable!("each loan is a `Borrow`")
                };
                let common = place.projections.len().min(loaned.projections.len());
                let overlaps = place.variable == loaned.variable
                    && place.projections[..common] == loaned.projections[..common];
                let past_storage = replaces
                    && loaned.projections[common..].contains(&mir::Projection::Dereference);
                if !overlaps || past_storage || (reads && !mutable) {
                    continue;
                }
                reported.set(loan);
                let name = file.slice(self.body.variables.span(place.variable));
                let reference = if mutable {
                    "a `&mut` reference"
                } else {
                    "a reference"
                };
                let message =
                    format!("`{name}` is {event} while {reference} to it is still in use");
                let error = Diagnostic::new(diagnostics::BORROW_CONFLICT, message)
                    .label(
                        file,
                        self.body.instructions.span(id),
                        format!("{event} here"),
                    )
                    .label(
                        file,
                        self.body.instructions.span(borrow),
                        "reference made here",
                    );
                return Some((holder, error));
            }
        }
        None
    }

    /// Where `holder` is next used after the instruction at `position` of
    /// `block`, on the nearest path that does not set it again: searched
    /// only for an error.
    fn next_use(&self, holder: Id<Holder>, block: Id<mir::Block>, position: usize) -> Option<Span> {
        let body = self.body;
        let mut seen = vec![false; body.blocks.len()];
        let mut pending = VecDeque::from([(block, position + 1)]);
        'paths: while let Some((block, from)) = pending.pop_front() {
            for id in body.lists.items(body.blocks[block].instructions).skip(from) {
                let mut used = false;
                self.uses(id, |used_holder| used |= used_holder == holder);
                if used {
                    return Some(body.instructions.span(id));
                }
                if self.defines(id).contains(&Some(holder)) {
                    continue 'paths; // set again: this path does not use it
                }
            }
            for target in body.blocks[block].terminator.targets() {
                if !std::mem::replace(&mut seen[target.index()], true) {
                    pending.push_back((target, 0));
                }
            }
        }
        None
    }
}
