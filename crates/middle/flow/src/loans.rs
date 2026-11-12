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

use std::collections::VecDeque;
use std::ops::Range;
use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::mir::AccessKind;
use model::mir::Class;
use model::mir::InstructionKind;
use model::mir::LoanWords;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::PlaceRef;
use model::mir::REFERENCE_WORDS;
use model::mir::Root;
use model::mir::Slot;
use model::mir::Step;
use model::mir::Terminator;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

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

/// What is in `a` or `b`, both in order, in order and once each.
fn sorted_union<T: Copy + Ord>(a: &[T], b: &[T]) -> Vec<T> {
    let mut union = a.to_vec();
    union.extend_from_slice(b);
    union.sort_unstable();
    union.dedup();
    union
}

/// Whether the steps name their bytes exactly up to the first `Through`:
/// each index is known while compiling.
fn known_until_through(steps: &[Step]) -> bool {
    (steps.iter().take_while(|&&step| step != Step::Through)).all(|step| match step {
        Step::Index { index, .. } => matches!(index, Operand::Constant { .. }),
        Step::Offset(_) | Step::Through => true,
    })
}

/// Whether `place` names its holders one by one, so a write gives each a
/// new value and a read copies each: it is in a slot's own storage, and
/// each index is known while compiling. An index known only when the
/// program runs may be any element.
fn names_exactly(place: PlaceRef) -> bool {
    matches!(place.root, Root::Slot(_))
        && !place.steps.contains(&Step::Through)
        && known_until_through(place.steps)
}

/// The bytes a place covers at one level: `count` runs of `width` bytes,
/// `stride` apart from `start`. One run, unless an index only the program
/// knows makes it a part of each element the index may pick.
#[derive(Clone, Copy)]
struct Bytes {
    start: u64,
    width: u64,
    stride: u64,
    count: u64,
}

impl Bytes {
    /// From the first run's start to the last run's end.
    fn span(self) -> Range<u64> {
        let last = self.count.saturating_sub(1).saturating_mul(self.stride);
        self.start..self.start.saturating_add(last).saturating_add(self.width)
    }

    /// Whether the runs cover all of `span`: one run, or runs as wide as
    /// their stride.
    fn is_solid(self) -> bool {
        self.count == 1 || self.width >= self.stride
    }

    /// Whether some run meets `range`.
    fn run_meets(self, range: &Range<u64>) -> bool {
        // The first run that ends after `range` starts.
        let first = match range.start.checked_sub(self.start + self.width) {
            None => 0,
            Some(past) => past / self.stride + 1,
        };
        first < self.count && self.start + first.saturating_mul(self.stride) < range.end
    }

    /// Whether `self` and `other` may share a byte. Runs of one stride
    /// meet where their parts of an element meet; runs of two strides are
    /// taken to meet.
    fn meets(self, other: Bytes) -> bool {
        let (a, b) = (self.span(), other.span());
        if a.start >= b.end || b.start >= a.end {
            return false;
        }
        match (self.is_solid(), other.is_solid()) {
            (true, true) => true,
            (false, true) => self.run_meets(&b),
            (true, false) => other.run_meets(&a),
            (false, false) if self.stride == other.stride => {
                let stride = self.stride;
                // Where `other`'s part starts in `self`'s element.
                let shift = (other.start % stride + stride - self.start % stride) % stride;
                shift < self.width || shift + other.width > stride
            }
            (false, false) => true,
        }
    }
}

/// The loans each holder may hold at one point of a function, as pairs in
/// order, none for a holder without one. A table of every holder and every
/// loan, kept for every block, grows with the cube of a function's size.
#[derive(Clone, Default, PartialEq)]
struct Held {
    pairs: Vec<(Id<Holder>, Id<Loan>)>,
}

impl Held {
    /// Where the pairs of `holder` are.
    fn pair_range(&self, holder: Id<Holder>) -> Range<usize> {
        let start = self.pairs.partition_point(|pair| pair.0 < holder);
        let end = self.pairs.partition_point(|pair| pair.0 <= holder);
        start..end
    }

    /// The loans of `holder`, in order.
    fn loans(&self, holder: Id<Holder>) -> impl Iterator<Item = Id<Loan>> {
        self.pairs[self.pair_range(holder)]
            .iter()
            .map(|pair| pair.1)
    }

    /// Makes `loans`, in order, all that `holder` holds.
    fn set(&mut self, holder: Id<Holder>, loans: &[Id<Loan>]) {
        let row = self.pair_range(holder);
        drop(
            self.pairs
                .splice(row, loans.iter().map(|&loan| (holder, loan))),
        );
    }

    /// Adds `loan` to what `holder` holds.
    fn add(&mut self, holder: Id<Holder>, loan: Id<Loan>) {
        if let Err(position) = self.pairs.binary_search(&(holder, loan)) {
            self.pairs.insert(position, (holder, loan));
        }
    }
}

/// What a loan lent (`Holders::lent`).
enum Lent<'b> {
    Borrow(Borrowed<'b>),
    /// What the caller lent the written parameter at this position.
    Parameter(usize),
}

/// A `Borrow`, the instruction a loan is.
#[derive(Clone, Copy)]
struct Borrowed<'b> {
    instruction: Id<mir::Instruction>,
    place: PlaceRef<'b>,
    size: Option<u64>,
    mutable: bool,
    words: &'static LoanWords,
}

/// A `&mut` loan reserved until a call (two-phase).
#[derive(Clone, Copy)]
struct Reservation {
    call: Id<mir::Instruction>,
    /// The holder of the call's memory argument the reference was stored
    /// in, if it was passed in memory.
    carrier: Option<Id<Holder>>,
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

/// What an instruction does to a place, as the loan rules see it.
struct Access<'p> {
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
    /// The bytes of a place of `steps`, one per level: in its slot, then in
    /// what each pointer it goes through points to. A level before a
    /// `Through` is the pointer gone through; the last covers `width` bytes,
    /// or all there are when `None`. An index only the program knows makes
    /// a level a part of each element it may pick; a second one there, all
    /// the bytes from its first element on.
    fn levels<'s>(
        &self,
        steps: &'s [Step],
        width: Option<u64>,
    ) -> impl Iterator<Item = Bytes> + 's {
        let pointer_bytes = self.pointer_bytes;
        let mut steps = steps.iter();
        let mut done = false;
        std::iter::from_fn(move || {
            if done {
                return None;
            }
            let mut bytes = Bytes {
                start: 0,
                width: 0,
                stride: 0,
                count: 1,
            };
            let mut to_the_end = false; // a second index the program picks
            for &step in steps.by_ref() {
                match step {
                    Step::Through => {
                        if !to_the_end {
                            bytes.width = pointer_bytes;
                        }
                        return Some(bytes);
                    }
                    _ if to_the_end => {}
                    Step::Offset(offset) => bytes.start += offset,
                    Step::Index {
                        index: Operand::Constant { bits, .. },
                        size,
                        ..
                    } => bytes.start += bits[0] * size,
                    Step::Index { size, count, .. } if bytes.count == 1 => {
                        bytes.stride = size;
                        bytes.count = count.unwrap_or(u64::MAX);
                    }
                    Step::Index { .. } => {
                        (bytes.width, bytes.count, to_the_end) = (u64::MAX, 1, true);
                    }
                }
            }
            done = true;
            if !to_the_end {
                bytes.width = width.unwrap_or(u64::MAX);
            }
            Some(bytes)
        })
    }

    /// The bytes of a place of `steps` in its slot (`levels`).
    fn first_level(&self, steps: &[Step], width: Option<u64>) -> Bytes {
        (self.levels(steps, width).next()).expect("a place has a level")
    }

    /// How a message names `slot`: by its name, or, for a hidden one, which
    /// a message names only as a temporary, "a temporary value": its code
    /// may be long or take several lines, and the labels show it.
    fn slot_name(&self, slot: Id<Slot>, file: &SourceFile) -> String {
        if self.body.slots[slot].hidden {
            return "a temporary value".to_string();
        }
        format!("`{}`", file.slice(self.body.slots.span(slot)))
    }

    /// How many holders there are.
    fn holder_count(&self) -> usize {
        self.holder_starts[self.holder_starts.len() - 1]
    }

    /// The set of `holders`, to walk a block with.
    fn live_set(&self, holders: &[Id<Holder>]) -> Bits<Holder> {
        let mut live = Bits::new(self.holder_count());
        holders.iter().for_each(|&holder| live.set(holder));
        live
    }

    /// The holders of the `index`th of `holder_starts`.
    fn holders_at(&self, index: usize) -> Range<usize> {
        self.holder_starts[index]..self.holder_starts[index + 1]
    }

    fn value_holders(&self, value: Id<mir::Instruction>) -> Range<usize> {
        self.holders_at(self.body.slots.len() + value.index())
    }

    /// The holder of `value`, which gives a pointer that may hold a loan.
    fn value_holder(&self, value: Id<mir::Instruction>) -> Id<Holder> {
        let holders = self.value_holders(value);
        assert_eq!(
            holders.len(),
            1,
            "the value gives a pointer that may hold a loan"
        );
        Id::from_index(holders.start)
    }

    /// The holder of a result kept in memory, if it may hold a loan.
    fn result_holders(&self) -> Range<usize> {
        self.holders_at(self.body.slots.len() + self.body.instructions.len())
    }

    /// The holders of `slot` whose offset is in `bytes`.
    fn slot_holders(&self, slot: Id<Slot>, bytes: &Range<u64>) -> Range<usize> {
        let holders = &self.body.slots[slot].holders;
        let first = holders.partition_point(|holder| holder.offset < bytes.start);
        let end = holders.partition_point(|holder| holder.offset < bytes.end);
        let start = self.holder_starts[slot.index()];
        start + first..start + end
    }

    /// The holders a `return` ending `block` hands back, used there, and
    /// the `return`'s span; none when the result holds no loan, as a raw
    /// pointer does not.
    fn returned_holders(&self, block: Id<mir::Block>) -> Option<(Range<usize>, Span)> {
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

    fn operand_holders(&self, operand: Operand) -> Range<usize> {
        match operand {
            Operand::Value(value) => self.value_holders(value),
            Operand::Constant { .. } | Operand::Parameter(_) | Operand::Global(_) => 0..0,
        }
    }

    /// The holders in `place`, `width` bytes or all there are when `None`:
    /// those in its slot's bytes; for a place through a pointer, that
    /// pointer's.
    fn place_holders(&self, place: PlaceRef, width: Option<u64>) -> Range<usize> {
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

    /// What a call of the signature `signature` with `arguments` passes:
    /// through its hidden first argument, the place it writes a result kept
    /// in memory and its size; then the arguments its code wrote.
    fn call_arguments(
        &self,
        signature: u32,
        arguments: &'b [Operand],
    ) -> (Option<(PlaceRef<'b>, u64)>, &'b [Operand]) {
        let hidden = self.body.signatures[signature as usize].hidden_parameters();
        let (hidden, written) = arguments.split_at(hidden as usize);
        let destination = hidden.first().map(|&address| {
            let Operand::Value(address) = address else {
                unreachable!("a result kept in memory is written to an address")
            };
            match &self.body.instructions[address].kind {
                InstructionKind::Address { place, size } => (place.as_place_ref(), *size),
                _ => unreachable!("a result kept in memory is written to a place"),
            }
        });
        (destination, written)
    }

    /// The holders of the `written` arguments a call of the signature
    /// `signature` passes that its origins name (`in`): what its returned
    /// pointer may point into.
    fn origin_holders(&self, signature: u32, written: &[Operand]) -> Vec<Range<usize>> {
        (self.body.signatures[signature as usize].origins.iter())
            .map(|&position| self.argument_holders(written[position as usize]))
            .collect()
    }

    /// All the loans of `holders`, in order.
    fn loans_of(&self, held: &Held, holders: impl Iterator<Item = usize>) -> Vec<Id<Loan>> {
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
    fn uses(&self, id: Id<mir::Instruction>, mut used: impl FnMut(Id<Holder>)) {
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
                callee,
                signature,
                arguments,
            } => {
                if let mir::Callee::Pointer(pointer) = callee {
                    use_range(self.operand_holders(*pointer));
                }
                // Not the destination: the call writes it.
                let (_, written) = self.call_arguments(*signature, arguments);
                for &argument in written {
                    use_range(self.argument_holders(argument));
                }
            }
            _ => kind.operands(|operand| use_range(self.operand_holders(operand))),
        }
    }

    /// The holders the instruction `id` gives a new value: its own value,
    /// or those of the place it writes when it replaces them.
    fn defines(&self, id: Id<mir::Instruction>) -> Range<usize> {
        let written = match &self.body.instructions[id].kind {
            InstructionKind::Call {
                signature,
                arguments,
                ..
            } => {
                let (destination, _) = self.call_arguments(*signature, arguments);
                destination.map(|(place, size)| (place, Some(size)))
            }
            kind => {
                let mut written = None;
                kind.accesses(|access| {
                    if let AccessKind::Write = access.kind {
                        written = Some((access.place, access.width(self.pointer_bytes)));
                    }
                });
                written
            }
        };
        match written {
            Some((place, width)) if names_exactly(place) => self.place_holders(place, width),
            Some(_) => 0..0,
            None => self.value_holders(id),
        }
    }

    /// The loan the `Borrow` instruction `borrow` makes.
    fn loan(&self, borrow: Id<mir::Instruction>) -> Id<Loan> {
        let loan = self
            .borrows
            .partition_point(|other| other.index() < borrow.index());
        Id::from_index(loan)
    }

    /// The parameter loan of the written parameter at `position`.
    fn parameter_loan(&self, position: usize) -> Id<Loan> {
        Id::from_index(self.borrows.len() + position)
    }

    /// How many loans there are: the borrows, then the parameter loans.
    fn loan_count(&self) -> usize {
        self.borrows.len() + self.body.parameters
    }

    fn lent(&self, loan: Id<Loan>) -> Lent<'b> {
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

    /// The `&mut` loans whose reference is used once, by a call: as its
    /// argument, or stored in a memory argument's copy used only by it.
    fn reserved_loans(&self) -> Vec<Option<Reservation>> {
        let holder_count = self.holder_count();
        let mut use_counts = vec![0_u32; holder_count];
        let mut users = vec![None; holder_count]; // the last user of each
        for id in self.body.instructions.ids() {
            if let InstructionKind::Call {
                signature,
                arguments,
                ..
            } = &self.body.instructions[id].kind
            {
                // Used again through the returned reference, so its loan
                // outlives the call.
                let (_, written) = self.call_arguments(*signature, arguments);
                for holders in self.origin_holders(*signature, written) {
                    holders.for_each(|holder| use_counts[holder] += 1);
                }
            }
            self.uses(id, |holder| {
                use_counts[holder.index()] += 1;
                users[holder.index()] = Some(id);
            });
        }
        let used_once_by = |holder: usize| {
            let user = users[holder].filter(|_| use_counts[holder] == 1)?;
            Some((user, &self.body.instructions[user].kind))
        };
        let reservation = |borrow: Id<mir::Instruction>| {
            let holder = self.value_holder(borrow).index();
            match used_once_by(holder)? {
                (call, InstructionKind::Call { .. }) => Some(Reservation {
                    call,
                    carrier: None,
                }),
                (_, InstructionKind::Store { class, place, .. })
                    if names_exactly(place.as_place_ref()) =>
                {
                    let carrier = self
                        .place_holders(place.as_place_ref(), Some(class.bytes(self.pointer_bytes)));
                    if carrier.len() != 1 {
                        return None;
                    }
                    let (call, InstructionKind::Call { .. }) = used_once_by(carrier.start)? else {
                        return None;
                    };
                    Some(Reservation {
                        call,
                        carrier: Some(Id::from_index(carrier.start)),
                    })
                }
                _ => None,
            }
        };
        (self.borrows.iter())
            .map(|&borrow| match self.body.instructions[borrow].kind {
                InstructionKind::Borrow { mutable: true, .. } => reservation(borrow),
                _ => None,
            })
            .collect()
    }

    /// By block, in order, the lines that can give, use or break a loan:
    /// each that defines or uses a holder, writes a place with one, or
    /// accesses a borrowed slot, and each borrow and call. Any other changes nothing
    /// the walks track, and most do not: they read and write slots no
    /// loan reaches, such as a loop's count. A loan is broken only by an
    /// access to its own slot.
    fn lines_that_matter(&self) -> Vec<Box<[Id<mir::Instruction>]>> {
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
    fn step_held(&self, held: &mut Held, id: Id<mir::Instruction>) {
        let kind = &self.body.instructions[id].kind;
        let target = holder_ids(self.value_holders(id)).next();
        match kind {
            InstructionKind::Store {
                class,
                place,
                value,
            } => {
                let hidden = self.body.signature.hidden_parameters();
                let parameter = match *value {
                    Operand::Parameter(index) if index >= hidden => {
                        Some(self.parameter_loan((index - hidden) as usize))
                    }
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
                ..
            } => {
                let (destination, written) = self.call_arguments(*signature, arguments);
                let sources = self.origin_holders(*signature, written);
                match (destination, target) {
                    (Some((place, size)), _) => {
                        self.step_write(held, place, size, &sources, false, None);
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
            // The result kept in memory, written where the hidden first
            // parameter points.
            Root::At(Operand::Parameter(0)) if !self.result_holders().is_empty() => {
                self.result_holders()
            }
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
    fn step_live(
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
    /// The live holders where each block ends, in order: those used later on
    /// some path. Found from every block, not only those that leave the
    /// function: a block of an endless `for {}` reaches no exit. A `return`
    /// block, which nothing follows, ends with its returned value in use.
    /// Few holders are live where a block ends, and a set of every holder
    /// kept for every block would grow with the blocks times the holders.
    /// Where many stay live across many blocks a list costs more than a set:
    /// 3,000 references live across 3,000 branches take 154 MB and 1.9 s, with
    /// sets 124 MB and 1.0 s.
    fn live_at_ends(&self) -> Vec<Option<Vec<Id<Holder>>>> {
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
    fn held_at_starts(&self, ends: &[Option<Vec<Id<Holder>>>]) -> Vec<Option<Held>> {
        let body = self.holders.body;
        let mut entry = Held::default();
        for position in 0..body.parameters {
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

    /// `error` with a label where `holder`, whose loan it breaks, is next used
    /// after the instruction at `position` of `block`. A reference used only
    /// by the call has no later use to show.
    fn label_later_use(
        &self,
        error: Diagnostic,
        holder: Id<Holder>,
        block: Id<mir::Block>,
        position: usize,
        file: &Arc<SourceFile>,
    ) -> Diagnostic {
        let Some(later) = self.next_use(holder, block, position) else {
            return error;
        };
        let noun = self.holders.holder_words(holder).noun;
        error.label(file, later, format!("{noun} used later here"))
    }

    /// Adds to `errors` those of the `return` at `span`, whose value holds
    /// `loans`: one for each loan not yet `reported` of the function's
    /// own storage (a borrow not through a pointer), or of a parameter its
    /// origins do not name.
    fn return_errors(
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
    fn accesses(&self, id: Id<mir::Instruction>) -> [Option<Access<'b>>; 2] {
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
    fn move_of_read(
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
    fn broken_loan(
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
            if self.holders.body.slots[slot].hidden {
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
    fn activation_error(
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

    /// Where `holder` is next used after the instruction at `position` of
    /// `block`, on the nearest path that does not set it again: searched
    /// only for an error.
    fn next_use(&self, holder: Id<Holder>, block: Id<mir::Block>, position: usize) -> Option<Span> {
        let body = self.holders.body;
        let mut seen = vec![false; body.blocks.len()];
        let mut pending = VecDeque::from([(block, position + 1)]);
        'paths: while let Some((block, from)) = pending.pop_front() {
            for &id in self.lines[block.index()].iter().skip(from) {
                let mut used = false;
                self.holders
                    .uses(id, |used_holder| used |= used_holder == holder);
                if used {
                    return Some(body.instructions.span(id));
                }
                if self.holders.defines(id).contains(&holder.index()) {
                    continue 'paths; // set again: this path does not use it
                }
            }
            if let Some((returned, span)) = self.holders.returned_holders(block)
                && returned.contains(&holder.index())
            {
                return Some(span);
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

#[cfg(test)]
mod tests {
    use super::*;

    /// `count` runs of `width` bytes, `stride` apart from `start`.
    fn runs(start: u64, width: u64, stride: u64, count: u64) -> Bytes {
        Bytes {
            start,
            width,
            stride,
            count,
        }
    }

    /// The second word of each 16-byte element, as a field read at an index
    /// only the program knows, meets only the bytes of a second word.
    #[test]
    fn a_part_of_each_element_meets_only_that_part() {
        let second_words = runs(8, 8, 16, 4);
        assert!(!second_words.meets(runs(0, 8, 0, 1)), "the first word");
        assert!(
            second_words.meets(runs(24, 8, 0, 1)),
            "element 1's second word"
        );
        assert!(
            !second_words.meets(runs(64, 8, 0, 1)),
            "past the last element"
        );
        assert!(!second_words.meets(runs(0, 8, 16, 4)), "every first word");
        assert!(
            second_words.meets(runs(12, 4, 16, 4)),
            "a part of each second word"
        );
        assert!(
            second_words.meets(runs(0, 16, 16, 4)),
            "every element whole"
        );
    }
}
