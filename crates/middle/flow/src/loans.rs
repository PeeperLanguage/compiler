//! The loan check (T0037): a place used in a way that a reference to it
//! forbids while that reference is still in use, and a variable whose scope
//! ends while a reference to it is still in use.
//!
//! A loan is one `Borrow`. A holder is a reference or slice in a variable or
//! an instruction value, on its own or in a field, however deep: it holds the
//! loans it may refer through. So each field of a struct keeps its own loans,
//! as if each had a lifetime of its own in Rust, and `p.second` read out of
//! `p` holds only those of `second`. Liveness is by variable or value, as in
//! Rust: using any part of a struct uses all its holders. A loan is in use
//! after a point when some holder that holds it is used again later, so a
//! borrow lasts until the last use of the reference, not to its block's end.
//!
//! Two dataflow passes, then one walk:
//!
//! 1. Backward: the holders used again later (liveness).
//! 2. Forward: the loans each holder may hold. A `Borrow` holds its own
//!    loan and, when it goes through a reference (`&mut r.*`), the loans of
//!    `r`, so what `r` refers to stays borrowed while the new reference is in
//!    use; borrowing a struct holds the loans of the holders in the place
//!    too, so what a reference to it reads out still counts them. Copying a
//!    reference copies its loans; a `store` to a variable replaces them and
//!    ends each loan of a place through it, which now names what the new
//!    reference points to, and one to a field adds to them, as in Rust. No
//!    `store` behind a reference holds a loan: the checker refuses it.
//!    Each block passes on only the loans of holders live at its end: a loan
//!    is read only through a holder that is used, so no error can need a
//!    dead holder's, and what is carried follows the references in use, not
//!    every reference the function ever made.
//! 3. Each access, checked against the loans of the holders in use after it.
//!
//! A shared loan forbids changing, moving, ending and `&mut` borrowing its
//! place; a `&mut` loan forbids every access. Places conflict when one's
//! steps start with the other's (`p` and `p.x`, not `p.x` and `p.y`).
//! Changing a variable or ending its scope only touches its own storage, so
//! a loan reached through the reference stored there is not broken.
//!
//! A `&mut` borrow used only as a call argument is reserved until the call
//! (two-phase): until then it acts as a shared loan, so the other arguments
//! may read the place (`update(&mut x, read(&x))`). When the call starts it
//! takes hold, and a reference to the place still in use then, the call's
//! other arguments included, is an error (`both(&mut x, &x)`).
//!
//! Each holder a call returns holds all the loans of the arguments its
//! function's origins name (`in`), so they stay borrowed while it is in use;
//! a `&mut` argument there is never reserved, as its loan outlives the call.
//! Each holder of a parameter holds a parameter loan, numbered after the
//! borrows, for what the caller lent; it conflicts with nothing here. A
//! returned value may hold only the parameter loans the origins name, and no
//! loan of the function's own storage (T0036).

use std::collections::VecDeque;
use std::ops::Range;
use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

/// A reference or slice in a variable or instruction value, numbered from 0
/// in that order, a struct's in field order. Only named by `Id<Holder>`.
enum Holder {}

/// One `Borrow`, numbered from 0 in instruction order, then one parameter
/// loan for each parameter. Only named by `Id<Loan>`.
enum Loan {}

/// Reports each access that a reference still in use forbids, once per
/// loan, in `body`, whose returned reference may point into the parameters
/// `origins`. A body with no `Borrow` and no reference result has no loan to
/// break and is not walked.
pub fn report_loan_errors(
    body: &mir::Body,
    types: &Types,
    origins: &[u32],
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
    if borrows.is_empty() && types.holds_loans(body.result).is_none() {
        return;
    }
    let variables = body.variables.ids().map(|id| body.variables[id].ty);
    let values = body.instructions.ids().map(|id| body.instructions[id].ty);
    let mut holder_count = 0;
    let mut holder_starts = Vec::with_capacity(variables.len() + values.len() + 1);
    holder_starts.push(holder_count);
    for ty in variables.chain(values) {
        holder_count += types.holders_in(ty);
        holder_starts.push(holder_count);
    }
    let mut loans_through = vec![Vec::new(); body.variables.len()];
    for (index, &borrow) in borrows.iter().enumerate() {
        if let mir::InstructionKind::Borrow { ref place, .. } = body.instructions[borrow].kind
            && place.is_behind_reference()
        {
            loans_through[place.variable.index()].push(Id::from_index(index));
        }
    }
    let mut loans = Loans {
        body,
        types,
        origins,
        reserved: Bits::new(0), // found below: it needs `Loans::uses`
        borrows,
        holder_starts,
        loans_through,
    };
    loans.reserved = loans.reserved_loans();
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

/// One function's loans and holders.
struct Loans<'b> {
    body: &'b mir::Body,
    types: &'b Types,   // to name a reference or a slice in a message
    origins: &'b [u32], // the parameters its returned reference may point into
    borrows: Vec<Id<mir::Instruction>>, // each `Borrow` loan's instruction
    /// Where the holders of each variable, then of each instruction's value,
    /// start; last, how many there are.
    holder_starts: Vec<usize>,
    /// The `&mut` loans reserved until their call (two-phase).
    reserved: Bits<Loan>,
    /// By variable, the loans of places through the reference in it
    /// (`&mut r.*`), which a store to it ends.
    loans_through: Vec<Vec<Id<Loan>>>,
}

/// The words a message names a borrow with, by its type: a reference's or
/// a slice's.
struct BorrowWords {
    noun: &'static str,     // "reference made here"
    writable: &'static str, // "a `&mut` argument"
    to: &'static str,       // "a reference to it"
    into: &'static str,     // "returns a reference into `x`"
    mutably_borrowed: &'static str,
}

const REFERENCE_WORDS: BorrowWords = BorrowWords {
    noun: "reference",
    writable: "`&mut`",
    to: "to",
    into: "into",
    mutably_borrowed: "borrowed as `&mut`",
};

const SLICE_WORDS: BorrowWords = BorrowWords {
    noun: "slice",
    writable: "`&mut []`",
    to: "of",
    into: "of",
    mutably_borrowed: "borrowed as `&mut []`",
};

/// What an instruction does to a place, as the loan rules see it.
struct Access<'p> {
    variable: Id<mir::Variable>,
    projections: &'p [mir::Projection],
    subject: &'static str, // what is done to: "" (the place) or "address of "
    event: &'static str,   // for the message: "changed", "read", ...
    label: &'static str,   // where it is done: "read", "address taken", ...
    reads: bool,           // only reads it, which a shared loan allows
    own_storage: bool,     // only its own storage: replaced, ended, a length read
    span: Span,
}

impl Loans<'_> {
    /// The words for a borrow of the type `ty`, a reference or slice.
    fn words(&self, ty: TypeId) -> &'static BorrowWords {
        match self.types[ty] {
            Type::Slice { .. } => &SLICE_WORDS,
            _ => &REFERENCE_WORDS,
        }
    }

    /// How a message names `variable`: by its name, or, for a hidden one,
    /// which a message names only as a temporary, "a temporary value": its
    /// code may be long or take several lines, and the labels show it.
    fn variable_name(&self, variable: Id<mir::Variable>, file: &SourceFile) -> String {
        if self.body.variables[variable].hidden {
            return "a temporary value".to_string();
        }
        format!("`{}`", file.slice(self.body.variables.span(variable)))
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

    fn variable_holders(&self, variable: Id<mir::Variable>) -> Range<usize> {
        self.holders_at(variable.index())
    }

    fn value_holders(&self, value: Id<mir::Instruction>) -> Range<usize> {
        self.holders_at(self.body.variables.len() + value.index())
    }

    /// The holders a `return` ending `block` hands back, used there, and
    /// the `return`'s span.
    fn returned(&self, block: Id<mir::Block>) -> Option<(Range<usize>, Span)> {
        match self.body.blocks[block].terminator {
            mir::Terminator::Return {
                value: Some(ref value),
                span,
            } => {
                let holders = self.operand_holders(value);
                (!holders.is_empty()).then_some((holders, span))
            }
            _ => None,
        }
    }

    fn operand_holders(&self, operand: &mir::Operand) -> Range<usize> {
        match *operand {
            mir::Operand::Value(value) => self.value_holders(value),
            mir::Operand::Constant(_) | mir::Operand::Argument(_) => 0..0,
        }
    }

    /// The part of `holders`, those of a value of type `ty`, in its field
    /// `field`, and that field's type.
    fn field_holders(
        &self,
        holders: Range<usize>,
        ty: TypeId,
        field: u32,
    ) -> (Range<usize>, TypeId) {
        let Type::Struct { ref fields, .. } = self.types[ty] else {
            unreachable!("only a struct's field holds a reference")
        };
        let (before, rest) = fields.split_at(field as usize);
        let first = holders.start + self.types.holders_in_fields(before);
        (first..first + self.types.holders_in(rest[0]), rest[0])
    }

    /// The holders that `place` is in: those under its fields up to its
    /// first other step.
    fn place_holders(&self, place: &mir::Place) -> Range<usize> {
        let mut holders = self.variable_holders(place.variable);
        let mut ty = self.body.variables[place.variable].ty;
        for step in &place.projections {
            match *step {
                mir::Projection::Field(field) if !holders.is_empty() => {
                    (holders, ty) = self.field_holders(holders, ty, field);
                }
                _ => break,
            }
        }
        holders
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

    /// The holders the instruction `id` uses: all those of the variable of a
    /// place it reads or goes through, and of each operand.
    fn uses(&self, id: Id<mir::Instruction>, mut used: impl FnMut(Id<Holder>)) {
        let kind = &self.body.instructions[id].kind;
        let (place, operands): (Option<&mir::Place>, &[mir::Operand]) = match *kind {
            mir::InstructionKind::Load(ref place)
            | mir::InstructionKind::Length(ref place)
            | mir::InstructionKind::Move(ref place)
            | mir::InstructionKind::Borrow { ref place, .. }
            | mir::InstructionKind::Address(ref place) => (Some(place), &[]),
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
                    holder_ids(self.operand_holders(operand)).for_each(&mut used);
                }
                (None, &[])
            }
            mir::InstructionKind::Unary { ref value, .. }
            | mir::InstructionKind::Convert(ref value)
            | mir::InstructionKind::Field { ref value, .. }
            | mir::InstructionKind::Check {
                failed: ref value, ..
            } => (None, std::slice::from_ref(value)),
            mir::InstructionKind::Call { ref arguments, .. }
            | mir::InstructionKind::Aggregate(ref arguments)
            | mir::InstructionKind::ArrayParts(ref arguments)
            | mir::InstructionKind::Print {
                values: ref arguments,
                ..
            } => (None, arguments),
            mir::InstructionKind::Phi(ref incoming) => {
                for (_, operand) in incoming {
                    holder_ids(self.operand_holders(operand)).for_each(&mut used);
                }
                (None, &[])
            }
            mir::InstructionKind::ScopeEnd(_) => (None, &[]),
        };
        if let Some(place) = place {
            holder_ids(self.variable_holders(place.variable)).for_each(&mut used);
        }
        for operand in operands {
            holder_ids(self.operand_holders(operand)).for_each(&mut used);
        }
    }

    /// The holders the instruction `id` gives a new value: its own value,
    /// or the whole variable it stores to.
    fn defines(&self, id: Id<mir::Instruction>) -> Range<usize> {
        match self.body.instructions[id].kind {
            mir::InstructionKind::Store { ref place, .. } => place
                .whole_variable()
                .map_or(0..0, |variable| self.variable_holders(variable)),
            _ => self.value_holders(id),
        }
    }

    /// The loan the `Borrow` instruction `borrow` makes.
    fn loan(&self, borrow: Id<mir::Instruction>) -> Id<Loan> {
        let loan = self
            .borrows
            .partition_point(|other| other.index() < borrow.index());
        Id::from_index(loan)
    }

    /// The `&mut` loans whose reference is used once, as a call argument.
    fn reserved_loans(&self) -> Bits<Loan> {
        let holder_count = self.holder_count();
        let mut use_counts = vec![0_u32; holder_count];
        let mut call_arguments = Bits::new(holder_count);
        for id in self.body.instructions.ids() {
            let call = match self.body.instructions[id].kind {
                mir::InstructionKind::Call {
                    signature,
                    ref arguments,
                    ..
                } => {
                    // Used again through the returned reference, so its loan
                    // outlives the call.
                    for &position in self.types.origins(signature) {
                        for holder in self.operand_holders(&arguments[position as usize]) {
                            use_counts[holder] += 1;
                        }
                    }
                    true
                }
                _ => false,
            };
            self.uses(id, |holder| {
                use_counts[holder.index()] += 1;
                if call {
                    call_arguments.set(holder);
                }
            });
        }
        let mut reserved = Bits::new(self.borrows.len());
        for &borrow in &self.borrows {
            let mutable = matches!(
                self.body.instructions[borrow].kind,
                mir::InstructionKind::Borrow { mutable: true, .. }
            );
            let holder = self.value_holders(borrow).start;
            if mutable && use_counts[holder] == 1 && call_arguments.has(Id::from_index(holder)) {
                reserved.set(self.loan(borrow));
            }
        }
        reserved
    }

    /// `held`, the loans of each holder, after the instruction `id`. What
    /// is not listed gives no value that holds a loan.
    fn step_held(&self, held: &mut Held, id: Id<mir::Instruction>) {
        let kind = &self.body.instructions[id].kind;
        if let mir::InstructionKind::Store {
            ref place,
            ref value,
        } = *kind
        {
            self.step_store(held, place, value);
            return;
        }
        let target = self.value_holders(id);
        if target.is_empty() {
            return;
        }
        let copy = |held: &mut Held, target: Range<usize>, source: Range<usize>| {
            for (to, from) in holder_ids(target).zip(holder_ids(source)) {
                let loans: Vec<Id<Loan>> = held.loans(from).collect();
                held.set(to, &loans);
            }
        };
        match *kind {
            mir::InstructionKind::Call {
                signature,
                ref arguments,
                ..
            } => {
                let origins = self.types.origins(signature).iter();
                let sources = origins
                    .flat_map(|&position| self.operand_holders(&arguments[position as usize]));
                let loans = self.loans_of(held, sources);
                holder_ids(target).for_each(|holder| held.set(holder, &loans));
            }
            // A value read out of a holder holds its loans; from behind it,
            // those of any it may have come from.
            mir::InstructionKind::Load(ref place) | mir::InstructionKind::Move(ref place) => {
                let source = self.place_holders(place);
                if place.is_behind_reference() {
                    let loans = self.loans_of(held, source);
                    holder_ids(target).for_each(|holder| held.set(holder, &loans));
                } else {
                    copy(held, target, source);
                }
            }
            // What the place holds, or goes through, stays borrowed while
            // the new reference is in use.
            mir::InstructionKind::Borrow { ref place, .. } => {
                let loans = self.loans_of(held, self.place_holders(place));
                let holder = Id::from_index(target.start);
                held.set(holder, &loans);
                held.add(holder, self.loan(id));
            }
            mir::InstructionKind::Aggregate(ref parts) => {
                let mut next = target.start;
                for part in parts {
                    let count = self.types.holders_in(self.body.operand_type(*part));
                    copy(held, next..next + count, self.operand_holders(part));
                    next += count;
                }
            }
            mir::InstructionKind::Field {
                ref value,
                ref fields,
            } => {
                let mut source = self.operand_holders(value);
                let mut ty = self.body.operand_type(*value);
                for &field in fields {
                    (source, ty) = self.field_holders(source, ty, field);
                }
                copy(held, target, source);
            }
            _ => unreachable!("a value holding a loan has its step"),
        }
    }

    /// `held` after `value` is stored to `place`. To a whole variable, it
    /// replaces what the variable held and ends each loan of a place through
    /// it: a store to `r` repoints it, so `r.*` no longer names what a loan
    /// of it borrowed, and what it points into stays borrowed by the loans
    /// its reference took from `r`. To a field, it adds to what the field
    /// held, as Rust's lifetime of a field stays what it was.
    fn step_store(&self, held: &mut Held, place: &mir::Place, value: &mir::Operand) {
        if place.is_behind_reference() {
            return; // the checker lets no store here hold a loan
        }
        let whole = place.whole_variable();
        let mut sources = holder_ids(self.operand_holders(value));
        for holder in holder_ids(self.place_holders(place)) {
            let loans: Vec<Id<Loan>> = match sources.next() {
                Some(source) => held.loans(source).collect(),
                None => Vec::new(),
            };
            if whole.is_some() {
                held.set(holder, &[]);
            }
            loans.iter().for_each(|&loan| held.add(holder, loan));
            // Its parameter loan, after the borrows.
            if let mir::Operand::Argument(position) = *value {
                let loan = Id::from_index(self.borrows.len() + position as usize);
                held.add(holder, loan);
            }
        }
        if let Some(variable) = whole
            && let ended = &self.loans_through[variable.index()]
            && !ended.is_empty()
        {
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

    /// The live holders where each block ends, in order: those used later on
    /// some path. Found from every block, not only those that leave the
    /// function: a block of an endless `for {}` reaches no exit. A `return`
    /// block, which nothing follows, ends with its returned reference in use.
    /// Few holders are live where a block ends, and a set of every holder
    /// kept for every block would grow with the blocks times the holders.
    /// Where many stay live across many blocks a list costs more than a set:
    /// 3,000 references live across 3,000 branches take 154 MB and 1.9 s, with
    /// sets 124 MB and 1.0 s.
    fn live_at_ends(&self) -> Vec<Option<Vec<Id<Holder>>>> {
        let body = self.body;
        let predecessors = predecessors(body);
        dataflow(
            body.blocks.len(),
            body.blocks.ids().map(|block| {
                let returned = self.returned(block).map_or(0..0, |(returned, _)| returned);
                (block, holder_ids(returned).collect())
            }),
            |block| predecessors[block.index()].iter().copied(),
            |block, live: &mut Vec<Id<Holder>>| {
                let mut walked = self.live_set(live);
                let ids: Vec<_> = body.lists.items(body.blocks[block].instructions).collect();
                for &id in ids.iter().rev() {
                    self.step_live(&mut walked, id, |_| {});
                }
                *live = walked.ids().collect();
            },
            |a, b| sorted_union(a, b),
        )
    }

    /// The loans each holder may hold where each block starts, `None` for a
    /// block not reached, keeping only holders live at its predecessors'
    /// `ends`: a step reads only holders it uses, and an error only live
    /// ones, so a holder not live there is never read again.
    fn held_at_starts(&self, ends: &[Option<Vec<Id<Holder>>>]) -> Vec<Option<Held>> {
        let body = self.body;
        dataflow(
            body.blocks.len(),
            [(Id::from_index(0), Held::default())],
            |block| body.blocks[block].terminator.targets(),
            |block, held| {
                for id in body.lists.items(body.blocks[block].instructions) {
                    self.step_held(held, id);
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
        let body = self.body;
        let ends = self.live_at_ends();
        let starts = self.held_at_starts(&ends);
        // The borrows, then one parameter loan per parameter.
        let mut reported = Bits::new(self.borrows.len() + self.body.parameters);
        let mut errors = Vec::new();
        for ((block, start), end) in body.blocks.ids().zip(starts).zip(ends) {
            let Some(mut held) = start else {
                continue; // not reached
            };
            let ids: Vec<_> = body.lists.items(body.blocks[block].instructions).collect();
            // First, so a local whose reference is returned is reported as
            // that, not as gone while its reference is in use.
            if let Some((returned, span)) = self.returned(block) {
                let mut at_return = held.clone();
                for &id in &ids {
                    self.step_held(&mut at_return, id);
                }
                let loans = self.loans_of(&at_return, returned);
                self.return_errors(span, &loans, &mut reported, file, &mut errors);
            }
            // The holders used later: walked back to the block's start once,
            // keeping only the holders each instruction flips, so the walk
            // forward toggles them back in order. A set kept per instruction
            // would grow with the block's length times its holders.
            let mut live = self.live_set(end.as_deref().unwrap_or_default());
            let mut flips = Vec::new(); // (position, holder), the last first
            for (position, &id) in ids.iter().enumerate().rev() {
                self.step_live(&mut live, id, |holder| flips.push((position, holder)));
            }
            for (position, &id) in ids.iter().enumerate() {
                // `live` is before `id` here, when a call's reserved arguments
                // take hold. No call accesses a place, so an instruction has
                // at most one of the two errors.
                let error = self.activation_error(id, &held, &live, &mut reported, file);
                while let Some(&(at, holder)) = flips.last()
                    && at == position
                {
                    live.toggle(holder);
                    flips.pop();
                }
                let error = error.or_else(|| {
                    let access = self.access(id)?;
                    let skip = self.defines(id);
                    self.broken_loan(&access, &held, &live, skip, &mut reported, file)
                });
                if let Some((holder, error)) = error {
                    let error = self.label_later_use(error, holder, block, position, file);
                    errors.push((body.instructions.span(id).start, error));
                }
                self.step_held(&mut held, id);
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
        // The holder's variable or value, found only for an error.
        let index = (self.holder_starts).partition_point(|&start| start <= holder.index()) - 1;
        let ty = match index.checked_sub(self.body.variables.len()) {
            None => self.body.variables[Id::from_index(index)].ty,
            Some(value) => self.body.instructions[Id::from_index(value)].ty,
        };
        let noun = self.words(ty).noun;
        error.label(file, later, format!("{noun} used later here"))
    }

    /// Adds to `errors` those of the `return` at `span`, whose value holds
    /// `loans`: one for each loan not yet `reported` of the function's
    /// own storage (a borrow not through a reference), or of a parameter its
    /// origins do not name.
    fn return_errors(
        &self,
        span: Span,
        loans: &[Id<Loan>],
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
        errors: &mut Vec<(u32, Diagnostic)>,
    ) {
        // Named by what the function returns: a reference into a slice's
        // elements is still a reference.
        let words = self.words(self.body.result);
        let (returned, into) = (words.noun, words.into);
        for &loan in loans {
            if reported.has(loan) {
                continue;
            }
            let error = if let Some(&borrow) = self.borrows.get(loan.index()) {
                let mir::InstructionKind::Borrow { ref place, .. } =
                    self.body.instructions[borrow].kind
                else {
                    unreachable!("each loan is a `Borrow`")
                };
                // Through a reference: what it points into is checked by
                // the loans it holds.
                if place.is_behind_reference() {
                    continue;
                }
                let name = self.variable_name(place.variable, file);
                let message = format!(
                    "returns a {returned} {into} {name}, which is gone once the function returns"
                );
                let error = Diagnostic::new(diagnostics::LOCAL_ADDRESS_RETURNED, message)
                    .label(file, span, "");
                // A borrow inside the `return` is already shown.
                // shortcut: the renderer draws a second label on one line
                // a line too high; label it too once that is fixed.
                let made = self.body.instructions.span(borrow);
                if made.start < span.start || made.end > span.end {
                    let made_noun = self.words(self.body.instructions[borrow].ty).noun;
                    error.label(file, made, format!("{made_noun} made here"))
                } else {
                    error
                }
            } else {
                let position = (loan.index() - self.borrows.len()) as u32;
                if self.origins.contains(&position) {
                    continue;
                }
                let parameter = self.body.variables.span(Id::from_index(position as usize));
                let name = file.slice(parameter);
                let message =
                    format!("returns a {returned} {into} `{name}`, which `in` does not name");
                Diagnostic::new(diagnostics::INVALID_RETURN, message)
                    .label(file, span, "")
                    .help(format!("add `{name}` to `in`"), Vec::new())
            };
            reported.set(loan);
            errors.push((span.start, error));
        }
    }

    /// What the instruction `id` does to a place, if anything. A reserved
    /// `&mut` borrow only reserves its place, so it is checked as a shared
    /// borrow.
    fn access(&self, id: Id<mir::Instruction>) -> Option<Access<'_>> {
        let (place, event, reads, own_storage) = match self.body.instructions[id].kind {
            mir::InstructionKind::Load(ref place) => (place, "read", true, false),
            mir::InstructionKind::Length(ref place) => (place, "read", true, true),
            mir::InstructionKind::Move(ref place) => (place, "moved", false, false),
            mir::InstructionKind::Store { ref place, .. } => (place, "changed", false, true),
            mir::InstructionKind::Borrow {
                ref place,
                mutable: false,
            } => (place, "borrowed", true, false),
            mir::InstructionKind::Borrow {
                ref place,
                mutable: true,
            } => {
                let reserves = self.reserved.has(self.loan(id));
                let event = self.words(self.body.instructions[id].ty).mutably_borrowed;
                (place, event, reserves, false)
            }
            // `@x` can write `x`, so no reference to it may be in use; it
            // looks like no borrow, so the message names the address.
            mir::InstructionKind::Address(ref place) => {
                return Some(Access {
                    variable: place.variable,
                    projections: &place.projections,
                    subject: "address of ",
                    event: "being taken",
                    label: "address taken",
                    reads: false,
                    own_storage: false,
                    span: self.body.instructions.span(id),
                });
            }
            mir::InstructionKind::ScopeEnd(variable) => {
                return Some(Access {
                    variable,
                    projections: &[],
                    subject: "",
                    event: "gone",
                    label: "gone",
                    reads: false,
                    own_storage: true,
                    span: self.body.instructions.span(id),
                });
            }
            _ => return None,
        };
        Some(Access {
            variable: place.variable,
            projections: &place.projections,
            subject: "",
            event,
            label: event,
            reads,
            own_storage,
            span: self.body.instructions.span(id),
        })
    }

    /// The first loan not yet `reported` that `access` breaks, held in
    /// `held` by a holder `live` after it other than those in `skip`: that
    /// holder, and the error without where the holder is used later.
    fn broken_loan(
        &self,
        access: &Access,
        held: &Held,
        live: &Bits<Holder>,
        skip: Range<usize>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let pairs = held.pairs.iter().filter(|&&(holder, loan)| {
            live.has(holder)
                && !skip.contains(&holder.index())
                && loan.index() < self.borrows.len()
                && !reported.has(loan)
        });
        for &(holder, loan) in pairs {
            let borrow = self.borrows[loan.index()];
            let mir::InstructionKind::Borrow {
                place: ref loaned,
                mutable,
            } = self.body.instructions[borrow].kind
            else {
                unreachable!("each loan is a `Borrow`")
            };
            let mut steps = access.projections.iter().zip(&*loaned.projections);
            let overlaps = access.variable == loaned.variable
                && steps.all(|(&left, &right)| left.may_overlap(right));
            let common = access.projections.len().min(loaned.projections.len());
            let past_storage = access.own_storage
                && (loaned.projections[common..].iter()).any(|step| step.leaves_storage());
            // A reserved loan acts as a shared one until its call.
            let exclusive = mutable && !self.reserved.has(loan);
            if !overlaps || past_storage || (access.reads && !exclusive) {
                continue;
            }
            reported.set(loan);
            let name = self.variable_name(access.variable, file);
            let words = self.words(self.body.instructions[borrow].ty);
            let (noun, to) = (words.noun, words.to);
            let kind = if mutable {
                format!("a {} {noun}", words.writable)
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
                    self.body.instructions.span(borrow),
                    format!("{noun} made here"),
                );
            // A temporary ends with its statement; a variable, its block.
            if self.body.variables[access.variable].hidden {
                let help =
                    "keep the value in a variable with `let`, so it lasts as long as the reference";
                error = error.help(help, Vec::new());
            }
            return Some((holder, error));
        }
        None
    }

    /// At a call, the first loan broken when a reserved `&mut` argument takes
    /// hold: a `&mut` borrow of its place, checked against every holder
    /// `live` just before the call (used after it, or an argument of it)
    /// other than the argument itself.
    fn activation_error(
        &self,
        id: Id<mir::Instruction>,
        held: &Held,
        live: &Bits<Holder>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let mir::InstructionKind::Call { ref arguments, .. } = self.body.instructions[id].kind
        else {
            return None;
        };
        for &argument in arguments {
            let mir::Operand::Value(borrow) = argument else {
                continue;
            };
            if !matches!(
                self.body.instructions[borrow].kind,
                mir::InstructionKind::Borrow { .. }
            ) || !self.reserved.has(self.loan(borrow))
            {
                continue;
            }
            let Some(mut access) = self.access(borrow) else {
                unreachable!("a borrow accesses its place")
            };
            access.reads = false; // it takes hold now
            let skip = self.value_holders(borrow);
            if let Some((holder, error)) =
                self.broken_loan(&access, held, live, skip, reported, file)
            {
                let writable = self.words(self.body.instructions[borrow].ty).writable;
                let note = format!("a {writable} argument takes hold when the call starts");
                return Some((holder, error.note(note)));
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
                if self.defines(id).contains(&holder.index()) {
                    continue 'paths; // set again: this path does not use it
                }
            }
            if let Some((returned, span)) = self.returned(block)
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
