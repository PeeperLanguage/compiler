//! What the path checks needed and nothing after them does: `ScopeEnd`s,
//! blocks that only jump, and blocks no path reaches. Run on a program
//! with no errors, before the interpreter or a dump reads it.

use model::mir::BlockId;
use model::mir::Body;
use model::mir::InstructionKind;
use model::mir::Program;
use model::mir::Terminator;

use crate::reachable;

/// Removes from every body what no phase after the path checks uses. The
/// block order stays, so the entry is still the first block.
pub fn clean(program: &mut Program) {
    for body in &mut program.functions {
        remove_scope_ends(body);
        skip_empty_blocks(body);
        remove_unreachable_blocks(body);
    }
}

fn remove_scope_ends(body: &mut Body) {
    let instructions = &body.instructions;
    for block in &mut body.blocks {
        (block.instructions).retain(|&id| {
            !matches!(
                instructions[id.0 as usize].kind,
                InstructionKind::ScopeEnd(_)
            )
        });
    }
}

/// Makes each jump to a block that only jumps go straight past it. An
/// endless empty loop has no end, so a jump stops somewhere on it, after as
/// many jumps as there are blocks.
fn skip_empty_blocks(body: &mut Body) {
    for block in 0..body.blocks.len() {
        let mut terminator = body.blocks[block].terminator.clone();
        terminator.retarget(|mut target| {
            for _ in 0..body.blocks.len() {
                let next = &body.blocks[target.0 as usize];
                match next.terminator {
                    Terminator::Goto(next_target) if next.instructions.is_empty() => {
                        target = next_target;
                    }
                    _ => break,
                }
            }
            target
        });
        body.blocks[block].terminator = terminator;
    }
}

/// Removes the blocks no path reaches and numbers the rest from 0.
fn remove_unreachable_blocks(body: &mut Body) {
    let reached = reachable(body);
    let mut count = 0;
    let new_ids: Vec<BlockId> = (reached.iter())
        .map(|&kept| {
            count += u32::from(kept);
            BlockId(count.saturating_sub(1))
        })
        .collect();
    let blocks = std::mem::take(&mut body.blocks);
    for (mut block, kept) in blocks.into_iter().zip(reached) {
        if kept {
            block
                .terminator
                .retarget(|target| new_ids[target.0 as usize]);
            body.blocks.push(block);
        }
    }
}
