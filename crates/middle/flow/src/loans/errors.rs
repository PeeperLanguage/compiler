//! Each access a line makes, and the errors and labels for an access a
//! loan in use forbids.

use std::collections::VecDeque;
use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::mir::AccessKind;
use model::mir::InstructionKind;
use model::mir::LoanWords;
use model::mir::PlaceRef;
use model::mir::REFERENCE_WORDS;
use model::mir::Root;
use model::mir::Slot;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::loans::Holder;
use crate::loans::Holders;
use crate::loans::Loan;
use crate::loans::Loans;
use crate::loans::holders::Held;
use crate::loans::holders::Lent;

/// What an instruction does to a place, as the loan rules see it.
pub(super) struct Access<'p> {
    place: PlaceRef<'p>,
    width: Option<u64>,    // how many bytes from the place; `None`: all there are
    subject: &'static str, // what is done to: "" (the place) or "address of "
    event: &'static str,   // for the message: "changed", "read", ...
    label: &'static str,   // where it is done: "read", "address taken", ...
    reads: bool,           // only reads it, which a shared loan allows
    own_storage: bool,     // only its own storage: replaced or ended
    span: Span,
}

impl<'b> Holders<'b> {
    /// How a message names `slot`: by its name, or, for one made for a value
    /// (no debug type), only as a temporary, "a temporary value": its code
    /// may be long or take several lines, and the labels show it.
    fn slot_name(&self, slot: Id<Slot>, file: &SourceFile) -> String {
        if self.body.slots[slot].debug_type.is_none() {
            return "a temporary value".to_string();
        }
        format!("`{}`", file.slice(self.body.slots.span(slot)))
    }

    /// The words that name `holder`: those of its slot's holder, of the
    /// borrow that gives it, or a reference's.
    fn holder_words(&self, holder: Id<Holder>) -> &'static LoanWords {
        let index = (self.holder_starts).partition_point(|&start| start <= holder.index()) - 1;
        let slots = self.body.slots.len();
        if index < slots {
            let first = self.holder_starts[index];
            return self.body.slots[Id::from_index(index)].holders[holder.index() - first].words;
        }
        let instruction = index - slots;
        if instruction == self.body.instructions.len() {
            return &REFERENCE_WORDS; // a result kept in memory
        }
        match &self.body.instructions[Id::from_index(instruction)].kind {
            InstructionKind::Borrow { words, .. } => words,
            _ => &REFERENCE_WORDS,
        }
    }
}

impl<'b> Loans<'b> {
    /// `error` with a label where `holder`, whose loan it breaks, is next used
    /// after the instruction at `position` of `block`, on the nearest path
    /// that does not set it again: searched only for an error. A reference
    /// used only by the call has no later use to show.
    pub(super) fn label_later_use(
        &self,
        error: Diagnostic,
        holder: Id<Holder>,
        block: Id<mir::Block>,
        position: usize,
        file: &Arc<SourceFile>,
    ) -> Diagnostic {
        let body = self.holders.body;
        let mut seen = vec![false; body.blocks.len()];
        let mut pending = VecDeque::from([(block, position + 1)]);
        let later = 'search: {
            'paths: while let Some((block, from)) = pending.pop_front() {
                for &id in self.lines[block.index()].iter().skip(from) {
                    let mut used = false;
                    self.holders
                        .uses(id, |used_holder| used |= used_holder == holder);
                    if used {
                        break 'search body.instructions.span(id);
                    }
                    if self.holders.defines(id).contains(&holder.index()) {
                        continue 'paths; // set again: this path does not use it
                    }
                }
                if let Some((returned, span)) = self.holders.returned_holders(block)
                    && returned.contains(&holder.index())
                {
                    break 'search span;
                }
                for target in body.blocks[block].terminator.targets() {
                    if !std::mem::replace(&mut seen[target.index()], true) {
                        pending.push_back((target, 0));
                    }
                }
            }
            return error;
        };
        let noun = self.holders.holder_words(holder).noun;
        error.label(file, later, format!("{noun} used later here"))
    }

    /// Adds to `errors` those of the `return` at `span`, whose value holds
    /// `loans`: one for each loan not yet `reported` of the function's
    /// own storage (a borrow not through a pointer), or of a parameter its
    /// origins do not name.
    pub(super) fn return_errors(
        &self,
        span: Span,
        loans: &[Id<Loan>],
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
        errors: &mut Vec<(u32, Diagnostic)>,
    ) {
        let Some(returned) = self.holders.body.returned else {
            unreachable!("only a result that may hold a loan holds one")
        };
        for &loan in loans {
            if reported.has(loan) {
                continue;
            }
            let error = match self.holders.lent(loan) {
                Lent::Borrow(borrow) => {
                    // Through a pointer: what it points into is checked by the
                    // loans it holds.
                    let Root::Slot(slot) = borrow.place.root else {
                        continue;
                    };
                    if borrow.place.is_behind_pointer() {
                        continue;
                    }
                    let name = self.holders.slot_name(slot, file);
                    let message = format!(
                        "returns {returned} {name}, which is gone once the function returns"
                    );
                    let error = Diagnostic::new(diagnostics::LOCAL_ADDRESS_RETURNED, message)
                        .label(file, span, "");
                    // A borrow inside the `return` is already shown.
                    // shortcut: the renderer draws a second label on one line
                    // a line too high; label it too once that is fixed.
                    let made = self.holders.body.instructions.span(borrow.instruction);
                    if made.start < span.start || made.end > span.end {
                        error.label(file, made, format!("{} made here", borrow.words.noun))
                    } else {
                        error
                    }
                }
                Lent::Parameter(position) => {
                    if self
                        .holders
                        .body
                        .signature
                        .origins
                        .contains(&(position as u32))
                    {
                        continue;
                    }
                    let name = file.slice(self.holders.body.slots.span(Id::from_index(position)));
                    let message = format!("returns {returned} `{name}`, which `in` does not name");
                    Diagnostic::new(diagnostics::INVALID_RETURN, message)
                        .label(file, span, "")
                        .help(format!("add `{name}` to `in`"), Vec::new())
                }
            };
            reported.set(loan);
            errors.push((span.start, error));
        }
    }

    /// What the instruction `id` does to places: a copy reads one and
    /// changes another. A reserved `&mut` borrow only reserves its place,
    /// so it is checked as a shared borrow.
    pub(super) fn accesses(&self, id: Id<mir::Instruction>) -> [Option<Access<'b>>; 2] {
        let span = self.holders.body.instructions.span(id);
        let kind = &self.holders.body.instructions[id].kind;
        let mut accesses = [None, None];
        let mut next = accesses.iter_mut();
        kind.accesses(|access| {
            let on = |event, reads, own_storage| Access {
                place: access.place,
                width: access.width(self.holders.pointer_bytes),
                subject: "",
                event,
                label: event,
                reads,
                own_storage,
                span,
            };
            let found = match access.kind {
                AccessKind::Read => on("read", true, false),
                AccessKind::Write => on("changed", false, true),
                AccessKind::Move => on("moved", false, false),
                AccessKind::End => on("gone", false, true),
                AccessKind::Borrow { mutable: false } => on("borrowed", true, false),
                AccessKind::Borrow { mutable: true } => {
                    let loan = self.holders.loan(id);
                    let Lent::Borrow(borrow) = self.holders.lent(loan) else {
                        unreachable!("a `Borrow`'s loan is a borrow")
                    };
                    let reserves = self.reserved[loan.index()].is_some();
                    on(borrow.words.mutably_borrowed, reserves, false)
                }
                // `@x` can write `x`, so no reference to it may be in use; it
                // looks like no borrow, so the message names the address.
                AccessKind::Address => Access {
                    subject: "address of ",
                    event: "being taken",
                    label: "address taken",
                    ..on("", false, false)
                },
            };
            *next
                .next()
                .expect("an instruction touches at most two places") = Some(found);
        });
        accesses
    }

    /// The move of what the instruction `id` reads, if a `Moved` marks it
    /// among the instructions `after` it: lowering marks a move right after
    /// the loads or the copy that read the value. A move forbids more than
    /// a read, so the read is checked as the move.
    pub(super) fn move_of_read(
        &self,
        id: Id<mir::Instruction>,
        after: &[Id<mir::Instruction>],
    ) -> Option<Access<'b>> {
        let instructions = &self.holders.body.instructions;
        let (InstructionKind::Load { place: read, .. } | InstructionKind::Copy { from: read, .. }) =
            &instructions[id].kind
        else {
            return None;
        };
        let reads = |id: &&Id<mir::Instruction>| {
            matches!(
                instructions[**id].kind,
                InstructionKind::Load { .. } | InstructionKind::Copy { .. }
            )
        };
        let &next = after.iter().find(|id| !reads(id))?;
        match &instructions[next].kind {
            InstructionKind::Moved { place, .. } if place.root == read.root => {
                let [moved, None] = self.accesses(next) else {
                    unreachable!("a move accesses one place")
                };
                moved
            }
            _ => None,
        }
    }

    /// The first loan not yet `reported` that `access` breaks, held in
    /// `held` by a holder `live` after it that is not `skip`ped: that
    /// holder, and the error without where the holder is used later.
    pub(super) fn broken_loan(
        &self,
        access: &Access,
        held: &Held,
        live: &Bits<Holder>,
        skip: &dyn Fn(Id<Holder>) -> bool,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let Root::Slot(slot) = access.place.root else {
            return None; // memory no loan names
        };
        let pairs = (held.pairs.iter())
            .filter(|&&(holder, loan)| live.has(holder) && !skip(holder) && !reported.has(loan));
        for &(holder, loan) in pairs {
            let Lent::Borrow(borrow) = self.holders.lent(loan) else {
                continue; // a parameter's: what the caller lent conflicts with nothing here
            };
            if borrow.place.root != access.place.root {
                continue;
            }
            // Level by level while both go on; an access to its own
            // storage does not reach a loan that goes on through it.
            let mut accessed = self.holders.levels(access.place.steps, access.width);
            let mut borrowed = self.holders.levels(borrow.place.steps, borrow.size);
            let breaks = loop {
                match (accessed.next(), borrowed.next()) {
                    (Some(accessed), Some(borrowed)) if !accessed.meets(borrowed) => break false,
                    (Some(_), Some(_)) => {}
                    (None, Some(_)) => break !access.own_storage,
                    (_, None) => break true,
                }
            };
            // A reserved loan acts as a shared one until its call.
            let exclusive = borrow.mutable && self.reserved[loan.index()].is_none();
            if !breaks || (access.reads && !exclusive) {
                continue;
            }
            reported.set(loan);
            let name = self.holders.slot_name(slot, file);
            let (noun, to) = (borrow.words.noun, borrow.words.to);
            let kind = if borrow.mutable {
                format!("a {} {noun}", borrow.words.writable)
            } else {
                format!("a {noun}")
            };
            let (subject, event) = (access.subject, access.event);
            let message =
                format!("{subject}{name} is {event} while {kind} {to} it is still in use");
            let mut error = Diagnostic::new(diagnostics::BORROW_CONFLICT, message)
                .label(file, access.span, format!("{} here", access.label))
                .label(
                    file,
                    self.holders.body.instructions.span(borrow.instruction),
                    format!("{noun} made here"),
                );
            // How long a temporary lasts is not written in the code.
            if self.holders.body.slots[slot].debug_type.is_none() {
                let help = "a temporary lasts until its statement ends, \
                            or as long as the variable a `let` or `=` gives it to";
                error = error.help(help, Vec::new());
            }
            return Some((holder, error));
        }
        None
    }

    /// At a call, the first loan broken when the `waiting` loans, those
    /// reserved until it, take hold: a `&mut` borrow of each one's place,
    /// checked against every holder `live` just before the call (used after
    /// it, or an argument of it) other than those that carry the argument
    /// to it.
    pub(super) fn activation_error(
        &self,
        waiting: impl Iterator<Item = Id<Loan>>,
        held: &Held,
        live: &Bits<Holder>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        for loan in waiting {
            let (Lent::Borrow(borrowed), Some(reservation)) =
                (self.holders.lent(loan), self.reserved[loan.index()])
            else {
                unreachable!("a waiting loan is a reserved borrow's")
            };
            let [Some(mut access), None] = self.accesses(borrowed.instruction) else {
                unreachable!("a borrow accesses its place")
            };
            access.reads = false; // it takes hold now
            let own = self.holders.value_holder(borrowed.instruction);
            let skip = |holder| holder == own || Some(holder) == reservation.carrier;
            if let Some((holder, error)) =
                self.broken_loan(&access, held, live, &skip, reported, file)
            {
                let note = format!(
                    "a {} argument takes hold when the call starts",
                    borrowed.words.writable
                );
                return Some((holder, error.note(note)));
            }
        }
        None
    }
}
