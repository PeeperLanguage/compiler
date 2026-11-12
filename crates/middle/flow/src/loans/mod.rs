//! The loan check (T0037): a place used in a way that a reference to it
//! forbids while that reference is still in use, and a variable whose scope
//! ends while a reference to it is still in use.
//!
//! A loan is one `Borrow`. A holder is a pointer that may hold loans: one of
//! a slot's `holders` (a reference, slice or interface in a variable or
//! temporary, on its own or in a field, however deep), or the value of an
//! instruction that gives a pointer. So each field of a struct keeps its own
//! loans. Liveness is by holder too: reading `p.second` uses only the holders
//! of `p.second`, so what `p.first` points at may change meanwhile, and a
//! whole use of `p` uses them all. A loan is in use after a point when some
//! holder that holds it is used again later, so a borrow lasts until the
//! last use of the reference, not to its block's end.
//!
//! A place is byte ranges, one per level: in its slot, then in what each
//! pointer it goes through points to. The holders of a place are those whose
//! offset is in its first range. An index only the program knows may be any
//! element, so its range covers all of them.
//!
//! Two dataflow passes, then one walk, each over only the instructions that
//! can give, use or break a loan:
//!
//! 1. Backward: the holders used again later (liveness).
//! 2. Forward: the loans each holder may hold. A `Borrow` holds its own
//!    loan and, when it goes through a reference (`&mut r.*`), the loans of
//!    `r`, so what `r` refers to stays borrowed while the new reference is in
//!    use; borrowing a struct holds the loans of the holders in the place
//!    too, so what a reference to it reads out still counts them. Copying a
//!    reference copies its loans; a write to the bytes of exactly known
//!    holders replaces them and ends each loan of a place through them,
//!    which now names what the new reference points to. No write behind a
//!    reference holds a loan: the checker refuses it.
//!    Each block passes on only the loans of holders live at its end: a loan
//!    is read only through a holder that is used, so no error can need a
//!    dead holder's, and what is carried follows the references in use, not
//!    every reference the function ever made.
//! 3. Each access, checked against the loans of the holders in use after it.
//!
//! A shared loan forbids changing, moving, ending and `&mut` borrowing its
//! place; a `&mut` loan forbids every access. Places conflict when their
//! ranges meet at each level both reach (`p` and `p.x`, not `p.x` and
//! `p.y`). Changing a variable or ending its scope only touches its own
//! storage, so a loan reached through the reference stored there is not
//! broken.
//!
//! A `&mut` borrow used only as a call argument is reserved until the call
//! (two-phase): until then it acts as a shared loan, so the other arguments
//! may read the place (`update(&mut x, read(&x))`). When the call starts it
//! takes hold, and a reference to the place still in use then, the call's
//! other arguments included, is an error (`both(&mut x, &x)`). An argument
//! passed in memory is a temporary copy, so a borrow stored there and used
//! only by the call is reserved too.
//!
//! Each holder a call returns holds all the loans of the arguments its
//! function's origins name (`in`), so they stay borrowed while it is in use;
//! a `&mut` argument there is never reserved, as its loan outlives the call.
//! Each holder of a parameter holds a parameter loan, numbered after the
//! borrows, for what the caller lent; it conflicts with nothing here. A
//! returned value may hold only the parameter loans the origins name, and no
//! loan of the function's own storage (T0036). A result kept in memory is
//! one holder, which each write to it adds to.

mod errors;
mod holders;
mod passes;
mod places;
mod two_phase;

use std::ops::Range;
use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::mir::Class;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::Root;
use model::mir::Step;
use source::SourceFile;

use crate::Bits;
use crate::loans::two_phase::Reservation;

/// A pointer that may hold loans: each slot's holders in slot order, then
/// one for each instruction giving a pointer, then one for a result kept in
/// memory. Only named by `Id<Holder>`.
enum Holder {}

/// One `Borrow`, numbered from 0 in instruction order, then one parameter
/// loan for each parameter. Only named by `Id<Loan>`.
enum Loan {}

/// Reports each access that a reference still in use forbids, once per
/// loan, in `body`, for a machine whose pointers are `pointer_bytes` wide.
/// A body with no `Borrow` and a result that holds no loan has no loan to
/// break and is not walked.
pub fn report_loan_errors(
    body: &mir::Body,
    pointer_bytes: u64,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let borrows: Vec<Id<mir::Instruction>> = (body.instructions.ids())
        .filter(|&id| matches!(body.instructions[id].kind, InstructionKind::Borrow { .. }))
        .collect();
    if borrows.is_empty() && body.returned.is_none() {
        return;
    }
    let mut holder_count = 0;
    let mut holder_starts = Vec::with_capacity(body.slots.len() + body.instructions.len() + 2);
    holder_starts.push(holder_count);
    for slot in body.slots.ids() {
        holder_count += body.slots[slot].holders.len();
        holder_starts.push(holder_count);
    }
    for id in body.instructions.ids() {
        let gives_pointer = body.class_of(Operand::Value(id)) == Some(Class::Pointer);
        let holds = matches!(
            body.instructions[id].kind,
            InstructionKind::Borrow { .. }
                | InstructionKind::Load { .. }
                | InstructionKind::Call { .. }
        );
        holder_count += usize::from(gives_pointer && holds);
        holder_starts.push(holder_count);
    }
    let in_memory = matches!(body.signature.result, Some(Passing::Memory { .. }));
    holder_count += usize::from(in_memory && body.returned.is_some());
    holder_starts.push(holder_count);
    let mut loans_through = vec![Vec::new(); body.slots.len()];
    for (index, &borrow) in borrows.iter().enumerate() {
        if let InstructionKind::Borrow { place, .. } = &body.instructions[borrow].kind
            && let Root::Slot(slot) = place.root
            && place.steps.contains(&Step::Through)
        {
            loans_through[slot.index()].push(Id::from_index(index));
        }
    }
    let holders = Holders {
        body,
        pointer_bytes,
        borrows,
        holder_starts,
        loans_through,
    };
    let loans = Loans {
        reserved: holders.reserved_loans(),
        lines: holders.lines_that_matter(),
        holders,
    };
    loans.report(file, diagnostics);
}

/// The holders of `range` as ids.
fn holder_ids(range: Range<usize>) -> impl Iterator<Item = Id<Holder>> {
    range.map(Id::from_index)
}

/// One function's loans and holders, and what each line does to them.
struct Holders<'b> {
    body: &'b mir::Body,
    pointer_bytes: u64,
    borrows: Vec<Id<mir::Instruction>>, // each `Borrow` loan's instruction
    /// Where the holders of each slot, then of each instruction's value,
    /// then of a result kept in memory start; last, how many there are.
    holder_starts: Vec<usize>,
    /// By slot, the loans of places through a pointer in it (`&mut r.*`),
    /// which a write to that pointer ends.
    loans_through: Vec<Vec<Id<Loan>>>,
}

/// The loan check of one function: its holders, and what the walks need
/// found from them first.
struct Loans<'b> {
    holders: Holders<'b>,
    /// By loan, the call a `&mut` loan is reserved until (two-phase).
    reserved: Vec<Option<Reservation>>,
    /// By block, the lines that can give, use or break a loan, in order:
    /// what every walk walks (`lines_that_matter`).
    lines: Vec<Box<[Id<mir::Instruction>]>>,
}

impl<'b> Loans<'b> {
    fn report(&self, file: &Arc<SourceFile>, diagnostics: &mut Vec<Diagnostic>) {
        let body = self.holders.body;
        let ends = self.live_at_ends();
        let starts = self.held_at_starts(&ends);
        // The borrows, then one parameter loan per parameter.
        let mut reported = Bits::new(self.holders.loan_count());
        // The reserved loans by the call they wait for, so each call finds
        // its own without a walk over every borrow.
        let mut reservations: Vec<(Id<mir::Instruction>, Id<Loan>)> = (self.reserved.iter())
            .enumerate()
            .filter_map(|(loan, reservation)| {
                Some((reservation.as_ref()?.call, Id::from_index(loan)))
            })
            .collect();
        reservations.sort_unstable();
        let mut errors = Vec::new();
        for ((block, start), end) in body.blocks.ids().zip(starts).zip(ends) {
            let Some(mut held) = start else {
                continue; // not reached
            };
            let ids = &self.lines[block.index()];
            // First, so a local whose reference is returned is reported as
            // that, not as gone while its reference is in use.
            if let Some((returned, span)) = self.holders.returned_holders(block) {
                let mut at_return = held.clone();
                for &id in ids {
                    self.holders.step_held(&mut at_return, id);
                }
                let loans = self.holders.loans_of(&at_return, returned);
                self.return_errors(span, &loans, &mut reported, file, &mut errors);
            }
            // The holders used later: walked back to the block's start once,
            // keeping only the holders each instruction flips, so the walk
            // forward toggles them back in order. A set kept per instruction
            // would grow with the block's length times its holders.
            let mut live = self.holders.live_set(end.as_deref().unwrap_or_default());
            let mut flips = Vec::new(); // (position, holder), the last first
            let walked = ids.iter().enumerate();
            for (position, &id) in walked.clone().rev() {
                self.holders
                    .step_live(&mut live, id, |holder| flips.push((position, holder)));
            }
            for (position, &id) in walked {
                // `live` is before `id` here, when a call's reserved arguments
                // take hold. No call accesses a place, so an instruction has
                // at most one of the two errors.
                let first = reservations.partition_point(|&(call, _)| call < id);
                let end = reservations.partition_point(|&(call, _)| call <= id);
                let waiting = reservations[first..end].iter().map(|&(_, loan)| loan);
                let error = self.activation_error(waiting, &held, &live, &mut reported, file);
                while let Some(&(at, holder)) = flips.last()
                    && at == position
                {
                    live.toggle(holder);
                    flips.pop();
                }
                let error = error.or_else(|| {
                    let defined = self.holders.defines(id);
                    let skip = |holder: Id<Holder>| defined.contains(&holder.index());
                    let mut accesses = self.accesses(id);
                    if let Some(moved) = self.move_of_read(id, &ids[position + 1..]) {
                        accesses[0] = Some(moved);
                    }
                    (accesses.into_iter().flatten()).find_map(|access| {
                        self.broken_loan(&access, &held, &live, &skip, &mut reported, file)
                    })
                });
                if let Some((holder, error)) = error {
                    let error = self.label_later_use(error, holder, block, position, file);
                    errors.push((body.instructions.span(id).start, error));
                }
                self.holders.step_held(&mut held, id);
                // As at a block's end: a holder not live is never read again,
                // so each check scans only the loans of holders in use.
                held.pairs.retain(|pair| live.has(pair.0));
            }
        }
        errors.sort_by_key(|&(start, _)| start);
        diagnostics.extend(errors.into_iter().map(|(_, error)| error));
    }
}
