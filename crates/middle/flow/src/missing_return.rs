//! The missing-return check (T0017): a function with a result whose
//! closing `}` can be reached.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use source::SourceFile;
use source::Span;

use crate::predecessors;

/// The most paths one missing-return error names.
const SHOWN_CAUSES: usize = 3;

/// Reports a function, written in `file` and giving a result, whose end
/// can be reached without a `return`. `result_text` names the result's
/// type, asked only for the error.
///
/// Lowering ends such a function's last block with `FallOff` at its closing
/// brace, so the question is whether that block can be reached. If it can,
/// the error names the paths that arrive there right after a branch side or a
/// nested block, at most `SHOWN_CAUSES` of them. When the last block itself
/// runs statements of the function body, only the end is named, since one
/// `return` there fixes every path.
pub fn report_missing_return(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    result_text: impl FnOnce() -> String,
    diagnostics: &mut Vec<Diagnostic>,
) {
    if body.signature.result.is_none() {
        return; // such a function returns at its closing brace
    }
    let end = body
        .blocks
        .ids()
        .find_map(|block| match body.blocks[block].terminator {
            mir::Terminator::FallOff(span) => Some((block, span)),
            _ => None,
        });
    let Some((end, end_span)) = end else {
        return;
    };
    let reachable = body.reachable();
    if !reachable[end.index()] {
        return;
    }
    let mut causes = missing_return_causes(body, end);
    causes.sort_by_key(|&(span, _): &(Span, &str)| span.start);
    let hidden = causes.len().saturating_sub(SHOWN_CAUSES);
    let end_label = format!("function ends without returning `{}`", result_text());
    let mut error = Diagnostic::new(
        diagnostics::MISSING_RETURN,
        "not all control paths return a value",
    );
    for &(span, label) in &causes[..causes.len() - hidden] {
        error = error.label(file, span, label);
    }
    error = error
        .label(file, end_span, end_label)
        .note("execution can reach the end of the function without a `return`");
    if hidden > 0 {
        error = error.note(format!("{hidden} more path(s) like these are not shown"));
    }
    let help = if causes.is_empty() {
        "add a `return` at the end of the function"
    } else {
        "add a `return` at the end of the function, or make each path shown return a value"
    };
    diagnostics.push(error.help(help, Vec::new()));
}

/// Where each path arrives at `end`, a reachable block, and the label for it:
/// nothing when `end` itself runs code, as one `return` there fixes every path.
fn missing_return_causes(body: &mir::Body, end: Id<mir::Block>) -> Vec<(Span, &'static str)> {
    let mut causes = Vec::new();
    let mut end_code = body.lists.items(body.blocks[end].instructions);
    if end_code.any(|id| body.instructions[id].kind.runs()) {
        return causes;
    }
    let predecessors = predecessors(body);
    // Back from the end through blocks that do nothing: the first thing
    // met on each path is how that path gets there. A branch is met once
    // through each side that leads here.
    let mut visited = vec![false; body.blocks.len()];
    let mut pending = vec![end];
    while let Some(block) = pending.pop() {
        for &predecessor in &predecessors[block.index()] {
            let mir::Block {
                instructions,
                terminator,
                ..
            } = &body.blocks[predecessor];
            if let mir::Terminator::Branch {
                condition,
                span,
                then,
                ..
            } = *terminator
            {
                // A constant condition still has two sides (the module doc
                // of `lib.rs`); the label only says which side it takes.
                let always = match condition {
                    mir::Operand::Constant { bits, .. } => Some(bits[0] != 0),
                    _ => None,
                };
                let side = then == block;
                let label = match (always, side) {
                    (Some(true), true) => "this is always true, so nothing is returned",
                    (Some(false), false) => "this is always false, so nothing is returned",
                    (_, true) => "if this is true, nothing is returned",
                    (_, false) => "if this is false, nothing is returned",
                };
                match causes.iter_mut().find(|(cause, _)| *cause == span) {
                    None => causes.push((span, label)),
                    Some(other_side) if always.is_none() => {
                        other_side.1 = "either way, nothing is returned";
                    }
                    Some(other_side) if always == Some(side) => other_side.1 = label,
                    Some(_) => {}
                }
            } else if !std::mem::replace(&mut visited[predecessor.index()], true) {
                let code = body.lists.items(*instructions);
                match code.filter(|&id| body.instructions[id].kind.runs()).last() {
                    Some(last) => causes.push((
                        body.instructions.span(last),
                        "this path ends without returning",
                    )),
                    None => pending.push(predecessor),
                }
            }
        }
    }
    causes
}
