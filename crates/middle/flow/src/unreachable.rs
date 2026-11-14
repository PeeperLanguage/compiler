//! The unreachable-code warning (W0001): code no path reaches.

use std::sync::Arc;

use arena::Id;
use diagnostics::Diagnostic;
use model::mir;
use source::SourceFile;
use source::Span;

/// Warns once at the code written first in each stretch that no path
/// reaches, such as code after a `return`. A stretch is an unreached block
/// no other jumps to and every block it leads to that an earlier stretch
/// did not; its code can be written out of order, as a `for` works out
/// what it walks before the name it binds. Code is an instruction that
/// runs, a `return`, a `panic` or a branch; a jump between blocks or the
/// closing brace is not.
pub fn warn_unreachable_code(
    body: &mir::Body,
    file: &Arc<SourceFile>,
    diagnostics: &mut Vec<Diagnostic>,
) {
    let mut seen = body.reachable();
    let unreached: Vec<Id<mir::Block>> = body
        .blocks
        .ids()
        .filter(|block| !seen[block.index()])
        .collect();
    let mut entered = vec![false; body.blocks.len()];
    for &block in &unreached {
        for target in body.blocks[block].terminator.targets() {
            entered[target.index()] = true;
        }
    }
    // Code after a `return` or `panic` starts a block nothing jumps to; a
    // loop of blocks nothing enters starts at its first block.
    let starts = (unreached.iter())
        .filter(|block| !entered[block.index()])
        .chain(&unreached);
    let mut warnings = Vec::new();
    for &start in starts {
        if seen[start.index()] {
            continue; // in a stretch already warned about
        }
        let mut first: Option<Span> = None;
        body.mark_reachable(start, &mut seen, |block| {
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
            let spans = (body.lists.items(*instructions))
                .filter(|&instruction| body.instructions[instruction].kind.runs())
                .map(|instruction| body.instructions.span(instruction));
            // The earliest, and of those the widest: the whole statement.
            first = (first.into_iter().chain(spans).chain(terminator))
                .min_by_key(|span| (span.start, std::cmp::Reverse(span.end)));
        });
        warnings.extend(first);
    }
    warnings.sort_by_key(|span| span.start);
    for span in warnings {
        diagnostics.push(
            Diagnostic::new(diagnostics::UNREACHABLE_CODE, "unreachable code").label(
                file,
                span,
                "this never runs",
            ),
        );
    }
}
