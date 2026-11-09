//! The loan check (T0037, T0036): a place used in a way that a reference to
//! it forbids while that reference is still in use, a variable whose scope
//! ends while a reference to it is still in use, and a returned reference
//! to the function's own storage.
//!
//! A loan is one `Borrow`. A holder is a reference in a variable or an
//! instruction value, on its own or in a field, however deep: it holds the
//! loans it may refer through. Each field keeps its own loans, and `p.second`
//! read out of `p` holds only those of `second`. A loan is in use after a
//! point when some holder that holds it is used again later, so a borrow
//! lasts until the last use of the reference, not to its block's end. The
//! check knows a type only by its facts: how many holders a value of it is
//! (`Facts::holders`), and for a field, which of them (`Projection::Field`).
//!
//! Two dataflow passes, then one walk:
//!
//! 1. Backward: the holders used again later (liveness).
//! 2. Forward: the loans each holder may hold. A `Borrow` holds its own loan
//!    and those of the holders in its place, so what a reference refers
//!    through stays borrowed while the new reference is in use. Copying a
//!    reference copies its loans; a `store` to a variable or a field replaces
//!    them and ends each loan of a place through it, which now names what
//!    the new reference points to. Each block passes on only the loans of
//!    holders live at its end.
//! 3. Each access, checked against the loans of the holders in use after it.
//!
//! A shared loan forbids changing, moving, ending and `&mut` borrowing its
//! place; a `&mut` loan forbids every access. Places conflict when one's
//! steps start with the other's (`p` and `p.x`, not `p.x` and `p.y`).
//! Changing a variable or ending its scope touches only its own storage, so
//! a loan reached through the reference stored there is not broken.
//!
//! A `&mut` borrow used only as a call argument is reserved until the call
//! (two-phase): until then it acts as a shared loan, so the other arguments
//! may read the place. When the call starts it takes hold, and a reference
//! to the place still in use then, the call's other arguments included, is
//! an error.

use std::ops::Range;

use model::mir::BlockId;
use model::mir::Body;
use model::mir::InstructionId;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::mir::Program;
use model::mir::Projection;
use model::mir::Terminator;
use model::mir::VariableId;
use model::span::Span;

use crate::Bits;
use crate::Diagnostic;
use crate::dataflow;
use crate::predecessors;
use crate::slice;

/// Reports each access that a reference still in use forbids, once per
/// loan, and each returned reference to the function's own storage. A body
/// with no `Borrow` has no loan and is not walked.
pub(crate) fn report(body: &Body, program: &Program, source: &str, errors: &mut Vec<Diagnostic>) {
    let borrows: Vec<InstructionId> = (body.instructions.iter().enumerate())
        .filter(|(_, instruction)| matches!(instruction.kind, InstructionKind::Borrow { .. }))
        .map(|(index, _)| InstructionId(index as u32))
        .collect();
    if borrows.is_empty() {
        return;
    }
    let variables = body.variables.iter().map(|variable| variable.ty);
    let values = body.instructions.iter().map(|instruction| instruction.ty);
    let mut holder_count = 0;
    let mut holder_starts = vec![holder_count];
    for ty in variables.chain(values) {
        holder_count += program.facts(ty).holders;
        holder_starts.push(holder_count);
    }
    let mut loans_through = vec![Vec::new(); body.variables.len()];
    for (loan, &borrow) in borrows.iter().enumerate() {
        if let InstructionKind::Borrow { place, .. } = &body.instructions[borrow.0 as usize].kind
            && place.is_behind_reference()
        {
            loans_through[place.variable.0 as usize].push(loan);
        }
    }
    let mut loans = Loans {
        body,
        program,
        borrows,
        holder_starts,
        reserved: Bits::new(0), // found below: it needs `Loans::uses`
        loans_through,
    };
    loans.reserved = loans.reserved_loans();
    loans.report(source, errors);
}

/// What is in `a` or `b`, both in order, in order and once each.
fn sorted_union<T: Copy + Ord>(a: &[T], b: &[T]) -> Vec<T> {
    let mut union = a.to_vec();
    union.extend_from_slice(b);
    union.sort_unstable();
    union.dedup();
    union
}

/// The loans each holder may hold at one point, as (holder, loan) pairs in
/// order, none for a holder without one: a table of every holder and every
/// loan kept for every block would grow with the cube of a function's size.
#[derive(Clone, Default, PartialEq)]
struct Held {
    pairs: Vec<(usize, usize)>,
}

impl Held {
    /// Where the pairs of `holder` are.
    fn pair_range(&self, holder: usize) -> Range<usize> {
        let start = self.pairs.partition_point(|pair| pair.0 < holder);
        let end = self.pairs.partition_point(|pair| pair.0 <= holder);
        start..end
    }

    /// The loans of `holder`, in order.
    fn loans(&self, holder: usize) -> impl Iterator<Item = usize> + '_ {
        self.pairs[self.pair_range(holder)]
            .iter()
            .map(|pair| pair.1)
    }

    /// Makes `loans`, in order, all that `holder` holds.
    fn set(&mut self, holder: usize, loans: &[usize]) {
        let row = self.pair_range(holder);
        drop(
            self.pairs
                .splice(row, loans.iter().map(|&loan| (holder, loan))),
        );
    }

    /// Adds `loan` to what `holder` holds.
    fn add(&mut self, holder: usize, loan: usize) {
        if let Err(position) = self.pairs.binary_search(&(holder, loan)) {
            self.pairs.insert(position, (holder, loan));
        }
    }
}

/// One function's loans and holders.
struct Loans<'b> {
    body: &'b Body,
    program: &'b Program, // the holders of an aggregate's parts, by type
    /// Each loan's `Borrow`, in instruction order.
    borrows: Vec<InstructionId>,
    /// Where the holders of each variable, then of each instruction's value,
    /// start; last, how many there are.
    holder_starts: Vec<usize>,
    /// The `&mut` loans reserved until their call (two-phase).
    reserved: Bits,
    /// By variable, the loans of places through the reference in it
    /// (`&mut r.*`), which a store to it ends.
    loans_through: Vec<Vec<usize>>,
}

/// What an instruction does to a place, as the loan rules see it.
struct Access<'p> {
    variable: VariableId,
    projections: &'p [Projection],
    event: &'static str, // for the message: "changed", "read", ...
    reads: bool,         // only reads it, which a shared loan allows
    own_storage: bool,   // only its own storage: replaced or ended
    span: Span,
}

impl Loans<'_> {
    /// How a message names `variable`: by its name, or, for one lowering
    /// made, "a temporary value", as its span is code, not a name.
    fn variable_name(&self, variable: VariableId, source: &str) -> String {
        let variable = &self.body.variables[variable.0 as usize];
        match variable.hidden {
            true => "a temporary value".to_string(),
            false => format!("`{}`", slice(source, variable.span)),
        }
    }

    fn holder_count(&self) -> usize {
        self.holder_starts[self.holder_starts.len() - 1]
    }

    /// The set of `holders`, to walk a block with.
    fn live_set(&self, holders: &[usize]) -> Bits {
        let mut live = Bits::new(self.holder_count());
        holders.iter().for_each(|&holder| live.set(holder));
        live
    }

    /// The holders of the `index`th of `holder_starts`.
    fn holders_at(&self, index: usize) -> Range<usize> {
        self.holder_starts[index]..self.holder_starts[index + 1]
    }

    fn value_holders(&self, value: InstructionId) -> Range<usize> {
        self.holders_at(self.body.variables.len() + value.0 as usize)
    }

    fn operand_holders(&self, operand: &Operand) -> Range<usize> {
        match *operand {
            Operand::Value(value) => self.value_holders(value),
            Operand::Constant(_) | Operand::Argument(_) | Operand::Function(_) => 0..0,
        }
    }

    /// The holders a `return` ending `block` hands back, used there, and
    /// the `return`'s span.
    fn returned(&self, block: BlockId) -> Option<(Range<usize>, Span)> {
        match &self.body.blocks[block.0 as usize].terminator {
            Terminator::Return {
                value: Some(value),
                span,
            } => {
                let holders = self.operand_holders(value);
                (!holders.is_empty()).then_some((holders, *span))
            }
            _ => None,
        }
    }

    /// The holders that `place` is in: those under its fields, up to its
    /// first dereference.
    fn place_holders(&self, place: &Place) -> Range<usize> {
        let holders = self.holders_at(place.variable.0 as usize);
        let mut start = holders.start;
        let mut end = holders.end;
        for step in &place.projections {
            match *step {
                Projection::Field {
                    first_holder,
                    holders,
                    ..
                } => {
                    start += first_holder as usize;
                    end = start + holders as usize;
                }
                Projection::Dereference => break,
            }
        }
        start..end
    }

    /// All the loans of `holders`, in order.
    fn loans_of(&self, held: &Held, holders: impl Iterator<Item = usize>) -> Vec<usize> {
        let mut loans: Vec<usize> = holders.flat_map(|holder| held.loans(holder)).collect();
        loans.sort_unstable();
        loans.dedup();
        loans
    }

    /// The holders the instruction `id` uses: those under a place it reads
    /// or goes through, and those of each operand.
    fn uses(&self, id: InstructionId, mut used: impl FnMut(usize)) {
        let mut operand = |operand: &Operand| self.operand_holders(operand).for_each(&mut used);
        let place = match &self.body.instructions[id.0 as usize].kind {
            InstructionKind::Load(place)
            | InstructionKind::Move(place)
            | InstructionKind::Borrow { place, .. } => Some(place),
            // A store reads only a reference it writes through.
            InstructionKind::Store { place, value, .. } => {
                operand(value);
                place.is_behind_reference().then_some(place)
            }
            InstructionKind::Binary { left, right, .. } => {
                operand(left);
                operand(right);
                None
            }
            InstructionKind::Call { arguments, .. } => {
                arguments.iter().for_each(operand);
                None
            }
            InstructionKind::Aggregate(parts) => {
                parts.iter().for_each(|part| operand(&part.value));
                None
            }
            InstructionKind::ScopeEnd(_) => None,
        };
        if let Some(place) = place {
            self.place_holders(place).for_each(used);
        }
    }

    /// The holders the instruction `id` gives a new value: its own value,
    /// or those of the place it stores to, unless behind a reference.
    fn defines(&self, id: InstructionId) -> Range<usize> {
        match &self.body.instructions[id.0 as usize].kind {
            InstructionKind::Store { place, .. } if place.is_behind_reference() => 0..0,
            InstructionKind::Store { place, .. } => self.place_holders(place),
            _ => self.value_holders(id),
        }
    }

    /// The loan the `Borrow` instruction `borrow` makes.
    fn loan(&self, borrow: InstructionId) -> usize {
        self.borrows.partition_point(|other| other.0 < borrow.0)
    }

    /// The `&mut` loans whose reference is used once, as a call argument.
    fn reserved_loans(&self) -> Bits {
        let mut use_counts = vec![0_u32; self.holder_count()];
        let mut call_arguments = Bits::new(self.holder_count());
        for index in 0..self.body.instructions.len() {
            let id = InstructionId(index as u32);
            let call = match &self.body.instructions[index].kind {
                InstructionKind::Call { arguments, .. } => {
                    // Used again through the returned reference, so its loan
                    // outlives the call (see `step_held`).
                    if !self.value_holders(id).is_empty() {
                        for argument in arguments {
                            self.operand_holders(argument)
                                .for_each(|holder| use_counts[holder] += 1);
                        }
                    }
                    true
                }
                _ => false,
            };
            self.uses(id, |holder| {
                use_counts[holder] += 1;
                if call {
                    call_arguments.set(holder);
                }
            });
        }
        let mut reserved = Bits::new(self.borrows.len());
        for &borrow in &self.borrows {
            let mutable = matches!(
                self.body.instructions[borrow.0 as usize].kind,
                InstructionKind::Borrow { mutable: true, .. }
            );
            let holder = self.value_holders(borrow).start;
            if mutable && use_counts[holder] == 1 && call_arguments.has(holder) {
                reserved.set(self.loan(borrow));
            }
        }
        reserved
    }

    /// `held`, the loans of each holder, after the instruction `id`.
    fn step_held(&self, held: &mut Held, id: InstructionId) {
        let kind = &self.body.instructions[id.0 as usize].kind;
        if let InstructionKind::Store { place, value, .. } = kind {
            self.step_store(held, place, value);
            return;
        }
        let target = self.value_holders(id);
        if target.is_empty() {
            return;
        }
        let copy = |held: &mut Held, target: Range<usize>, source: Range<usize>| {
            for (to, from) in target.zip(source) {
                let loans: Vec<usize> = held.loans(from).collect();
                held.set(to, &loans);
            }
        };
        match kind {
            // shortcut: what a call returns holds the loans of all its
            // arguments, as functions do not yet say which it may point
            // into; written when a function returning a reference takes two.
            InstructionKind::Call { arguments, .. } => {
                let sources = arguments
                    .iter()
                    .flat_map(|argument| self.operand_holders(argument));
                let loans = self.loans_of(held, sources);
                target.for_each(|holder| held.set(holder, &loans));
            }
            // A value read out of a holder holds its loans; from behind it,
            // those of the reference it came through.
            InstructionKind::Load(place) | InstructionKind::Move(place) => {
                let source = self.place_holders(place);
                if place.is_behind_reference() {
                    let loans = self.loans_of(held, source);
                    target.for_each(|holder| held.set(holder, &loans));
                } else {
                    copy(held, target, source);
                }
            }
            // What the place holds, or goes through, stays borrowed while
            // the new reference is in use.
            InstructionKind::Borrow { place, .. } => {
                let loans = self.loans_of(held, self.place_holders(place));
                held.set(target.start, &loans);
                held.add(target.start, self.loan(id));
            }
            InstructionKind::Aggregate(parts) => {
                let mut next = target.start;
                for part in parts {
                    let count = self.program.facts(part.ty).holders;
                    copy(held, next..next + count, self.operand_holders(&part.value));
                    next += count;
                }
            }
            InstructionKind::Store { .. }
            | InstructionKind::Binary { .. }
            | InstructionKind::ScopeEnd(_) => {
                unreachable!("a value holding a loan has its step")
            }
        }
    }

    /// `held` after `value` is stored to `place`, a variable or its field:
    /// the store replaces what each holder there held, and ends each loan
    /// of a place through them, as a store to `r` repoints it, so `r.*` no
    /// longer names what a loan of it borrowed.
    fn step_store(&self, held: &mut Held, place: &Place, value: &Operand) {
        if place.is_behind_reference() {
            return; // the checker lets no store here hold a loan
        }
        let mut sources = self.operand_holders(value);
        for holder in self.place_holders(place) {
            let loans: Vec<usize> = match sources.next() {
                Some(source) => held.loans(source).collect(),
                None => Vec::new(),
            };
            held.set(holder, &loans);
        }
        // Each loan of a place under `place` that goes through a reference:
        // its steps start with the same fields.
        let under_place =
            |loan: &usize| match &self.body.instructions[self.borrows[*loan].0 as usize].kind {
                InstructionKind::Borrow {
                    place: borrowed, ..
                } => (place.projections.iter())
                    .zip(&borrowed.projections)
                    .all(|(stored, borrowed)| stored == borrowed),
                _ => unreachable!("each loan is a `Borrow`"),
            };
        let ended: Vec<usize> = (self.loans_through[place.variable.0 as usize].iter())
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
    fn step_live(&self, live: &mut Bits, id: InstructionId, mut flipped: impl FnMut(usize)) {
        for holder in self.defines(id) {
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
    /// function, as a block of an endless `for {}` reaches no exit. A
    /// `return` block ends with its returned reference in use.
    fn live_at_ends(&self) -> Vec<Option<Vec<usize>>> {
        let body = self.body;
        let predecessors = predecessors(body);
        dataflow(
            body.blocks.len(),
            (0..body.blocks.len()).map(|index| {
                let block = BlockId(index as u32);
                let returned = self.returned(block).map_or(0..0, |(returned, _)| returned);
                (block, returned.collect())
            }),
            |block| predecessors[block.0 as usize].clone(),
            |block, live: &mut Vec<usize>| {
                let mut walked = self.live_set(live);
                for &id in body.blocks[block.0 as usize].instructions.iter().rev() {
                    self.step_live(&mut walked, id, |_| {});
                }
                *live = walked.ids().collect();
            },
            |a, b| sorted_union(a, b),
        )
    }

    /// The loans each holder may hold where each block starts, `None` for a
    /// block not reached, keeping only holders live at its predecessors'
    /// `ends`: a holder not live there is never read again.
    fn held_at_starts(&self, ends: &[Option<Vec<usize>>]) -> Vec<Option<Held>> {
        let body = self.body;
        dataflow(
            body.blocks.len(),
            [(BlockId(0), Held::default())],
            |block| body.blocks[block.0 as usize].terminator.targets(),
            |block, held| {
                for &id in &body.blocks[block.0 as usize].instructions {
                    self.step_held(held, id);
                }
                let Some(live) = &ends[block.0 as usize] else {
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

    fn report(&self, source: &str, errors: &mut Vec<Diagnostic>) {
        let body = self.body;
        let ends = self.live_at_ends();
        let starts = self.held_at_starts(&ends);
        let mut reported = Bits::new(self.borrows.len());
        let mut found = Vec::new();
        for (index, (start, end)) in starts.into_iter().zip(ends).enumerate() {
            let Some(mut held) = start else {
                continue; // not reached
            };
            let block = BlockId(index as u32);
            let ids = &body.blocks[index].instructions;
            // First, so a local whose reference is returned is reported as
            // that, not as gone while its reference is in use.
            if let Some((returned, span)) = self.returned(block) {
                let mut at_return = held.clone();
                for &id in ids {
                    self.step_held(&mut at_return, id);
                }
                let loans = self.loans_of(&at_return, returned);
                self.return_errors(span, &loans, &mut reported, source, &mut found);
            }
            // The holders used later: walked back to the block's start once,
            // keeping only the holders each instruction flips, so the walk
            // forward toggles them back in order.
            let mut live = self.live_set(end.as_deref().unwrap_or_default());
            let mut flips = Vec::new(); // (position, holder), the last first
            for (position, &id) in ids.iter().enumerate().rev() {
                self.step_live(&mut live, id, |holder| flips.push((position, holder)));
            }
            for (position, &id) in ids.iter().enumerate() {
                // `live` is before `id` here, when a call's reserved arguments
                // take hold. No call accesses a place, so an instruction has
                // at most one of the two errors.
                let error = self.activation_error(id, &held, &live, &mut reported, source);
                while let Some(&(at, holder)) = flips.last()
                    && at == position
                {
                    live.toggle(holder);
                    flips.pop();
                }
                let error = error.or_else(|| {
                    let access = self.access(id)?;
                    let skip = self.defines(id);
                    self.broken_loan(&access, &held, &live, skip, &mut reported, source)
                });
                found.extend(error);
                self.step_held(&mut held, id);
                // As at a block's end: a holder not live is never read again.
                held.pairs.retain(|pair| live.has(pair.0));
            }
        }
        found.sort_by_key(|error| error.span.start);
        errors.extend(found);
    }

    /// Adds to `found` one error for each of `loans`, held by the value the
    /// `return` at `span` gives, that is not yet `reported` and borrows the
    /// function's own storage: a borrow not through a reference.
    fn return_errors(
        &self,
        span: Span,
        loans: &[usize],
        reported: &mut Bits,
        source: &str,
        found: &mut Vec<Diagnostic>,
    ) {
        for &loan in loans {
            if reported.has(loan) {
                continue;
            }
            let InstructionKind::Borrow { place, .. } =
                &self.body.instructions[self.borrows[loan].0 as usize].kind
            else {
                unreachable!("each loan is a `Borrow`")
            };
            // Through a reference: what it points into is checked by the
            // loans it holds.
            if place.is_behind_reference() {
                continue;
            }
            let name = self.variable_name(place.variable, source);
            reported.set(loan);
            found.push(Diagnostic {
                code: "T0036",
                message: format!(
                    "returns a reference into {name}, which is gone once the function returns"
                ),
                span,
            });
        }
    }

    /// What the instruction `id` does to a place, if anything. A reserved
    /// `&mut` borrow only reserves its place, so it is checked as a shared
    /// borrow.
    fn access(&self, id: InstructionId) -> Option<Access<'_>> {
        let instruction = &self.body.instructions[id.0 as usize];
        let (variable, projections, event, reads, own_storage) = match &instruction.kind {
            InstructionKind::Load(place) => {
                (place.variable, &place.projections[..], "read", true, false)
            }
            InstructionKind::Move(place) => (
                place.variable,
                &place.projections[..],
                "moved",
                false,
                false,
            ),
            InstructionKind::Store { place, .. } => (
                place.variable,
                &place.projections[..],
                "changed",
                false,
                true,
            ),
            InstructionKind::Borrow {
                place,
                mutable: false,
            } => (
                place.variable,
                &place.projections[..],
                "borrowed",
                true,
                false,
            ),
            InstructionKind::Borrow {
                place,
                mutable: true,
            } => {
                let reserves = self.reserved.has(self.loan(id));
                (
                    place.variable,
                    &place.projections[..],
                    "borrowed as `&mut`",
                    reserves,
                    false,
                )
            }
            InstructionKind::ScopeEnd(variable) => (*variable, &[][..], "gone", false, true),
            // These use values only, never a place.
            InstructionKind::Binary { .. }
            | InstructionKind::Call { .. }
            | InstructionKind::Aggregate(_) => {
                return None;
            }
        };
        Some(Access {
            variable,
            projections,
            event,
            reads,
            own_storage,
            span: instruction.span,
        })
    }

    /// The error for the first loan not yet `reported` that `access` breaks,
    /// held in `held` by a holder `live` after it other than those in `skip`.
    fn broken_loan(
        &self,
        access: &Access,
        held: &Held,
        live: &Bits,
        skip: Range<usize>,
        reported: &mut Bits,
        source: &str,
    ) -> Option<Diagnostic> {
        let mut pairs = held.pairs.iter().filter(|&&(holder, loan)| {
            live.has(holder) && !skip.contains(&holder) && !reported.has(loan)
        });
        let (_, loan) = *pairs.find(|&&(_, loan)| {
            let InstructionKind::Borrow {
                place: loaned,
                mutable,
            } = &self.body.instructions[self.borrows[loan].0 as usize].kind
            else {
                unreachable!("each loan is a `Borrow`")
            };
            let steps = access.projections.iter().zip(&loaned.projections);
            let overlaps = access.variable == loaned.variable
                && steps.into_iter().all(|steps| match steps {
                    (
                        Projection::Field { index: left, .. },
                        Projection::Field { index: right, .. },
                    ) => left == right,
                    (Projection::Dereference, Projection::Dereference) => true,
                    _ => unreachable!("one storage has one type, so one kind of step"),
                });
            let common = access.projections.len().min(loaned.projections.len());
            let past_storage = access.own_storage
                && loaned.projections[common..].contains(&Projection::Dereference);
            // A reserved loan acts as a shared one until its call.
            let exclusive = *mutable && !self.reserved.has(loan);
            overlaps && !past_storage && !(access.reads && !exclusive)
        })?;
        reported.set(loan);
        let borrow = &self.body.instructions[self.borrows[loan].0 as usize];
        let kind = match borrow.kind {
            InstructionKind::Borrow { mutable: true, .. } => "a `&mut` reference",
            _ => "a reference",
        };
        let name = self.variable_name(access.variable, source);
        Some(Diagnostic {
            code: "T0037",
            message: format!(
                "{name} is {} while {kind} to it is still in use",
                access.event
            ),
            span: access.span,
        })
    }

    /// At a call, the first loan broken when a reserved `&mut` argument takes
    /// hold: a `&mut` borrow of its place, checked against every holder
    /// `live` just before the call (used after it, or an argument of it)
    /// other than the argument itself.
    fn activation_error(
        &self,
        id: InstructionId,
        held: &Held,
        live: &Bits,
        reported: &mut Bits,
        source: &str,
    ) -> Option<Diagnostic> {
        let InstructionKind::Call { arguments, .. } = &self.body.instructions[id.0 as usize].kind
        else {
            return None;
        };
        for &argument in arguments {
            let Operand::Value(borrow) = argument else {
                continue;
            };
            if !matches!(
                self.body.instructions[borrow.0 as usize].kind,
                InstructionKind::Borrow { .. }
            ) || !self.reserved.has(self.loan(borrow))
            {
                continue;
            }
            let Some(mut access) = self.access(borrow) else {
                unreachable!("a borrow accesses its place")
            };
            access.reads = false; // it takes hold now
            let skip = self.value_holders(borrow);
            if let Some(error) = self.broken_loan(&access, held, live, skip, reported, source) {
                return Some(error);
            }
        }
        None
    }
}
