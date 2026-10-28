//! Writes diagnostics for a terminal: the header, each marked source line with
//! the line above it, the notes, and the lines a fix changes as they read once
//! it is applied. In color, source lines are highlighted from their tokens.

mod layout;

use std::collections::HashMap;
use std::fmt::Write as _;
use std::io;
use std::io::Write;
use std::ops::Range;
use std::sync::Arc;

use source::SourceFile;
use token::Token;
use token::TokenKind;
use unicode_general_category::GeneralCategory;
use unicode_general_category::get_general_category;

use crate::Diagnostic;
use crate::NoteKind;
use crate::Severity;
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
use crate::fix::Fix;
use crate::printer::layout::line_number_width;
use crate::printer::layout::mark_rows;
use crate::printer::layout::snippets;

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
            Severity::Info | Severity::Hint => {}
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
    severity: Severity, // its label's
    messages: Vec<&'a str>,
}

/// A run of one character on a row of marks, at terminal cell `cell`, and
/// the message written after it, if any.
struct Stroke<'a> {
    cell: usize,
    glyph: &'static str,
    length: usize,
    severity: Severity, // its mark's
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
            _ = write!(self.out, "{color}{text}{RESET}");
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
        let header = match d.code.severity {
            Severity::Error => BOLD_RED,
            Severity::Warning => BOLD_YELLOW,
            Severity::Info | Severity::Hint => BOLD_CYAN,
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
                self.write_marked_line(snippet.file, line);
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
    fn write_marked_line(&mut self, file: &Arc<SourceFile>, line: &MarkedLine) {
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
            self.write_strokes(row);
        }
        self.write_gutter(WHITE, line.number);
        self.write_source_line(file, line.number);
        self.out.push('\n');
        for row in &mark_rows(text, &line.marks, &cells, |index| !above[index]) {
            self.write_strokes(row);
        }
    }

    /// Writes one row of marks, `|`s and messages, left to right.
    fn write_strokes(&mut self, row: &[Stroke]) {
        self.write_blank_gutter();
        let mut cell = 0;
        for stroke in row {
            if stroke.cell < cell {
                continue; // a `|` under a mark that starts at the same cell
            }
            // a column can pass the 65,535 that a format width allows
            self.out
                .extend(std::iter::repeat_n(' ', stroke.cell - cell));
            let color = match stroke.severity {
                Severity::Error => RED,
                Severity::Warning => YELLOW,
                Severity::Info | Severity::Hint => BLUE,
            };
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

fn line_text(file: &SourceFile, number: usize) -> &str {
    file.slice(file.line_span(number))
}

/// Whether to show the line above `number`, so the marked line reads in its
/// setting: it is not blank and not already shown.
fn has_context_line(file: &SourceFile, number: usize, last_shown: usize) -> bool {
    number > 1 && number - 1 > last_shown && !line_text(file, number - 1).trim().is_empty()
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
mod tests;
