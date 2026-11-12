//! The unreachable-code warning (W0001): code no path reaches.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use source::SourceFile;
use source::Span;

/// Warns once at the first code of each stretch that no path reaches, such as
/// code after a `return`; what that stretch leads to is not warned about
/// again. Code is an instruction, a `return` or a branch; a jump between
/// blocks or the closing brace is not.
pub fn warn_unreachable_code(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let mut seen = body.reachable();
    // (the first code written, its block) of each block not reached.
    let mut starts: Vec<(Span, Id<mir::Block>)> = body
        .blocks
        .ids()
        .filter(|block| !seen[block.index()])
        .filter_map(|block| {
            let mir::Block {
                instructions,
                terminator,
                ..
            } = &body.blocks[block];
            let terminator = match *terminator {
                mir::Terminator::Return { span, .. }
                | mir::Terminator::Panic { span, .. }
                | mir::Terminator::Branch { span, .. } => Some(span),
                mir::Terminator::Jump(_) | mir::Terminator::FallOff(_) => None,
            };
            let spans = body.lists.items(*instructions);
            let spans = spans
                .filter(|&instruction| body.instructions[instruction].kind.runs())
                .map(|instruction| body.instructions.span(instruction));
            // The earliest, and of those the widest: the whole statement.
            let first = spans
                .chain(terminator)
                .min_by_key(|span| (span.start, std::cmp::Reverse(span.end)))?;
            Some((first, block))
        })
        .collect();
    starts.sort_by_key(|&(span, _)| span.start);
    for (span, block) in starts {
        if seen[block.index()] {
            continue; // follows code already warned about
        }
        diagnostics.push(
            Diagnostic::new(diagnostics::UNREACHABLE_CODE, "unreachable code").label(
                file,
                span,
                "this never runs",
            ),
        );
        body.mark_reachable(block, &mut seen);
    }
}
