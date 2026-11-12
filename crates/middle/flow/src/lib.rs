//! Path checks: errors that depend on which paths execution can take through
//! one function. Type checking looks at each expression on its own; whether a
//! value is returned, or a variable set, depends on the route taken to a line,
//! and only the MIR's graph of blocks shows every route.
//!
//! What they check, each over one function's MIR:
//!
//! - `missing_return::report_missing_return` (T0017): a function with a
//!   result whose closing `}` can be reached, and so ends without returning
//!   a value. The error names how each path gets there: a branch side, or a
//!   nested block that ends.
//! - `variable_state::report_variable_state_errors` (T0039, T0034, T0035 and
//!   T0007): a variable used where some path left it unset or moved it, a
//!   value moved out of what holds it, and a variable without `mut` assigned
//!   where some path already set it. This is what lets `let x: i32;` be set
//!   once later.
//! - `loans::report_loan_errors` (T0037, T0036 and T0016): a place used in a way that a
//!   reference still in use forbids, or a variable whose scope ends while a
//!   reference to it is still in use.
//! - `unreachable::warn_unreachable_code` (W0001, a warning): code no path
//!   reaches, such as code after a `return`.
//!
//! How they work: they only read the graph. A block's terminator gives the
//! edges out of it (`Terminator::targets`), and `predecessors` the edges in.
//! Whether a block can be reached is found from the entry. The variable state
//! and loan checks are dataflow: they carry facts along the edges
//! until nothing changes, which also works once loops add edges back.
//!
//! What they do not do:
//!
//! - No type, name or operator rules, and no constant folding: the checker did
//!   those while building THIR. A function whose checking reported an error
//!   is not lowered at all (`Checker::check_body`), so these checks never
//!   repeat or follow from an earlier error.
//! - No changes to the MIR, and no fixes: they only report.
//! - No use of constant values to decide which paths exist. `if false { ... }`
//!   still has two sides, as in Go and Rust, so whether code compiles never
//!   depends on a constant's value; a label only says which side the constant
//!   takes.
//! - No errors from blocks that cannot be reached: the unreachable-code
//!   check warns about them once, and the other checks skip them.
//! - No warning that a condition is always true or false: `if DEBUG` with a
//!   constant `DEBUG` is how code is switched on and off, and Go, Rust and Zig
//!   do not warn there either (decided by the owner, 2026-10-17).
//! - No building of the MIR: the `lower` crate does that, and these checks run
//!   after it, called by `compile`.

use std::marker::PhantomData;

use arena::Id;
use model::mir;

pub mod loans;
pub mod missing_return;
pub mod unreachable;
pub mod variable_state;

/// Facts carried along a function's edges until nothing changes, which also
/// works once loops add edges back: forward from the entry, or backward from
/// the blocks that leave the function. Gives the facts where each block is
/// entered in that direction (its start forward, its end backward), `None`
/// for a block never entered. `seeds` are the first blocks with their facts;
/// `edges` gives the blocks entered after one; `run` turns the facts where a
/// block is entered into those where it is left; `join` gives the facts
/// where paths meet.
fn dataflow<B, F: Clone + PartialEq, E: IntoIterator<Item = Id<B>>>(
    blocks: usize,
    seeds: impl IntoIterator<Item = (Id<B>, F)>,
    edges: impl Fn(Id<B>) -> E,
    run: impl Fn(Id<B>, &mut F),
    join: impl Fn(&F, &F) -> F,
) -> Vec<Option<F>> {
    let mut entered = vec![None; blocks];
    let mut pending = Vec::new();
    for (block, facts) in seeds {
        entered[block.index()] = Some(facts);
        pending.push(block);
    }
    while let Some(block) = pending.pop() {
        let Some(mut facts) = entered[block.index()].clone() else {
            unreachable!("only entered blocks are pending")
        };
        run(block, &mut facts);
        for next in edges(block) {
            let joined = match &entered[next.index()] {
                None => facts.clone(),
                Some(facts_there) => join(facts_there, &facts),
            };
            if entered[next.index()].as_ref() != Some(&joined) {
                entered[next.index()] = Some(joined);
                pending.push(next);
            }
        }
    }
    entered
}

/// The blocks that go straight to each block, by index.
fn predecessors(body: &mir::Body) -> Vec<Vec<Id<mir::Block>>> {
    let mut predecessors = vec![Vec::new(); body.blocks.len()];
    for from in body.blocks.ids() {
        for target in body.blocks[from].terminator.targets() {
            predecessors[target.index()].push(from);
        }
    }
    predecessors
}

/// A set of ids of `T`, one bit per id: only an `Id<T>` goes in, so ids of
/// different things, such as variables and loans, cannot be mixed up.
struct Bits<T> {
    words: Vec<u64>,
    kind: PhantomData<fn() -> T>,
}

// Derived impls would require T itself to be Clone and PartialEq.
impl<T> Clone for Bits<T> {
    fn clone(&self) -> Bits<T> {
        Bits {
            words: self.words.clone(),
            kind: PhantomData,
        }
    }
}
impl<T> PartialEq for Bits<T> {
    fn eq(&self, other: &Bits<T>) -> bool {
        self.words == other.words
    }
}

impl<T> Bits<T> {
    /// No id in it, for ids below `count`.
    fn new(count: usize) -> Bits<T> {
        Bits {
            words: vec![0; count.div_ceil(64)],
            kind: PhantomData,
        }
    }

    fn set(&mut self, id: Id<T>) {
        self.words[id.index() / 64] |= 1 << (id.index() % 64);
    }

    fn clear(&mut self, id: Id<T>) {
        self.words[id.index() / 64] &= !(1 << (id.index() % 64));
    }

    /// Sets `id` if it is not in the set, else clears it.
    fn toggle(&mut self, id: Id<T>) {
        self.words[id.index() / 64] ^= 1 << (id.index() % 64);
    }

    /// The ids in the set, in order.
    fn ids(&self) -> impl Iterator<Item = Id<T>> {
        self.words.iter().enumerate().flat_map(|(index, &word)| {
            let mut rest = word;
            std::iter::from_fn(move || {
                let bit = (rest != 0).then(|| rest.trailing_zeros() as usize)?;
                rest &= rest - 1; // clears that lowest bit
                Some(Id::from_index(index * 64 + bit))
            })
        })
    }

    fn has(&self, id: Id<T>) -> bool {
        self.words[id.index() / 64] >> (id.index() % 64) & 1 != 0
    }

    /// The set whose words are `op` of the two sets' words: `|` for the
    /// union, `&` for the ids in both.
    fn combine(&self, other: &Bits<T>, op: fn(u64, u64) -> u64) -> Bits<T> {
        let words = self.words.iter().zip(&other.words);
        Bits {
            words: words.map(|(&a, &b)| op(a, b)).collect(),
            kind: PhantomData,
        }
    }
}
