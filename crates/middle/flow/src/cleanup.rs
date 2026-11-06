//! What the path checks needed and nothing after them does: `ScopeEnd`s,
//! blocks that only jump, and blocks no path reaches. Whoever reads a compiled
//! body runs this first, on a body with no errors: the backend and a dump
//! (the driver) and a program's run (`compile::interpret`). A body built for a
//! constant's run does not come here, as that run ends memory at each
//! `ScopeEnd`, and `peeper check` and the language server read no MIR.

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Terminator;

use crate::reachable;

/// Removes what no later phase uses. The block order stays, so the entry is
/// still the first block.
pub fn remove_unused_code(body: &mut mir::Body) {
    remove_scope_ends(body);
    skip_empty_blocks(body);
    remove_unreachable_blocks(body);
}

fn remove_scope_ends(body: &mut mir::Body) {
    let mut kept: Vec<NonZeroU32> = Vec::new();
    for block in body.blocks.ids() {
        let list = body.blocks[block].instructions;
        let runs = |&id: &Id<mir::Instruction>| body.instructions[id].kind.runs();
        kept.clear();
        kept.extend(body.lists.items(list).filter(runs).map(NonZeroU32::from));
        if kept.len() != list.len() {
            body.blocks[block].instructions = body.lists.add(&kept);
        }
    }
}

/// Makes each jump to a block that only jumps go straight past it. A block
/// with a `Phi` must keep its predecessors, as the `Phi` names them. An
/// endless empty loop has no end, so a jump stops somewhere on it, after as
/// many jumps as there are blocks.
fn skip_empty_blocks(body: &mut mir::Body) {
    for block in body.blocks.ids() {
        let mut terminator = body.blocks[block].terminator.clone();
        terminator.retarget(|mut target| {
            for _ in 0..body.blocks.len() {
                let mir::Block {
                    instructions,
                    terminator: Terminator::Goto(next),
                    ..
                } = body.blocks[target]
                else {
                    break;
                };
                let first = body.lists.items(body.blocks[next].instructions).next();
                let phi = first.is_some_and(|id| {
                    matches!(body.instructions[id].kind, InstructionKind::Phi(_))
                });
                if !instructions.is_empty() || phi {
                    break;
                }
                target = next;
            }
            target
        });
        body.blocks[block].terminator = terminator;
    }
}

/// Removes the blocks no path reaches and numbers the rest from 0. A `Phi` in
/// a block that stays can still name one: a `continue` after a `return`.
fn remove_unreachable_blocks(body: &mut mir::Body) {
    let reachable = reachable(body);
    let mut next_id = 0;
    let new_ids: Vec<Option<Id<mir::Block>>> = (reachable.iter())
        .map(|&kept| {
            kept.then(|| {
                next_id += 1;
                Id::from_index(next_id - 1)
            })
        })
        .collect();
    let new_id = |block: Id<mir::Block>| new_ids[block.index()];
    let mut blocks = Arena::default();
    for block in body.blocks.ids().filter(|&block| reachable[block.index()]) {
        let mut kept = body.blocks[block].clone();
        kept.terminator
            .retarget(|target| new_id(target).expect("a reached block's target is reached"));
        let instructions = kept.instructions;
        blocks.push(kept, body.blocks.span(block));
        for id in body.lists.items(instructions) {
            let InstructionKind::Phi(incoming) = &mut body.instructions[id].kind else {
                break; // `Phi`s come first
            };
            let live = incoming
                .iter()
                .filter_map(|&(from, value)| Some((new_id(from)?, value)));
            *incoming = live.collect();
        }
    }
    body.blocks = blocks;
}
