//! Laying a diagnostic out: the snippets of source it shows, grouped by file
//! and nearby lines, and the rows its marks are drawn in under each line.

use std::ops::Range;
use std::sync::Arc;

use source::SourceFile;
use source::Span;

use crate::Diagnostic;
use crate::Severity;
use crate::printer::Mark;
use crate::printer::MarkedLine;
use crate::printer::Snippet;
use crate::printer::Stroke;
use crate::printer::cell_of;

/// Arranges the labels of `d` by file: the primary label's file first, then the
/// others by name; lines in order; on a line, marks from left to right with the
/// primary one first. Labels on exactly the same span are drawn once.
pub(crate) fn snippets(d: &Diagnostic) -> Vec<Snippet<'_>> {
    let mut snippets: Vec<Snippet> = Vec::new();
    for (index, label) in d.labels.iter().enumerate() {
        let at = if let Some(at) = snippets
            .iter()
            .position(|s| Arc::ptr_eq(s.file, &label.file))
        {
            at
        } else {
            snippets.push(Snippet {
                file: &label.file,
                line: 0,
                column: 0,
                lines: Vec::new(),
            });
            snippets.len() - 1
        };
        let snippet = &mut snippets[at];
        let span = clamp(&label.file, label.span);
        let (start_line, start_column) = label.file.position(span.start as usize);
        let (end_line, end_column) = label.file.position(span.end as usize);
        if snippet.line == 0 || index == 0 {
            (snippet.line, snippet.column) = (start_line, start_column);
        }
        for number in start_line..=end_line {
            let mut mark = Mark {
                start: if number == start_line {
                    start_column
                } else {
                    1
                },
                end: 0,
                severity: label.severity,
                messages: Vec::new(),
            };
            if number == end_line {
                mark.end = end_column;
                if !label.message.is_empty() {
                    mark.messages.push(&label.message);
                }
            }
            match snippet.lines.iter_mut().find(|l| l.number == number) {
                Some(line) => line.marks.push(mark),
                None => snippet.lines.push(MarkedLine {
                    number,
                    marks: vec![mark],
                }),
            }
        }
    }
    for snippet in &mut snippets {
        snippet.lines.sort_by_key(|l| l.number);
        for line in &mut snippet.lines {
            line.marks.sort_by_key(|m| (m.start, m.severity)); // a hint last
            line.marks = merge_same_span(std::mem::take(&mut line.marks));
        }
    }
    if snippets.len() > 1 {
        snippets[1..].sort_by(|a, b| a.file.path.cmp(&b.file.path));
    }
    snippets
}
/// The rows that draw the marks `chosen` picks, the row next to the source
/// line first. That row has every mark: in full, or only the word it starts
/// with where it covers a shorter mark. The mark nothing follows on that row
/// keeps its message there; every other mark with a message, and every mark
/// not drawn in full, drops: a `|` under its start, then the mark in full
/// with its message on a row of its own. The rightmost drops first, so no `|`
/// crosses a message.
pub(crate) fn mark_rows<'a>(
    text: &str,
    marks: &'a [Mark],
    cells: &[Range<usize>],
    chosen: impl Fn(usize) -> bool,
) -> Vec<Vec<Stroke<'a>>> {
    let mut shortest_first: Vec<usize> = (0..marks.len()).filter(|&index| chosen(index)).collect();
    shortest_first.sort_by_key(|&index| cells[index].len());
    let mut drawn: Vec<(usize, Range<usize>)> = Vec::new();
    let mut in_full = vec![false; marks.len()];
    for &index in &shortest_first {
        let free = |range: &Range<usize>| {
            drawn
                .iter()
                .all(|(_, taken)| range.end <= taken.start || taken.end <= range.start)
        };
        if free(&cells[index]) {
            in_full[index] = true;
            drawn.push((index, cells[index].clone()));
            continue;
        }
        let start = marks[index].start;
        let word = text
            .chars()
            .skip(start - 1)
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .count()
            .max(1);
        let cell = cells[index].start;
        let head = cell..cell_of(text, start + word).clamp(cell + 1, cells[index].end);
        if free(&head) {
            drawn.push((index, head));
        }
    }
    let stays = drawn
        .iter()
        .find(|(index, range)| {
            in_full[*index]
                && !marks[*index].messages.is_empty()
                && drawn.iter().all(|(_, other)| other.start < range.end)
        })
        .map(|&(index, _)| index);
    let mut dropping: Vec<usize> = shortest_first
        .iter()
        .copied()
        .filter(|&index| {
            Some(index) != stays && (!in_full[index] || !marks[index].messages.is_empty())
        })
        .collect();
    let glyph = |index: usize| match marks[index].severity {
        Severity::Hint => "-",
        _ => "^",
    };
    dropping.sort_by_key(|&index| std::cmp::Reverse((cells[index].start, cells[index].end)));
    // The `|`s of the marks from `dropping[from]` on, left to right.
    let pipes = |from: usize| -> Vec<Stroke<'a>> {
        dropping[from..]
            .iter()
            .rev()
            .map(|&index| Stroke {
                cell: cells[index].start,
                glyph: "|",
                length: 1,
                severity: marks[index].severity,
                message: None,
            })
            .collect()
    };
    // A mark in full with its first message, then a row for each other one.
    let full =
        |rows: &mut Vec<Vec<Stroke<'a>>>, mut row: Vec<Stroke<'a>>, index: usize, from: usize| {
            let mark = &marks[index];
            let range = &cells[index];
            row.push(Stroke {
                cell: range.start,
                glyph: glyph(index),
                length: range.len(),
                severity: mark.severity,
                message: mark.messages.first().copied(),
            });
            rows.push(row);
            for &message in mark.messages.iter().skip(1) {
                let mut row = pipes(from);
                row.push(Stroke {
                    cell: range.end,
                    glyph: "",
                    length: 0,
                    severity: mark.severity,
                    message: Some(message),
                });
                rows.push(row);
            }
        };
    let mut rows = Vec::new();
    let mut first: Vec<Stroke<'a>> = drawn
        .iter()
        .filter(|(index, _)| Some(*index) != stays)
        .map(|(index, range)| Stroke {
            cell: range.start,
            glyph: glyph(*index),
            length: range.len(),
            severity: marks[*index].severity,
            message: None,
        })
        .collect();
    first.sort_by_key(|stroke| stroke.cell);
    match stays {
        Some(index) => full(&mut rows, first, index, 0),
        None if !first.is_empty() => rows.push(first),
        None => {}
    }
    if !dropping.is_empty() {
        rows.push(pipes(0));
    }
    for (position, &index) in dropping.iter().enumerate() {
        full(&mut rows, pipes(position + 1), index, position + 1);
    }
    rows
}
fn merge_same_span(marks: Vec<Mark>) -> Vec<Mark> {
    let mut merged: Vec<Mark> = Vec::with_capacity(marks.len());
    for mark in marks {
        match merged
            .iter_mut()
            .find(|kept| kept.start == mark.start && kept.end == mark.end)
        {
            Some(kept) => {
                kept.severity = kept.severity.min(mark.severity); // the most severe
                kept.messages.extend(mark.messages);
            }
            None => merged.push(mark),
        }
    }
    merged
}
fn clamp(file: &SourceFile, span: Span) -> Span {
    let start = (span.start as usize).min(file.text.len());
    Span::new(start, (span.end as usize).clamp(start, file.text.len()))
}
pub(crate) fn line_number_width(d: &Diagnostic, snippets: &[Snippet]) -> usize {
    let lines = snippets
        .iter()
        .flat_map(|s| s.lines.iter().map(|l| l.number));
    let fixes = d
        .notes
        .iter()
        .flat_map(|n| n.fixes())
        .map(|f| f.file.line(clamp(&f.file, f.span).start as usize));
    let largest = lines.chain(fixes).max().unwrap_or(1).max(1);
    largest.ilog10() as usize + 1
}
