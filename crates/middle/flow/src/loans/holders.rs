//! How holders and loans are numbered, what each line uses and defines,
//! and the loans each holder holds (`Held`).

use std::ops::Range;

use arena::Id;
use model::mir;
use model::mir::AccessKind;
use model::mir::InstructionKind;
use model::mir::LoanWords;
use model::mir::Operand;
use model::mir::PlaceRef;
use model::mir::Root;
use model::mir::Slot;
use model::mir::Terminator;
use source::Span;

use crate::Bits;
use crate::loans::Holder;
use crate::loans::Holders;
use crate::loans::Loan;
use crate::loans::holder_ids;
use crate::loans::places::names_exactly;

/// The loans each holder may hold at one point of a function, as pairs in
/// order, none for a holder without one. A table of every holder and every
/// loan, kept for every block, grows with the cube of a function's size.
#[derive(Clone, Default, PartialEq)]
pub(super) struct Held {
    pub(super) pairs: Vec<(Id<Holder>, Id<Loan>)>,
}

impl Held {
    /// Where the pairs of `holder` are.
    fn pair_range(&self, holder: Id<Holder>) -> Range<usize> {
        let start = self.pairs.partition_point(|pair| pair.0 < holder);
        let end = self.pairs.partition_point(|pair| pair.0 <= holder);
        start..end
    }

    /// The loans of `holder`, in order.
    pub(super) fn loans(&self, holder: Id<Holder>) -> impl Iterator<Item = Id<Loan>> {
        self.pairs[self.pair_range(holder)]
            .iter()
            .map(|pair| pair.1)
    }

    /// Makes `loans`, in order, all that `holder` holds.
    pub(super) fn set(&mut self, holder: Id<Holder>, loans: &[Id<Loan>]) {
        let row = self.pair_range(holder);
        drop(
            self.pairs
                .splice(row, loans.iter().map(|&loan| (holder, loan))),
        );
    }

    /// Adds `loan` to what `holder` holds.
    pub(super) fn add(&mut self, holder: Id<Holder>, loan: Id<Loan>) {
        if let Err(position) = self.pairs.binary_search(&(holder, loan)) {
            self.pairs.insert(position, (holder, loan));
        }
    }
}

/// What a loan lent (`Holders::lent`).
pub(super) enum Lent<'b> {
    Borrow(Borrowed<'b>),
    /// What the caller lent the written parameter at this position.
    Parameter(usize),
}

/// A `Borrow`, the instruction a loan is.
#[derive(Clone, Copy)]
pub(super) struct Borrowed<'b> {
    pub(super) instruction: Id<mir::Instruction>,
    pub(super) place: PlaceRef<'b>,
    pub(super) size: Option<u64>,
    pub(super) mutable: bool,
    pub(super) words: &'static LoanWords,
}

impl<'b> Holders<'b> {
    /// How many holders there are.
    pub(super) fn holder_count(&self) -> usize {
        self.holder_starts[self.holder_starts.len() - 1]
    }

    /// The set of `holders`, to walk a block with.
    pub(super) fn live_set(&self, holders: &[Id<Holder>]) -> Bits<Holder> {
        let mut live = Bits::new(self.holder_count());
        holders.iter().for_each(|&holder| live.set(holder));
        live
    }

    /// The holders of the `index`th of `holder_starts`.
    fn holders_at(&self, index: usize) -> Range<usize> {
        self.holder_starts[index]..self.holder_starts[index + 1]
    }

    pub(super) fn value_holders(&self, value: Id<mir::Instruction>) -> Range<usize> {
        self.holders_at(self.body.slots.len() + value.index())
    }

    /// The holder of `value`, which gives a pointer that may hold a loan.
    pub(super) fn value_holder(&self, value: Id<mir::Instruction>) -> Id<Holder> {
        let holders = self.value_holders(value);
        assert_eq!(
            holders.len(),
            1,
            "the value gives a pointer that may hold a loan"
        );
        Id::from_index(holders.start)
    }

    /// The holder of a result kept in memory, if it may hold a loan.
    pub(super) fn result_holders(&self) -> Range<usize> {
        self.holders_at(self.body.slots.len() + self.body.instructions.len())
    }

    /// The holders of `slot` whose offset is in `bytes`.
    pub(super) fn slot_holders(&self, slot: Id<Slot>, bytes: &Range<u64>) -> Range<usize> {
        let holders = &self.body.slots[slot].holders;
        let first = holders.partition_point(|holder| holder.offset < bytes.start);
        let end = holders.partition_point(|holder| holder.offset < bytes.end);
        let start = self.holder_starts[slot.index()];
        start + first..start + end
    }

    /// The holders a `return` ending `block` hands back, used there, and
    /// the `return`'s span; none when the result holds no loan, as a raw
    /// pointer does not.
    pub(super) fn returned_holders(&self, block: Id<mir::Block>) -> Option<(Range<usize>, Span)> {
        let Terminator::Return { value, span } = &self.body.blocks[block].terminator else {
            return None;
        };
        self.body.returned?;
        let holders = match value {
            Some(value) => self.operand_holders(*value),
            None => self.result_holders(),
        };
        (!holders.is_empty()).then_some((holders, *span))
    }

    pub(super) fn operand_holders(&self, operand: Operand) -> Range<usize> {
        match operand {
            Operand::Value(value) => self.value_holders(value),
            Operand::Constant { .. } | Operand::Parameter(_) | Operand::Global(_) => 0..0,
        }
    }

    /// The holders in `place`, `width` bytes or all there are when `None`:
    /// those in its slot's bytes; for a place through a pointer, that
    /// pointer's.
    pub(super) fn place_holders(&self, place: PlaceRef, width: Option<u64>) -> Range<usize> {
        match place.root {
            // Most slots hold no pointer: none of their bytes need finding.
            Root::Slot(slot) if self.body.slots[slot].holders.is_empty() => {
                let start = self.holder_starts[slot.index()];
                start..start
            }
            Root::Slot(slot) => {
                self.slot_holders(slot, &self.first_level(place.steps, width).span())
            }
            Root::At(pointer) => self.operand_holders(pointer),
            Root::Result => 0..0, // the caller's memory, reached by no reference here
        }
    }

    /// The holders a call's argument passes: a register's, or those of the
    /// memory a copy passed by its address is in.
    fn argument_holders(&self, argument: Operand) -> Range<usize> {
        match argument {
            Operand::Value(id) => match &self.body.instructions[id].kind {
                InstructionKind::Address { place, size } => {
                    self.place_holders(place.as_place_ref(), Some(*size))
                }
                _ => self.value_holders(id),
            },
            Operand::Constant { .. } | Operand::Parameter(_) | Operand::Global(_) => 0..0,
        }
    }

    /// The holders of the `arguments` a call of the signature `signature`
    /// passes that its origins name (`in`): what its returned pointer may
    /// point into.
    pub(super) fn origin_holders(
        &self,
        signature: u32,
        arguments: &[Operand],
    ) -> Vec<Range<usize>> {
        (self.body.signatures[signature as usize].origins.iter())
            .map(|&position| self.argument_holders(arguments[position as usize]))
            .collect()
    }

    /// All the loans of `holders`, in order.
    pub(super) fn loans_of(
        &self,
        held: &Held,
        holders: impl Iterator<Item = usize>,
    ) -> Vec<Id<Loan>> {
        let mut loans: Vec<Id<Loan>> = holders
            .flat_map(|holder| held.loans(Id::from_index(holder)))
            .collect();
        loans.sort_unstable();
        loans.dedup();
        loans
    }

    /// The holders the instruction `id` uses: those under a place it reads,
    /// the pointer a place it writes or takes the address of goes through,
    /// and those of each operand. Reading `p.second` uses only the holders
    /// of `p.second`, so what `p.first` holds may be changed meanwhile. A
    /// `Moved` follows the read it marks, so it uses nothing more.
    pub(super) fn uses(&self, id: Id<mir::Instruction>, mut used: impl FnMut(Id<Holder>)) {
        let mut use_range = |range: Range<usize>| holder_ids(range).for_each(&mut used);
        let through = |place: PlaceRef| {
            if place.is_behind_pointer() {
                self.place_holders(place, None)
            } else {
                0..0
            }
        };
        let kind = &self.body.instructions[id].kind;
        kind.accesses(|access| match access.kind {
            AccessKind::Read | AccessKind::Borrow { .. } => {
                let width = access.width(self.pointer_bytes);
                use_range(self.place_holders(access.place, width));
            }
            AccessKind::Write | AccessKind::Address => use_range(through(access.place)),
            AccessKind::Move | AccessKind::End => {}
        });
        match kind {
            InstructionKind::Call {
                callee, arguments, ..
            } => {
                if let mir::Callee::Pointer(pointer) = callee {
                    use_range(self.operand_holders(*pointer));
                }
                for &argument in arguments {
                    use_range(self.argument_holders(argument));
                }
            }
            _ => kind.operands(|operand| use_range(self.operand_holders(operand))),
        }
    }

    /// The holders the instruction `id` gives a new value: its own value,
    /// or those of the place it writes when it replaces them.
    pub(super) fn defines(&self, id: Id<mir::Instruction>) -> Range<usize> {
        let mut written = None;
        self.body.instructions[id].kind.accesses(|access| {
            if let AccessKind::Write = access.kind {
                written = Some((access.place, access.width(self.pointer_bytes)));
            }
        });
        match written {
            Some((place, width)) if names_exactly(place) => self.place_holders(place, width),
            Some(_) => 0..0,
            None => self.value_holders(id),
        }
    }

    /// The loan the `Borrow` instruction `borrow` makes.
    pub(super) fn loan(&self, borrow: Id<mir::Instruction>) -> Id<Loan> {
        let loan = self
            .borrows
            .partition_point(|other| other.index() < borrow.index());
        Id::from_index(loan)
    }

    /// The parameter loan of the written parameter at `position`.
    pub(super) fn parameter_loan(&self, position: usize) -> Id<Loan> {
        Id::from_index(self.borrows.len() + position)
    }

    /// How many loans there are: the borrows, then the parameter loans.
    pub(super) fn loan_count(&self) -> usize {
        self.borrows.len() + self.body.signature.parameters.len()
    }

    pub(super) fn lent(&self, loan: Id<Loan>) -> Lent<'b> {
        let Some(&instruction) = self.borrows.get(loan.index()) else {
            return Lent::Parameter(loan.index() - self.borrows.len());
        };
        let InstructionKind::Borrow {
            place,
            size,
            mutable,
            words,
        } = &self.body.instructions[instruction].kind
        else {
            unreachable!("each of `borrows` is a `Borrow`")
        };
        Lent::Borrow(Borrowed {
            instruction,
            place: place.as_place_ref(),
            size: *size,
            mutable: *mutable,
            words,
        })
    }
}
