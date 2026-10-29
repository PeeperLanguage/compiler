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
//!    replaces them and ends each loan of a place through it, which now
//!    names what the new reference points to.
//! 2. Backward: the holders used again later (liveness).
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
//! A reference a call returns holds the loans of the arguments its
//! function's origins name (`in`), so they stay borrowed while it is in use;
//! a `&mut` argument there is never reserved, as its loan outlives the call.
//! Each reference parameter holds a parameter loan, numbered after the
//! borrows, for what the caller lent; it conflicts with nothing here. A
//! returned reference may hold only the parameter loans the origins name,
//! and no loan of the function's own storage (T0036).

use std::collections::VecDeque;
use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::DefId;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

use crate::Bits;
use crate::dataflow;
use crate::predecessors;

/// A variable or instruction value of reference type, numbered from 0 in
/// that order. Only named by `Id<Holder>`.
enum Holder {}

/// One `Borrow`, numbered from 0 in instruction order, then one parameter
/// loan for each parameter. Only named by `Id<Loan>`.
enum Loan {}

/// Reports each access that a reference still in use forbids, once per
/// loan, in the body of `function`; `origins_of(definition)` gives the
/// parameters a reference `definition` returns may point into. A body with
/// no `Borrow` and no reference result has no loan to break and is not
/// walked.
pub fn report_loan_errors<'t>(
    body: &mir::Body,
    types: &Types,
    function: DefId,
    origins_of: &dyn Fn(DefId) -> &'t [u32],
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
    if borrows.is_empty() && types[body.result].borrows().is_none() {
        return;
    }
    let variables = body.variables.ids().map(|id| body.variables[id].ty);
    let values = body.instructions.ids().map(|id| body.instructions[id].ty);
    let mut holder_count = 0;
    let mut holders = Vec::with_capacity(variables.len() + values.len());
    for ty in variables.chain(values) {
        if types[ty].borrows().is_some() {
            holders.push(Some(Id::from_index(holder_count)));
            holder_count += 1;
        } else {
            holders.push(None);
        }
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
        origins_of,
        origins: origins_of(function),
        reserved: Bits::new(0), // found below: it needs `Loans::uses`
        borrows,
        holders,
        loans_through,
    };
    loans.reserved = loans.reserved_loans();
    loans.report(file, diagnostics);
}

/// One function's loans and holders.
struct Loans<'b, 't> {
    body: &'b mir::Body,
    types: &'b Types, // to name a reference or a slice in a message
    origins_of: &'b dyn Fn(DefId) -> &'t [u32], // of a function it calls
    origins: &'t [u32], // the parameters its returned reference may point into
    borrows: Vec<Id<mir::Instruction>>, // each `Borrow` loan's instruction
    /// The holder of each variable, then of each instruction's value;
    /// `None` for one that is not a reference.
    holders: Vec<Option<Id<Holder>>>,
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
    writable: "`[&mut]`",
    to: "of",
    into: "of",
    mutably_borrowed: "borrowed as `[&mut]`",
};

/// What an instruction does to a place, as the loan rules see it.
struct Access<'p> {
    variable: Id<mir::Variable>,
    projections: &'p [mir::Projection],
    subject: &'static str, // what is done to: "" (the place) or "address of "
    event: &'static str,   // for the message: "changed", "read", ...
    label: &'static str,   // where it is done: "read", "address taken", ...
    reads: bool,           // only reads it, which a shared loan allows
    replaces: bool,        // replaces or ends the place's own storage
    span: Span,
}

impl Loans<'_, '_> {
    /// The words for a borrow of the type `ty`, a reference or slice.
    fn words(&self, ty: TypeId) -> &'static BorrowWords {
        match self.types[ty] {
            Type::Slice { .. } => &SLICE_WORDS,
            _ => &REFERENCE_WORDS,
        }
    }

    /// The borrows, then one parameter loan per parameter.
    fn loan_count(&self) -> usize {
        self.borrows.len() + self.body.parameters
    }

    /// How many holders there are: the variables and values that are
    /// references.
    fn holder_count(&self) -> usize {
        self.holders.iter().flatten().count()
    }

    fn variable_holder(&self, variable: Id<mir::Variable>) -> Option<Id<Holder>> {
        self.holders[variable.index()]
    }

    fn value_holder(&self, value: Id<mir::Instruction>) -> Option<Id<Holder>> {
        self.holders[self.body.variables.len() + value.index()]
    }

    /// The holder a `return` ending `block` hands back, used there, and
    /// the `return`'s span.
    fn returned(&self, block: Id<mir::Block>) -> Option<(Id<Holder>, Span)> {
        match self.body.blocks[block].terminator {
            mir::Terminator::Return {
                value: Some(ref value),
                span,
            } => self.operand_holder(value).map(|holder| (holder, span)),
            _ => None,
        }
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
                    if let Some(holder) = self.operand_holder(operand) {
                        used(holder);
                    }
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
                    function,
                    ref arguments,
                } => {
                    // Used again through the returned reference, so its loan
                    // outlives the call.
                    for &position in (self.origins_of)(function) {
                        if let Some(holder) = self.operand_holder(&arguments[position as usize]) {
                            use_counts[holder.index()] += 1;
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
            if let Some(holder) = self.value_holder(borrow)
                && mutable
                && use_counts[holder.index()] == 1
                && call_arguments.has(holder)
            {
                reserved.set(self.loan(borrow));
            }
        }
        reserved
    }

    /// `held`, the loans of each holder, after the instruction `id`.
    fn step_held(&self, held: &mut [Bits<Loan>], id: Id<mir::Instruction>) {
        // The holder given new loans, the holder whose loans it takes, and
        // a loan of its own. A reference read from a variable, or borrowed
        // through one, holds that variable's loans; a parameter, its
        // parameter loan.
        let (target, source, own) = match self.body.instructions[id].kind {
            mir::InstructionKind::Store {
                ref place,
                ref value,
            } => (
                place
                    .whole_variable()
                    .and_then(|variable| self.variable_holder(variable)),
                self.operand_holder(value),
                match *value {
                    // Its parameter loan, after the borrows.
                    mir::Operand::Argument(position) => {
                        Some(Id::from_index(self.borrows.len() + position as usize))
                    }
                    _ => None,
                },
            ),
            mir::InstructionKind::Call {
                function,
                ref arguments,
            } => {
                let Some(target) = self.value_holder(id) else {
                    return;
                };
                let mut loans = Bits::new(self.loan_count());
                for &position in (self.origins_of)(function) {
                    if let Some(source) = self.operand_holder(&arguments[position as usize]) {
                        loans = loans.combine(&held[source.index()], |a, b| a | b);
                    }
                }
                held[target.index()] = loans;
                return;
            }
            mir::InstructionKind::Load(ref place) | mir::InstructionKind::Move(ref place) => (
                self.value_holder(id),
                self.variable_holder(place.variable),
                None,
            ),
            mir::InstructionKind::Borrow { ref place, .. } => {
                let through = place.is_behind_reference();
                (
                    self.value_holder(id),
                    through
                        .then(|| self.variable_holder(place.variable))
                        .flatten(),
                    Some(self.loan(id)),
                )
            }
            _ => (self.value_holder(id), None, None),
        };
        if let Some(target) = target {
            held[target.index()] = match source {
                Some(source) => held[source.index()].clone(),
                None => Bits::new(self.loan_count()),
            };
            if let Some(loan) = own {
                held[target.index()].set(loan);
            }
        }
        // A store to `r` repoints it, so `r.*` no longer names what a loan
        // of it borrowed: that loan ends. What it points into stays borrowed
        // by the loans its reference took from `r`.
        if let mir::InstructionKind::Store { ref place, .. } = self.body.instructions[id].kind
            && let Some(variable) = place.whole_variable()
        {
            for &loan in &self.loans_through[variable.index()] {
                held.iter_mut().for_each(|holding| holding.clear(loan));
            }
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
        let holder_count = self.holder_count();
        let no_holders = Bits::new(holder_count);
        let no_loans = vec![Bits::new(self.loan_count()); holder_count];
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
        // From every block, not only those that leave the function: a block
        // of an endless `for {}` reaches no exit. A `return` block, which
        // nothing follows, ends with its returned reference in use.
        let ends = dataflow(
            body.blocks.len(),
            body.blocks.ids().map(|block| {
                let mut live = no_holders.clone();
                if let Some((returned, _)) = self.returned(block) {
                    live.set(returned);
                }
                (block, live)
            }),
            |block| predecessors[block.index()].iter().copied(),
            |block, live| {
                let ids: Vec<_> = instructions(block).collect();
                for &id in ids.iter().rev() {
                    self.step_live(live, id);
                }
            },
            |a: &Bits<Holder>, b: &Bits<Holder>| a.combine(b, |a, b| a | b),
        );
        let mut reported = Bits::new(self.loan_count());
        let mut errors = Vec::new();
        for ((block, start), end) in body.blocks.ids().zip(starts).zip(ends) {
            let Some(mut held) = start else {
                continue; // not reached
            };
            let ids: Vec<_> = instructions(block).collect();
            // First, so a local whose reference is returned is reported as
            // that, not as gone while its reference is in use.
            if let Some((returned, span)) = self.returned(block) {
                let mut at_return = held.clone();
                for &id in &ids {
                    self.step_held(&mut at_return, id);
                }
                let loans = &at_return[returned.index()];
                self.return_errors(span, loans, &mut reported, file, &mut errors);
            }
            // The holders used later, after each instruction.
            let mut live = end.unwrap_or_else(|| no_holders.clone());
            let mut live_after = vec![no_holders.clone(); ids.len()];
            for (position, &id) in ids.iter().enumerate().rev() {
                live_after[position] = live.clone();
                self.step_live(&mut live, id);
            }
            for (position, &id) in ids.iter().enumerate() {
                let live = &live_after[position];
                let access = self.access(id);
                let skip = self.defines(id);
                let error = access.and_then(|access| {
                    self.broken_loan(&access, &held, live, skip, &mut reported, file)
                });
                let error =
                    error.or_else(|| self.activation_error(id, &held, live, &mut reported, file));
                // A reference used only by the call has no later use to show.
                if let Some((holder, error)) = error {
                    let later = self.next_use(holder, block, position);
                    let error = match later {
                        Some(later) => {
                            // The holder's variable or value, found only for
                            // an error.
                            let index = (self.holders.iter())
                                .position(|&found| found == Some(holder))
                                .expect("each holder is a variable's or a value's");
                            let variables = body.variables.len();
                            let ty = match index.checked_sub(variables) {
                                None => body.variables[Id::from_index(index)].ty,
                                Some(value) => body.instructions[Id::from_index(value)].ty,
                            };
                            let noun = self.words(ty).noun;
                            error.label(file, later, format!("{noun} used later here"))
                        }
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

    /// Adds to `errors` those of the `return` at `span`, whose reference
    /// holds `loans`: one for each loan not yet `reported` of the function's
    /// own storage (a borrow not through a reference), or of a parameter its
    /// origins do not name.
    fn return_errors(
        &self,
        span: Span,
        loans: &Bits<Loan>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
        errors: &mut Vec<(u32, Diagnostic)>,
    ) {
        // Named by what the function returns: a reference into a slice's
        // elements is still a reference.
        let words = self.words(self.body.result);
        let (returned, into) = (words.noun, words.into);
        for loan in (0..self.loan_count()).map(Id::<Loan>::from_index) {
            if !loans.has(loan) || reported.has(loan) {
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
                let name = file.slice(self.body.variables.span(place.variable));
                let message = format!(
                    "returns a {returned} {into} `{name}`, which is gone once the function returns"
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
                    replaces: false,
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
                    replaces: true,
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
            replaces,
            span: self.body.instructions.span(id),
        })
    }

    /// The first loan not yet `reported` that `access` breaks, held in
    /// `held` by a holder `live` after it other than those in `skip`: that
    /// holder, and the error without where the holder is used later.
    fn broken_loan(
        &self,
        access: &Access,
        held: &[Bits<Loan>],
        live: &Bits<Holder>,
        skip: [Option<Id<Holder>>; 2],
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let holders = held
            .iter()
            .enumerate()
            .map(|(index, holding)| (Id::from_index(index), holding));
        let holders =
            holders.filter(|&(holder, _)| live.has(holder) && !skip.contains(&Some(holder)));
        for (holder, holding) in holders {
            let loans = (0..self.borrows.len()).map(Id::from_index);
            for loan in loans.filter(|&loan| holding.has(loan) && !reported.has(loan)) {
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
                let past_storage = access.replaces
                    && (loaned.projections[common..].iter())
                        .any(|step| matches!(step, mir::Projection::Dereference));
                // A reserved loan acts as a shared one until its call.
                let exclusive = mutable && !self.reserved.has(loan);
                if !overlaps || past_storage || (access.reads && !exclusive) {
                    continue;
                }
                reported.set(loan);
                let name = file.slice(self.body.variables.span(access.variable));
                let words = self.words(self.body.instructions[borrow].ty);
                let (noun, to) = (words.noun, words.to);
                let kind = if mutable {
                    format!("a {} {noun}", words.writable)
                } else {
                    format!("a {noun}")
                };
                let (subject, event) = (access.subject, access.event);
                let message =
                    format!("{subject}`{name}` is {event} while {kind} {to} it is still in use");
                let error = Diagnostic::new(diagnostics::BORROW_CONFLICT, message)
                    .label(file, access.span, format!("{} here", access.label))
                    .label(
                        file,
                        self.body.instructions.span(borrow),
                        format!("{noun} made here"),
                    );
                return Some((holder, error));
            }
        }
        None
    }

    /// At a call, the first loan broken when a reserved `&mut` argument takes
    /// hold: a `&mut` borrow of its place, checked against every holder in
    /// use just before the call (`live` after it, or an argument of it)
    /// other than the argument itself.
    fn activation_error(
        &self,
        id: Id<mir::Instruction>,
        held: &[Bits<Loan>],
        live: &Bits<Holder>,
        reported: &mut Bits<Loan>,
        file: &Arc<SourceFile>,
    ) -> Option<(Id<Holder>, Diagnostic)> {
        let mir::InstructionKind::Call { ref arguments, .. } = self.body.instructions[id].kind
        else {
            return None;
        };
        let mut live_before = None; // found only for a call with a reserved argument
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
            let skip = [self.value_holder(borrow), None];
            let live_before = live_before.get_or_insert_with(|| {
                let mut live_before = live.clone();
                self.step_live(&mut live_before, id);
                live_before
            });
            if let Some((holder, error)) =
                self.broken_loan(&access, held, live_before, skip, reported, file)
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
                if self.defines(id).contains(&Some(holder)) {
                    continue 'paths; // set again: this path does not use it
                }
            }
            if let Some((returned, span)) = self.returned(block)
                && returned == holder
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
