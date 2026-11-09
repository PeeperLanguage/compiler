//! The phases between lowering and the backends, on MIR alone: this crate
//! depends on `model` only, so it cannot name a feature, and knows a type
//! only by its facts.
//!
//! The path checks find errors that depend on which paths execution can
//! take through one function, over its MIR's graph of blocks:
//!
//! - `report_missing_return` (T0017): a function giving a value whose
//!   closing `}` can be reached;
//! - `variable_state` (T0039, T0034, T0035, T0007): a variable used where
//!   some path left it unset or moved it, a move out of a part, and a
//!   variable without `mut` set where some path already set it;
//! - `loans` (T0037, T0036): a place used in a way a reference still in use
//!   forbids, and a returned reference to the function's own storage;
//! - `warn_unreachable_code` (W0001, a warning): code no path reaches.
//!
//! Their codes and messages are the real compiler's (`crates/middle/flow`);
//! the real errors also have labels, notes and help, which the demo leaves
//! out. `cleanup` then removes what only the checks needed.

pub mod cleanup;
mod loans;
mod variable_state;

use model::mir::BlockId;
use model::mir::Body;
use model::mir::InstructionKind;
use model::mir::Program;
use model::mir::Terminator;
use model::span::Span;
use model::types::TypeId;

/// A path check's finding: its code, its message, where it is.
pub struct Diagnostic {
    pub code: &'static str,
    pub message: String,
    pub span: Span,
}

pub struct Report {
    pub errors: Vec<Diagnostic>,
    pub warnings: Vec<Diagnostic>,
}

/// Runs every path check on every function of `program`, written in
/// `source`, which gives the names in messages.
pub fn check(program: &Program, source: &str) -> Report {
    let mut report = Report {
        errors: Vec::new(),
        warnings: Vec::new(),
    };
    for body in &program.functions {
        report_missing_return(body, &mut report.errors);
        variable_state::report(body, source, &mut report.errors);
        loans::report(body, program, source, &mut report.errors);
        warn_unreachable_code(body, &mut report.warnings);
    }
    report
}

fn slice(source: &str, span: Span) -> &str {
    &source[span.start as usize..span.end as usize]
}

/// Whether the instruction does anything a program shows: all but a
/// `ScopeEnd`, which is for the checks.
fn runs(kind: &InstructionKind) -> bool {
    !matches!(kind, InstructionKind::ScopeEnd(_))
}

/// A function giving a value whose closing `}` can be reached. The error is
/// written where the first path that gets there leaves a branch or a block,
/// or at the `}` when the last block runs code itself.
fn report_missing_return(body: &Body, errors: &mut Vec<Diagnostic>) {
    if body.result == TypeId::VOID {
        return;
    }
    let end = (body.blocks.iter().enumerate()).find_map(|(index, block)| match block.terminator {
        Terminator::FallOff(span) => Some((BlockId(index as u32), span)),
        _ => None,
    });
    let Some((end, end_span)) = end else {
        return;
    };
    let reachable = reachable(body);
    if !reachable[end.0 as usize] {
        return;
    }
    let first_cause = missing_return_causes(body, end, &reachable)
        .into_iter()
        .min_by_key(|span| span.start);
    errors.push(Diagnostic {
        code: "T0017",
        message: "not all control paths return a value".to_string(),
        span: first_cause.unwrap_or(end_span),
    });
}

/// Where each path arrives at `end`: back from it through blocks that do
/// nothing, the first branch condition or last code met. Nothing when `end`
/// itself runs code, as one `return` there fixes every path.
fn missing_return_causes(body: &Body, end: BlockId, reachable: &[bool]) -> Vec<Span> {
    let code = |block: BlockId| {
        let block = &body.blocks[block.0 as usize];
        (block.instructions.iter().rev())
            .map(|id| &body.instructions[id.0 as usize])
            .find(|instruction| runs(&instruction.kind))
            .map(|instruction| instruction.span)
    };
    let mut causes = Vec::new();
    if code(end).is_some() {
        return causes;
    }
    let predecessors = predecessors(body);
    let mut visited = vec![false; body.blocks.len()];
    let mut pending = vec![end];
    while let Some(block) = pending.pop() {
        for &predecessor in &predecessors[block.0 as usize] {
            if !reachable[predecessor.0 as usize] {
                continue;
            }
            match body.blocks[predecessor.0 as usize].terminator {
                Terminator::Branch { span, .. } => causes.push(span),
                _ if !std::mem::replace(&mut visited[predecessor.0 as usize], true) => {
                    match code(predecessor) {
                        Some(last) => causes.push(last),
                        None => pending.push(predecessor),
                    }
                }
                _ => {}
            }
        }
    }
    causes
}

/// Warns once at the first code of each stretch no path reaches, such as
/// code after a `return`; what that stretch leads to is not warned about
/// again. Code is an instruction that runs, a `return` or a branch.
fn warn_unreachable_code(body: &Body, warnings: &mut Vec<Diagnostic>) {
    let mut seen = reachable(body);
    let mut starts: Vec<(Span, BlockId)> = (body.blocks.iter().enumerate())
        .filter(|&(index, _)| !seen[index])
        .filter_map(|(index, block)| {
            let terminator = match block.terminator {
                Terminator::Return { span, .. } | Terminator::Branch { span, .. } => Some(span),
                Terminator::Goto(_) | Terminator::FallOff(_) => None,
            };
            let spans = (block.instructions.iter())
                .map(|id| &body.instructions[id.0 as usize])
                .filter(|instruction| runs(&instruction.kind))
                .map(|instruction| instruction.span);
            // The earliest, and of those the widest: the whole statement.
            let first = spans
                .chain(terminator)
                .min_by_key(|span| (span.start, std::cmp::Reverse(span.end)))?;
            Some((first, BlockId(index as u32)))
        })
        .collect();
    starts.sort_by_key(|&(span, _)| span.start);
    for (span, block) in starts {
        if seen[block.0 as usize] {
            continue;
        }
        warnings.push(Diagnostic {
            code: "W0001",
            message: "unreachable code".to_string(),
            span,
        });
        mark_reachable(body, block, &mut seen);
    }
}

/// Which blocks control can reach from the entry, by index.
fn reachable(body: &Body) -> Vec<bool> {
    let mut reachable = vec![false; body.blocks.len()];
    mark_reachable(body, BlockId(0), &mut reachable);
    reachable
}

/// Marks `from` and every block control can reach from it, stopping at
/// blocks already marked.
fn mark_reachable(body: &Body, from: BlockId, marked: &mut [bool]) {
    let mut pending = vec![from];
    while let Some(block) = pending.pop() {
        if !std::mem::replace(&mut marked[block.0 as usize], true) {
            pending.extend(body.blocks[block.0 as usize].terminator.targets());
        }
    }
}

/// The blocks that go straight to each block, by index.
fn predecessors(body: &Body) -> Vec<Vec<BlockId>> {
    let mut predecessors = vec![Vec::new(); body.blocks.len()];
    for (from, block) in body.blocks.iter().enumerate() {
        for target in block.terminator.targets() {
            predecessors[target.0 as usize].push(BlockId(from as u32));
        }
    }
    predecessors
}

/// Facts carried along a function's edges until nothing changes, which also
/// works once loops add edges back. Gives the facts where each block is
/// entered (`None` for one never entered): `seeds` are the first blocks
/// with their facts, `edges` the blocks entered after one, `run` turns the
/// facts where a block is entered into those where it is left, and `join`
/// gives the facts where paths meet.
fn dataflow<F: Clone + PartialEq, E: IntoIterator<Item = BlockId>>(
    blocks: usize,
    seeds: impl IntoIterator<Item = (BlockId, F)>,
    edges: impl Fn(BlockId) -> E,
    run: impl Fn(BlockId, &mut F),
    join: impl Fn(&F, &F) -> F,
) -> Vec<Option<F>> {
    let mut entered: Vec<Option<F>> = vec![None; blocks];
    let mut pending = Vec::new();
    for (block, facts) in seeds {
        entered[block.0 as usize] = Some(facts);
        pending.push(block);
    }
    while let Some(block) = pending.pop() {
        let mut facts = entered[block.0 as usize]
            .clone()
            .expect("only entered blocks are pending");
        run(block, &mut facts);
        for next in edges(block) {
            let joined = match &entered[next.0 as usize] {
                None => facts.clone(),
                Some(there) => join(there, &facts),
            };
            if entered[next.0 as usize].as_ref() != Some(&joined) {
                entered[next.0 as usize] = Some(joined);
                pending.push(next);
            }
        }
    }
    entered
}

/// A set of indexes below a count, one bit each.
#[derive(Clone, PartialEq)]
struct Bits {
    words: Vec<u64>,
}

impl Bits {
    fn new(count: usize) -> Bits {
        Bits {
            words: vec![0; count.div_ceil(64)],
        }
    }

    fn set(&mut self, index: usize) {
        self.words[index / 64] |= 1 << (index % 64);
    }

    fn clear(&mut self, index: usize) {
        self.words[index / 64] &= !(1 << (index % 64));
    }

    fn has(&self, index: usize) -> bool {
        self.words[index / 64] >> (index % 64) & 1 != 0
    }

    /// Sets `index` if it is not in the set, else clears it.
    fn toggle(&mut self, index: usize) {
        self.words[index / 64] ^= 1 << (index % 64);
    }

    /// The indexes in the set, in order.
    fn ids(&self) -> impl Iterator<Item = usize> + '_ {
        (self.words.iter().enumerate()).flat_map(|(index, &word)| {
            let mut rest = word;
            std::iter::from_fn(move || {
                let bit = (rest != 0).then(|| rest.trailing_zeros() as usize)?;
                rest &= rest - 1;
                Some(index * 64 + bit)
            })
        })
    }

    /// The set whose words are `op` of the two sets' words.
    fn combine(&self, other: &Bits, op: fn(u64, u64) -> u64) -> Bits {
        let words = self.words.iter().zip(&other.words);
        Bits {
            words: words.map(|(&a, &b)| op(a, b)).collect(),
        }
    }
}
