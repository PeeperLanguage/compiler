//! The two dataflow passes, liveness and held loans, over only the lines
//! that can give, use or break a loan.

use std::ops::Range;

use arena::Id;
use model::mir;
use model::mir::AccessKind;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::PlaceRef;
use model::mir::Root;

use crate::Bits;
use crate::dataflow;
use crate::loans::Holder;
use crate::loans::Holders;
use crate::loans::Loan;
use crate::loans::Loans;
use crate::loans::holder_ids;
use crate::loans::holders::Held;
use crate::loans::holders::Lent;
use crate::loans::places::known_until_through;
use crate::loans::places::names_exactly;
use crate::predecessors;

/// What is in `a` or `b`, both in order, in order and once each.
pub(super) fn sorted_union<T: Copy + Ord>(a: &[T], b: &[T]) -> Vec<T> {
    let mut union = a.to_vec();
    union.extend_from_slice(b);
    union.sort_unstable();
    union.dedup();
    union
}

impl<'b> Holders<'b> {
    /// By block, in order, the lines that can give, use or break a loan:
    /// each that defines or uses a holder, writes a place with one, or
    /// accesses a borrowed slot, and each borrow and call. Any other changes nothing
    /// the walks track, and most do not: they read and write slots no
    /// loan reaches, such as a loop's count. A loan is broken only by an
    /// access to its own slot.
    pub(super) fn lines_that_matter(&self) -> Vec<Box<[Id<mir::Instruction>]>> {
        let body = self.body;
        let mut borrowed = Bits::new(body.slots.len());
        for &borrow in &self.borrows {
            if let InstructionKind::Borrow { place, .. } = &body.instructions[borrow].kind
                && let Root::Slot(slot) = place.root
            {
                borrowed.set(slot);
            }
        }
        let matters = |id: Id<mir::Instruction>| {
            let kind = &body.instructions[id].kind;
            let gives_or_breaks = match kind {
                InstructionKind::Borrow { .. } | InstructionKind::Call { .. } => true,
                _ => {
                    let mut found = false;
                    kind.accesses(|access| {
                        let width = access.width(self.pointer_bytes);
                        found |= matches!(access.place.root, Root::Slot(slot) if borrowed.has(slot))
                            || (matches!(access.kind, AccessKind::Write)
                                && !self.place_holders(access.place, width).is_empty());
                    });
                    found
                }
            };
            let mut uses_holder = false;
            if !gives_or_breaks {
                self.uses(id, |_| uses_holder = true);
            }
            gives_or_breaks || uses_holder || !self.defines(id).is_empty()
        };
        (body.blocks.ids())
            .map(|block| {
                (body.lists.items(body.blocks[block].instructions))
                    .filter(|&id| matters(id))
                    .collect()
            })
            .collect()
    }

    /// `held`, the loans of each holder, after the instruction `id`. What
    /// is not listed gives no value that holds a loan.
    pub(super) fn step_held(&self, held: &mut Held, id: Id<mir::Instruction>) {
        let kind = &self.body.instructions[id].kind;
        let target = holder_ids(self.value_holders(id)).next();
        match kind {
            InstructionKind::Store {
                class,
                place,
                value,
            } => {
                let parameter = match *value {
                    Operand::Parameter(index) => Some(self.parameter_loan(index as usize)),
                    _ => None,
                };
                let width = class.bytes(self.pointer_bytes);
                let sources = [self.operand_holders(*value)];
                let place = place.as_place_ref();
                self.step_write(held, place, width, &sources, true, parameter);
            }
            InstructionKind::Copy { to, from, size } => {
                let (to, from) = (to.as_place_ref(), from.as_place_ref());
                let sources = [self.place_holders(from, Some(*size))];
                self.step_write(held, to, *size, &sources, names_exactly(from), None);
            }
            // A value read out of a holder holds its loans; from behind it,
            // or from any of several elements, those of any it may have
            // come from.
            InstructionKind::Load { class, place } if let Some(target) = target => {
                let width = class.bytes(self.pointer_bytes);
                let loans =
                    self.loans_of(held, self.place_holders(place.as_place_ref(), Some(width)));
                held.set(target, &loans);
            }
            // What the place holds, or goes through, stays borrowed while
            // the new reference is in use.
            InstructionKind::Borrow { place, size, .. } => {
                let loans = self.loans_of(held, self.place_holders(place.as_place_ref(), *size));
                let holder = self.value_holder(id);
                held.set(holder, &loans);
                held.add(holder, self.loan(id));
            }
            InstructionKind::Call {
                signature,
                arguments,
                result,
                ..
            } => {
                let sources = self.origin_holders(*signature, arguments);
                match (result, target) {
                    (Some((place, size)), _) => {
                        let place = place.as_place_ref();
                        self.step_write(held, place, *size, &sources, false, None);
                    }
                    (None, Some(target)) => {
                        let loans = self.loans_of(held, sources.into_iter().flatten());
                        held.set(target, &loans);
                    }
                    (None, None) => {}
                }
            }
            _ => {}
        }
    }

    /// `held` after `width` bytes are written to `place` from the holders
    /// `sources`, each holder of the place given those of the source
    /// holder at its position when `paired`, else all their loans; and
    /// the `parameter` loan when the value is a parameter.
    ///
    /// When the place `names_exactly` its holders, as a variable or a field
    /// does, the write replaces what each held, and ends each loan of a
    /// place through them: a store to `r` repoints it, so `r.*` no longer
    /// names what a loan of it borrowed, and what it points into stays
    /// borrowed by the loans its reference took from `r`. An element only
    /// the program picks may be any of them, so each adds what the value
    /// holds, as a result kept in memory does.
    fn step_write(
        &self,
        held: &mut Held,
        place: PlaceRef,
        width: u64,
        sources: &[Range<usize>],
        paired: bool,
        parameter: Option<Id<Loan>>,
    ) {
        let all = || self.loans_of(held, sources.iter().cloned().flatten());
        let targets = match place.root {
            Root::Result => self.result_holders(),
            _ if place.is_behind_pointer() => return, // the checker lets no store here hold a loan
            _ => self.place_holders(place, Some(width)),
        };
        if !names_exactly(place) {
            let loans = all();
            for holder in holder_ids(targets) {
                for &loan in loans.iter().chain(&parameter) {
                    held.add(holder, loan);
                }
            }
            return;
        }
        let shared = (!paired).then(all);
        let mut paired_sources = (sources.iter().cloned().flatten()).map(Id::from_index);
        for holder in holder_ids(targets) {
            let mut loans: Vec<Id<Loan>> = match (&shared, paired_sources.next()) {
                (Some(loans), _) => loans.clone(),
                (None, Some(source)) => held.loans(source).collect(),
                (None, None) => Vec::new(),
            };
            loans.extend(parameter);
            held.set(holder, &loans);
        }
        // Each loan of a place through a pointer in the bytes written,
        // reached by bytes known while compiling.
        let Root::Slot(slot) = place.root else {
            unreachable!("a place named exactly is in a slot")
        };
        let written = self.first_level(place.steps, Some(width));
        let under_place = |&loan: &Id<Loan>| {
            let Lent::Borrow(borrowed) = self.lent(loan) else {
                unreachable!("a loan through a pointer is a borrow's")
            };
            let pointer = self.first_level(borrowed.place.steps, None).span();
            let written = written.span();
            known_until_through(borrowed.place.steps)
                && written.start <= pointer.start
                && pointer.end <= written.end
        };
        let ended: Vec<Id<Loan>> = (self.loans_through[slot.index()].iter())
            .copied()
            .filter(under_place)
            .collect();
        if !ended.is_empty() {
            held.pairs.retain(|pair| !ended.contains(&pair.1));
        }
    }

    /// `live`, the holders used later, before the instruction `id` runs.
    /// `flipped` is given each holder whose bit changes, once per change, so
    /// toggling them all gives back `live` after `id`.
    pub(super) fn step_live(
        &self,
        live: &mut Bits<Holder>,
        id: Id<mir::Instruction>,
        mut flipped: impl FnMut(Id<Holder>),
    ) {
        for holder in holder_ids(self.defines(id)) {
            if live.has(holder) {
                live.clear(holder);
                flipped(holder);
            }
        }
        self.uses(id, |holder| {
            if !live.has(holder) {
                live.set(holder);
                flipped(holder);
            }
        });
    }
}

impl<'b> Loans<'b> {
    /// The live holders where each block ends, in order: those used later on
    /// some path. Found from every block, not only those that leave the
    /// function: a block of an endless `for {}` reaches no exit. A `return`
    /// block, which nothing follows, ends with its returned value in use.
    /// Few holders are live where a block ends, and a set of every holder
    /// kept for every block would grow with the blocks times the holders.
    /// Where many stay live across many blocks a list costs more than a set:
    /// 3,000 references live across 3,000 branches take 154 MB and 1.9 s, with
    /// sets 124 MB and 1.0 s.
    pub(super) fn live_at_ends(&self) -> Vec<Option<Vec<Id<Holder>>>> {
        let body = self.holders.body;
        let predecessors = predecessors(body);
        dataflow(
            body.blocks.len(),
            body.blocks.ids().map(|block| {
                let returned = self
                    .holders
                    .returned_holders(block)
                    .map_or(0..0, |(returned, _)| returned);
                (block, holder_ids(returned).collect())
            }),
            |block| predecessors[block.index()].iter().copied(),
            |block, live: &mut Vec<Id<Holder>>| {
                let mut walked = self.holders.live_set(live);
                for &id in self.lines[block.index()].iter().rev() {
                    self.holders.step_live(&mut walked, id, |_| {});
                }
                *live = walked.ids().collect();
            },
            |a, b| sorted_union(a, b),
        )
    }

    /// The loans each holder may hold where each block starts, `None` for a
    /// block not reached, keeping only holders live at its predecessors'
    /// `ends`: a step reads only holders it uses, and an error only live
    /// ones, so a holder not live there is never read again. A parameter
    /// passed in memory has no store: its holders hold its loan from the
    /// start.
    pub(super) fn held_at_starts(&self, ends: &[Option<Vec<Id<Holder>>>]) -> Vec<Option<Held>> {
        let body = self.holders.body;
        let mut entry = Held::default();
        for position in 0..body.signature.parameters.len() {
            let slot = Id::from_index(position);
            if body.parameter_in_memory(slot).is_some() {
                let all = 0..u64::MAX;
                for holder in holder_ids(self.holders.slot_holders(slot, &all)) {
                    entry.add(holder, self.holders.parameter_loan(position));
                }
            }
        }
        dataflow(
            body.blocks.len(),
            [(Id::from_index(0), entry)],
            |block| body.blocks[block].terminator.targets(),
            |block, held| {
                for &id in &self.lines[block.index()] {
                    self.holders.step_held(held, id);
                }
                let Some(live) = &ends[block.index()] else {
                    unreachable!("each block starts the backward pass")
                };
                held.pairs
                    .retain(|pair| live.binary_search(&pair.0).is_ok());
            },
            |a: &Held, b: &Held| Held {
                pairs: sorted_union(&a.pairs, &b.pairs),
            },
        )
    }
}
