//! Writes diagnostics for a terminal: the header, each marked source line with
//! the line above it, the notes, and the lines a fix changes as they read once
//! it is applied. In color, source lines are highlighted from their tokens.

use std::collections::HashMap;
use std::fmt::Write as _;
use std::io;
use std::io::Write;
use std::ops::Range;
use std::sync::Arc;

use crate::colors::BLUE;
use crate::colors::BOLD_CYAN;
use crate::colors::BOLD_RED;
use crate::colors::BOLD_YELLOW;
use crate::colors::CYAN;
use crate::colors::FIX_ADDED;
use crate::colors::GREEN;
use crate::colors::GREY;
use crate::colors::LIGHT_GREEN;
use crate::colors::LIGHT_ORANGE;
use crate::colors::ORANGE;
use crate::colors::PURPLE;
use crate::colors::RED;
use crate::colors::RESET;
use crate::colors::WHITE;
use crate::colors::YELLOW;
use source::SourceFile;
use source::Span;
use token::Token;
use token::TokenKind;

use crate::Diagnostic;
use crate::NoteKind;
use crate::Severity;
use crate::fix::Fix;
use unicode_general_category::GeneralCategory;
use unicode_general_category::get_general_category;

const TAB_WIDTH: usize = 4;

/// Returns the tokens of a file, to highlight its lines.
pub type TokensOf<'a> = &'a dyn Fn(&Arc<SourceFile>) -> Vec<Token>;

/// Writes `diagnostics`, ordered by the file and position of their primary
/// place (those without a place first), then a summary line. With `colored`,
/// output has terminal colors and source lines are highlighted from the tokens
/// it returns for a file (the lexer's, read once per file). A span outside its
/// file's text is clamped, so printing never fails on a bad span.
pub fn print(
    out: &mut impl Write,
    diagnostics: &[Diagnostic],
    colored: Option<TokensOf>,
) -> io::Result<()> {
    let mut ordered: Vec<&Diagnostic> = diagnostics.iter().collect();
    ordered.sort_by(|a, b| match (a.labels.first(), b.labels.first()) {
        (Some(a), Some(b)) => (&a.file.path, a.span.start).cmp(&(&b.file.path, b.span.start)),
        (a, b) => a.is_some().cmp(&b.is_some()),
    });
    let mut p = Printer {
        out: String::new(),
        colored,
        width: 0,
        tokens: HashMap::new(),
    };
    let (mut errors, mut warnings) = (0, 0);
    for d in ordered {
        p.write_diagnostic(d);
        match d.code.severity {
            Severity::Error => errors += 1,
            Severity::Warning => warnings += 1,
            Severity::Info => {}
        }
    }
    match (errors, warnings) {
        (0, 0) => return out.write_all(p.out.as_bytes()),
        (0, _) => p.paint(
            ORANGE,
            format_args!("Compilation succeeded with {warnings} warning(s)"),
        ),
        (_, 0) => p.paint(
            RED,
            format_args!("Compilation failed with {errors} error(s)"),
        ),
        _ => p.paint(
            RED,
            format_args!("Compilation failed with {errors} error(s) and {warnings} warning(s)"),
        ),
    }
    p.out.push('\n');
    out.write_all(p.out.as_bytes())
}

struct Printer<'a> {
    out: String,
    colored: Option<TokensOf<'a>>,
    width: usize, // digits of the largest line number shown
    tokens: HashMap<*const SourceFile, Vec<Token>>, // each shown file's tokens, read once, in color only
}

/// The part of one source line a label covers, in 1-based character columns;
/// `end` is 0 when the label runs on past the end of the line.
struct Mark<'a> {
    start: usize,
    end: usize,
    primary: bool,
    messages: Vec<&'a str>,
}

/// A run of one character on a row of marks, at terminal cell `cell`, and
/// the message written after it, if any.
struct Stroke<'a> {
    cell: usize,
    glyph: &'static str,
    length: usize,
    primary: bool,
    message: Option<&'a str>,
}

struct MarkedLine<'a> {
    number: usize,
    marks: Vec<Mark<'a>>,
}

/// The marked lines of one file and the place its header names.
struct Snippet<'a> {
    file: &'a Arc<SourceFile>,
    line: usize,
    column: usize,
    lines: Vec<MarkedLine<'a>>,
}

impl Printer<'_> {
    fn paint(&mut self, color: &str, text: impl std::fmt::Display) {
        if self.colored.is_some() {
            _ = write!(self.out, "{color}{text}{}", RESET);
        } else {
            _ = write!(self.out, "{text}");
        }
    }

    fn write_gutter(&mut self, color: &str, number: usize) {
        let width = self.width;
        self.paint(color, format_args!("{number:>width$} | "));
    }

    fn write_blank_gutter(&mut self) {
        let width = self.width;
        self.paint(GREY, format_args!("{:>width$} | ", ""));
    }

    fn write_margin(&mut self) {
        self.write_blank_gutter();
        self.out.push('\n');
    }

    fn write_diagnostic(&mut self, d: &Diagnostic) {
        let snippets = snippets(d);
        self.width = line_number_width(d, &snippets);
        let (header, mark_color) = match d.code.severity {
            Severity::Error => (BOLD_RED, RED),
            Severity::Warning => (BOLD_YELLOW, YELLOW),
            Severity::Info => (BOLD_CYAN, BLUE),
        };
        self.paint(header, format_args!("[{}]", d.code.id));
        self.out.push_str(": ");
        self.paint(header, &d.message);
        self.out.push('\n');

        for (index, snippet) in snippets.iter().enumerate() {
            if index > 0 {
                self.out.push('\n');
            }
            let pad = self.width + 1;
            self.paint(
                BLUE,
                format_args!(
                    "{:pad$}--> {}:{}:{}",
                    "", snippet.file.path, snippet.line, snippet.column
                ),
            );
            self.out.push('\n');
            self.write_margin();
            let mut last_shown = 0;
            for line in &snippet.lines {
                let context = has_context_line(snippet.file, line.number, last_shown);
                let first_shown = line.number - usize::from(context);
                if last_shown > 0 && first_shown > last_shown + 1 {
                    let width = self.width;
                    self.paint(GREY, format_args!("{:>width$}", "..."));
                    self.out.push('\n');
                }
                if context {
                    self.write_gutter(GREY, line.number - 1);
                    self.write_source_line(snippet.file, line.number - 1);
                    self.out.push('\n');
                }
                self.write_marked_line(snippet.file, line, mark_color);
                last_shown = line.number;
            }
        }
        if !snippets.is_empty() && !d.notes.is_empty() {
            self.write_margin();
        }

        let mut after_fixed_lines = false;
        for note in &d.notes {
            if after_fixed_lines {
                self.write_margin();
            }
            let (kind, color) = match note.kind {
                NoteKind::Note => ("note", CYAN),
                _ if note.has_sure_fix() => ("fix", GREEN),
                NoteKind::Help(_) | NoteKind::Guess(_) => ("help", GREEN),
            };
            let pad = self.width + 1;
            _ = write!(self.out, "{:pad$}", "");
            self.paint(color, format_args!("= {kind}: "));
            _ = writeln!(self.out, "{}", note.text);
            after_fixed_lines = self.write_fixed_lines(note.fixes());
        }
        self.out.push('\n');
    }

    /// Writes the line with its marks: a mark inside a longer one goes above
    /// the line and the rest below, so every mark shows in full
    /// (`mark_rows`).
    fn write_marked_line(&mut self, file: &Arc<SourceFile>, line: &MarkedLine, mark_color: &str) {
        let text = line_text(file, line.number);
        let cells: Vec<Range<usize>> = line
            .marks
            .iter()
            .map(|mark| {
                let end = if mark.end == 0 {
                    text.chars().count() + 1
                } else {
                    mark.end
                };
                let start = cell_of(text, mark.start);
                start..cell_of(text, end).max(start + 1)
            })
            .collect();
        // Above: holds no other mark but lies on a longer one (on an equal
        // one written before it, for marks that cross).
        let above: Vec<bool> = cells
            .iter()
            .enumerate()
            .map(|(index, mark)| {
                let others = || {
                    cells
                        .iter()
                        .enumerate()
                        .filter(move |&(other, _)| other != index)
                };
                let holds =
                    others().any(|(_, other)| mark.start <= other.start && other.end <= mark.end);
                let lies_on = others().any(|(other_index, other)| {
                    mark.start < other.end
                        && other.start < mark.end
                        && (other.len(), index) > (mark.len(), other_index)
                });
                !holds && lies_on
            })
            .collect();
        let mut rows = mark_rows(text, &line.marks, &cells, |index| above[index]);
        rows.reverse();
        for row in &rows {
            self.write_strokes(row, mark_color);
        }
        self.write_gutter(WHITE, line.number);
        self.write_source_line(file, line.number);
        self.out.push('\n');
        for row in &mark_rows(text, &line.marks, &cells, |index| !above[index]) {
            self.write_strokes(row, mark_color);
        }
    }

    /// Writes one row of marks, `|`s and messages, left to right.
    fn write_strokes(&mut self, row: &[Stroke], mark_color: &str) {
        self.write_blank_gutter();
        let mut cell = 0;
        for stroke in row {
            if stroke.cell < cell {
                continue; // a `|` under a mark that starts at the same cell
            }
            // a column can pass the 65,535 that a format width allows
            self.out
                .extend(std::iter::repeat_n(' ', stroke.cell - cell));
            let color = if stroke.primary { mark_color } else { BLUE };
            if stroke.length > 0 {
                self.paint(color, stroke.glyph.repeat(stroke.length));
            }
            if let Some(message) = stroke.message {
                self.paint(color, format_args!(" {message}"));
            }
            cell = stroke.cell + stroke.length;
        }
        self.out.push('\n');
    }

    /// Writes line `number` of `file`, highlighted in color.
    fn write_source_line(&mut self, file: &Arc<SourceFile>, number: usize) {
        let line = file.line_span(number);
        let Some(scan) = self.colored else {
            self.write_cells(file.slice(line), 0, None);
            return;
        };
        let tokens = self
            .tokens
            .entry(Arc::as_ptr(file))
            .or_insert_with(|| scan(file));
        // The line in pieces with their highlight color. Text between tokens
        // that is not white space is a comment (or a character the lexer
        // rejected, which a mark shows anyway).
        let line = line.range();
        let mut pieces = Vec::new();
        let push_gap = |pieces: &mut Vec<_>, gap: Range<usize>| {
            let text = &file.text[gap.clone()];
            let comment = gap.start + (text.len() - text.trim_start().len());
            let comment_end = comment + text.trim().len();
            for (range, color) in [
                (gap.start..comment, None),
                (comment..comment_end, Some(GREY)),
                (comment_end..gap.end, None),
            ] {
                if !range.is_empty() {
                    pieces.push((range, color));
                }
            }
        };
        let mut position = line.start;
        let first = tokens.partition_point(|t| (t.span.end as usize) <= line.start);
        for t in &tokens[first..] {
            let span = t.span.range();
            if span.start >= line.end || t.kind == TokenKind::Eof {
                break;
            }
            let piece = span.start.max(line.start)..span.end.min(line.end);
            push_gap(&mut pieces, position..piece.start);
            position = piece.end;
            let color = match t.kind {
                TokenKind::String | TokenKind::CString => Some(LIGHT_GREEN),
                TokenKind::Number | TokenKind::Char | TokenKind::ByteChar => Some(LIGHT_ORANGE),
                TokenKind::DocComment => Some(GREY),
                kind if token::KEYWORDS.contains(&kind) => Some(PURPLE),
                _ => None,
            };
            pieces.push((piece, color));
        }
        push_gap(&mut pieces, position..line.end);
        let mut column = 0;
        for (range, color) in pieces {
            column = self.write_cells(&file.text[range], column, color);
        }
    }

    /// Shows every line the fixes change, as it reads once they are applied,
    /// with the added text marked. It shows nothing unless every fix is
    /// drawable: inside its file, on one line, without a line break in its text
    /// and not overlapping another. Returns whether it drew anything.
    fn write_fixed_lines(&mut self, fixes: &[Fix]) -> bool {
        let mut lines: Vec<(&Arc<SourceFile>, usize, Vec<&Fix>)> = Vec::new();
        for fix in fixes {
            let (start, end) = (fix.span.start as usize, fix.span.end as usize);
            if end < start
                || !fix.file.text.is_char_boundary(start)
                || !fix.file.text.is_char_boundary(end)
                || fix.new_text.contains(['\r', '\n'])
                || fix.file.line(start) != fix.file.line(end)
            {
                return false;
            }
            let number = fix.file.line(start);
            match lines
                .iter_mut()
                .find(|(file, n, _)| Arc::ptr_eq(file, &fix.file) && *n == number)
            {
                Some((_, _, fixes)) => fixes.push(fix),
                None => lines.push((&fix.file, number, vec![fix])),
            }
        }
        for (_, _, fixes) in &mut lines {
            fixes.sort_by_key(|f| (f.span.start, f.span.end));
            if fixes.windows(2).any(|w| w[1].span.start < w[0].span.end) {
                return false;
            }
        }
        if lines.is_empty() {
            return false;
        }
        self.write_margin();
        for (file, number, fixes) in lines {
            self.write_gutter(GREY, number);
            let span = file.line_span(number);
            let (mut cells, mut position) = (0, span.start as usize);
            for fix in fixes {
                let range = fix.span.range();
                cells = self.write_cells(&file.text[position..range.start], cells, None);
                let (kept, added, kept_after) =
                    split_change(&file.text[range.clone()], &fix.new_text);
                cells = self.write_cells(kept, cells, None);
                cells = self.write_cells(added, cells, Some(FIX_ADDED));
                cells = self.write_cells(kept_after, cells, None);
                position = range.end;
            }
            self.write_cells(&file.text[position..span.end as usize], cells, None);
            self.out.push('\n');
        }
        true
    }

    /// Writes `text` starting at terminal cell `column`, expanding tabs, in
    /// `color` when one is given, and returns the column after it.
    fn write_cells(&mut self, text: &str, mut column: usize, color: Option<&str>) -> usize {
        let mut expanded = String::with_capacity(text.len());
        for c in text.chars() {
            let width = cell_width(c, column);
            if c == '\t' {
                expanded.extend(std::iter::repeat_n(' ', width));
            } else {
                expanded.push(c);
            }
            column += width;
        }
        match color {
            Some(color) if !expanded.is_empty() => self.paint(color, expanded),
            _ => self.out.push_str(&expanded),
        }
        column
    }
}

/// Arranges the labels of `d` by file: the primary label's file first, then the
/// others by name; lines in order; on a line, marks from left to right with the
/// primary one first. Labels on exactly the same span are drawn once.
fn snippets(d: &Diagnostic) -> Vec<Snippet<'_>> {
    let mut snippets: Vec<Snippet> = Vec::new();
    for (index, label) in d.labels.iter().enumerate() {
        let at = match snippets
            .iter()
            .position(|s| Arc::ptr_eq(s.file, &label.file))
        {
            Some(at) => at,
            None => {
                snippets.push(Snippet {
                    file: &label.file,
                    line: 0,
                    column: 0,
                    lines: Vec::new(),
                });
                snippets.len() - 1
            }
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
                primary: index == 0,
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
            line.marks.sort_by_key(|m| (m.start, !m.primary));
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
fn mark_rows<'a>(
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
    let glyph = |index: usize| if marks[index].primary { "^" } else { "-" };
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
                primary: marks[index].primary,
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
                primary: mark.primary,
                message: mark.messages.first().copied(),
            });
            rows.push(row);
            for &message in mark.messages.iter().skip(1) {
                let mut row = pipes(from);
                row.push(Stroke {
                    cell: range.end,
                    glyph: "",
                    length: 0,
                    primary: mark.primary,
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
            primary: marks[*index].primary,
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
                kept.primary |= mark.primary;
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

fn line_text(file: &SourceFile, number: usize) -> &str {
    file.slice(file.line_span(number))
}

/// Whether to show the line above `number`, so the marked line reads in its
/// setting: it is not blank and not already shown.
fn has_context_line(file: &SourceFile, number: usize, last_shown: usize) -> bool {
    number > 1 && number - 1 > last_shown && !line_text(file, number - 1).trim().is_empty()
}

fn line_number_width(d: &Diagnostic, snippets: &[Snippet]) -> usize {
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

/// Splits a replacement into the part both texts start with, the new text in
/// between, and the part both end with; only the middle is marked.
fn split_change<'a>(old: &str, new: &'a str) -> (&'a str, &'a str, &'a str) {
    let prefix: usize = old
        .chars()
        .zip(new.chars())
        .take_while(|(a, b)| a == b)
        .map(|(_, b)| b.len_utf8())
        .sum();
    let suffix: usize = old[prefix.min(old.len())..]
        .chars()
        .rev()
        .zip(new[prefix..].chars().rev())
        .take_while(|(a, b)| a == b)
        .map(|(_, b)| b.len_utf8())
        .sum();
    let added_end = new.len() - suffix;
    (&new[..prefix], &new[prefix..added_end], &new[added_end..])
}

/// The terminal cell where the 1-based character `column` of `text` is drawn;
/// a column past the end counts one cell per column.
fn cell_of(text: &str, column: usize) -> usize {
    let (mut cell, mut current) = (0, 1);
    for c in text.chars() {
        if current >= column {
            return cell;
        }
        cell += cell_width(c, cell);
        current += 1;
    }
    cell + column.saturating_sub(current)
}

/// How many terminal cells `c` takes at cell `column`: a tab runs to the next
/// tab stop, a combining mark or format character takes none, and wide East
/// Asian characters and emoji take two.
fn cell_width(c: char, column: usize) -> usize {
    if c == '\t' {
        return TAB_WIDTH - column % TAB_WIDTH;
    }
    if matches!(
        get_general_category(c),
        GeneralCategory::NonspacingMark | GeneralCategory::EnclosingMark | GeneralCategory::Format
    ) {
        return 0;
    }
    // shortcut: block ranges of the Han, Hiragana, Katakana and Hangul scripts
    // instead of script tables, as wide as Go's printer; use a width table if
    // other wide scripts show up misaligned.
    match c as u32 {
        0x1100..=0x11FF
        | 0x2E80..=0x2FDF
        | 0x3000..=0x30FF
        | 0x3130..=0x318F
        | 0x31F0..=0x31FF
        | 0x3400..=0x4DBF
        | 0x4E00..=0x9FFF
        | 0xA960..=0xA97F
        | 0xAC00..=0xD7FF
        | 0xF900..=0xFAFF
        | 0xFF01..=0xFF60
        | 0xFFE0..=0xFFE6
        | 0x1F300..=0x1FAFF
        | 0x20000..=0x3FFFF => 2,
        _ => 1,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Code;
    use token::Token;

    fn span(file: &SourceFile, text: &str) -> Span {
        let start = file.text.find(text).unwrap();
        Span::new(start, start + text.len())
    }

    fn render(diagnostics: &[Diagnostic]) -> String {
        let mut out = Vec::new();
        print(&mut out, diagnostics, None).unwrap();
        String::from_utf8(out).unwrap()
    }

    fn no_tokens(_: &Arc<SourceFile>) -> Vec<Token> {
        Vec::new()
    }

    fn code(id: &'static str, severity: Severity) -> Code {
        Code { id, severity }
    }

    #[test]
    fn two_files() {
        let main = Arc::new(SourceFile::new(
            "src/main.peep",
            "fn main() -> i32 {\n    global::exit(0);\n}\n",
        ));
        let global = Arc::new(SourceFile::new(
            "lib/global.peep",
            "#[extern(\"exit\")]\nfn exit(code: i32);\n",
        ));
        let d = Diagnostic::new(
            code("M0004", Severity::Error),
            "`exit` is not exported from `global`",
        )
        .label(
            &main,
            span(&main, "global::exit"),
            "use of unexported symbol",
        )
        .label(
            &global,
            {
                let s = span(&global, "exit(");
                Span::new(s.start as usize, s.end as usize - 1)
            },
            "defined here",
        )
        .note("symbols with uppercase are exported otherwise private");
        let want = "[M0004]: `exit` is not exported from `global`
  --> src/main.peep:2:5
  | 
1 | fn main() -> i32 {
2 |     global::exit(0);
  |     ^^^^^^^^^^^^ use of unexported symbol

  --> lib/global.peep:2:4
  | 
1 | #[extern(\"exit\")]
2 | fn exit(code: i32);
  |    ---- defined here
  | 
  = note: symbols with uppercase are exported otherwise private

Compilation failed with 1 error(s)
";
        assert_eq!(render(&[d]), want);
    }

    /// A sure fix is labeled `fix`; a guess and plain advice stay `help`.
    #[test]
    fn fix_and_help_labels() {
        let file = Arc::new(SourceFile::new("a.peep", "cout\n"));
        let at = span(&file, "cout");
        let fix = Fix {
            file: file.clone(),
            span: at,
            new_text: "count".into(),
        };
        let d = Diagnostic::new(code("T0002", Severity::Error), "unknown name `cout`")
            .label(&file, at, "")
            .guess("did you mean `count`?", vec![fix])
            .help("declare it first", Vec::new());
        let got = render(&[d]);
        assert!(got.contains("= help: did you mean `count`?"), "{got}");
        assert!(got.contains("= help: declare it first"), "{got}");
        assert!(!got.contains("= fix:"), "{got}");
    }

    #[test]
    fn fix_with_preview() {
        let file = Arc::new(SourceFile::new(
            "a.peep",
            "struct S {\n    value: i32,\n}\n",
        ));
        let comma = span(&file, ",");
        let fix = Fix {
            file: file.clone(),
            span: comma,
            new_text: String::new(),
        };
        let d = Diagnostic::new(
            code("S0001", Severity::Info),
            "trailing comma is unnecessary",
        )
        .label(&file, comma, "")
        .help("remove this comma", vec![fix]);
        let want = "[S0001]: trailing comma is unnecessary
  --> a.peep:2:15
  | 
1 | struct S {
2 |     value: i32,
  |               ^
  | 
  = fix: remove this comma
  | 
2 |     value: i32

";
        assert_eq!(render(&[d]), want);
    }

    #[test]
    fn inserted_text_is_marked() {
        let file = Arc::new(SourceFile::new("a.peep", "let a = 1\n"));
        let at = Span::at(9);
        let fix = Fix {
            file: file.clone(),
            span: at,
            new_text: ";".into(),
        };
        let d = Diagnostic::new(code("P0002", Severity::Error), "expected `;`")
            .label(&file, at, "")
            .help("add missing `;`", vec![fix]);
        let mut out = Vec::new();
        print(&mut out, &[d], Some(&no_tokens)).unwrap();
        let out = String::from_utf8(out).unwrap();
        assert!(
            out.contains("let a = 1\x1b[38;5;22;48;5;114m;\x1b[0m\n"),
            "{out:?}"
        );
    }

    #[test]
    fn order_and_summary() {
        let file = Arc::new(SourceFile::new("a.peep", "a\nb\n"));
        let late = Diagnostic::new(code("W0001", Severity::Warning), "late").label(
            &file,
            span(&file, "b"),
            "",
        );
        let early = Diagnostic::new(code("L0001", Severity::Error), "early").label(
            &file,
            span(&file, "a"),
            "",
        );
        let nowhere = Diagnostic::new(code("M0001", Severity::Error), "no place");
        let got = render(&[late, early, nowhere]);
        let mut last = 0;
        for part in [
            "[M0001]",
            "[L0001]",
            "[W0001]",
            "Compilation failed with 2 error(s) and 1 warning(s)",
        ] {
            let at = got
                .find(part)
                .unwrap_or_else(|| panic!("{part} missing in:\n{got}"));
            assert!(at >= last, "{part} out of order in:\n{got}");
            last = at;
        }
    }

    #[test]
    fn span_over_lines_tabs_and_wide_characters() {
        let file = Arc::new(SourceFile::new("a.peep", "\tlet 名 = (\r\n  1)\r\n"));
        let start = file.text.find('名').unwrap();
        let end = file.text.find(')').unwrap() + 1;
        let d = Diagnostic::new(code("P0003", Severity::Error), "bad").label(
            &file,
            Span::new(start, end),
            "here",
        );
        let want = "[P0003]: bad
  --> a.peep:1:6
  | 
1 |     let 名 = (
  |         ^^^^^^
2 |   1)
  | ^^^^ here

Compilation failed with 1 error(s)
";
        assert_eq!(render(&[d]), want);
    }

    /// A message that would run into the next mark drops below it, the
    /// rightmost first, so no `|` crosses a message.
    #[test]
    fn marks_side_by_side() {
        let file = Arc::new(SourceFile::new(
            "a.peep",
            "let n = add(width, height) + scale(depth);\n",
        ));
        let d = Diagnostic::new(code("T0001", Severity::Error), "mismatched types")
            .label(&file, span(&file, "depth"), "expected `i32`, found `bool`")
            .label(&file, span(&file, "width"), "`width` is `i32`")
            .label(&file, span(&file, "height"), "`height` is `i64`");
        let want = "[T0001]: mismatched types
  --> a.peep:1:36
  |\x20
1 | let n = add(width, height) + scale(depth);
  |             -----  ------          ^^^^^ expected `i32`, found `bool`
  |             |      |
  |             |      ------ `height` is `i64`
  |             ----- `width` is `i32`

Compilation failed with 1 error(s)
";
        assert_eq!(render(&[d]), want);
    }

    /// A mark inside a longer one goes above the line; the longer one shows
    /// the word it starts with, then drops to show in full.
    #[test]
    fn marks_inside_marks() {
        let file = Arc::new(SourceFile::new(
            "a.peep",
            "return wrap(P.{ x = f(1), y = g(2) });\n",
        ));
        let d = Diagnostic::new(code("T0001", Severity::Error), "mismatched types")
            .label(
                &file,
                span(&file, "wrap(P.{ x = f(1), y = g(2) })"),
                "`wrap` takes `Q`",
            )
            .label(
                &file,
                span(&file, "P.{ x = f(1), y = g(2) }"),
                "`P` literal",
            )
            .label(&file, span(&file, "f(1)"), "`f` returns `bool`")
            .label(&file, span(&file, "g(2)"), "`g` returns `u8`");
        let want = "[T0001]: mismatched types
  --> a.peep:1:8
  |\x20
  |                     ---- `f` returns `bool`
  |                     |
  |                     ----      ---- `g` returns `u8`
1 | return wrap(P.{ x = f(1), y = g(2) });
  |        ^^^^ ------------------------ `P` literal
  |        |
  |        ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ `wrap` takes `Q`

Compilation failed with 1 error(s)
";
        assert_eq!(render(&[d]), want);
    }

    #[test]
    fn never_fails_on_bad_spans() {
        let file = Arc::new(SourceFile::new("a.peep", "x\n"));
        let fix = Fix {
            file: file.clone(),
            span: Span::new(1, 99),
            new_text: "y".into(),
        };
        let d = Diagnostic::new(code("L0001", Severity::Error), "outside")
            .label(&file, Span::new(40, 2), "")
            .help("broken fix is not drawn", vec![fix]);
        let got = render(&[d]);
        assert!(
            got.contains("[L0001]: outside") && got.contains("= fix: broken fix is not drawn"),
            "{got}"
        );
    }

    #[test]
    fn marks_far_into_a_long_line() {
        let text = format!("{}x\n", " ".repeat(70_000));
        let file = Arc::new(SourceFile::new("a.peep", text));
        let d = Diagnostic::new(code("L0001", Severity::Error), "far")
            .label(&file, Span::new(70_000, 70_001), "a")
            .label(&file, Span::new(70_000, 70_001), "b");
        assert!(render(&[d]).contains("^ a\n"));
    }

    #[test]
    fn color() {
        let file = Arc::new(SourceFile::new("a.peep", "x\n"));
        let d = Diagnostic::new(code("W0001", Severity::Warning), "w").label(
            &file,
            span(&file, "x"),
            "",
        );
        let mut out = Vec::new();
        print(&mut out, &[d], Some(&no_tokens)).unwrap();
        let out = String::from_utf8(out).unwrap();
        assert!(out.contains("\x1b[1;33m[W0001]\x1b[0m"), "{out:?}");
    }

    #[test]
    fn source_lines_are_highlighted_in_color() {
        let text = "fn main() {\n    let s = \"x\" + 1; // note\n}\n";
        let file = Arc::new(SourceFile::new("a.peep", text));
        let d = Diagnostic::new(code("P0003", Severity::Error), "bad").label(
            &file,
            span(&file, "+"),
            "",
        );
        // The lexer's tokens of `text`, comment left out as the lexer leaves it.
        let tokens = |file: &Arc<SourceFile>| {
            use token::TokenKind;
            let mut at = 0;
            let parts = [
                ("fn", TokenKind::Fn),
                ("main", TokenKind::Ident),
                ("(", TokenKind::LeftParen),
                (")", TokenKind::RightParen),
                ("{", TokenKind::LeftBrace),
                ("let", TokenKind::Let),
                ("s", TokenKind::Ident),
                ("=", TokenKind::Assign),
                ("\"x\"", TokenKind::String),
                ("+", TokenKind::Plus),
                ("1", TokenKind::Number),
                (";", TokenKind::Semicolon),
                ("}", TokenKind::RightBrace),
            ];
            let mut tokens: Vec<Token> = parts
                .iter()
                .map(|&(part, kind)| {
                    let start = at + file.text[at..].find(part).unwrap();
                    at = start + part.len();
                    Token {
                        kind,
                        span: Span::new(start, at),
                    }
                })
                .collect();
            tokens.push(Token {
                kind: TokenKind::Eof,
                span: Span::at(file.text.len()),
            });
            tokens
        };
        let mut out = Vec::new();
        print(&mut out, &[d], Some(&tokens)).unwrap();
        let out = String::from_utf8(out).unwrap();
        let line = "    \x1b[35mlet\x1b[0m s = \x1b[38;5;120m\"x\"\x1b[0m + \x1b[38;5;215m1\x1b[0m; \x1b[90m// note\x1b[0m\n";
        assert!(out.contains(line), "{out:?}");
        assert!(
            out.contains("\x1b[35mfn\x1b[0m main() {\n"),
            "context line: {out:?}"
        );
        assert!(
            !render(&[
                Diagnostic::new(code("P0003", Severity::Error), "bad").label(
                    &file,
                    span(&file, "+"),
                    ""
                )
            ])
            .contains('\x1b')
        );
    }
}
