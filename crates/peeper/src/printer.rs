//! Writes diagnostics for a terminal: the header, each marked source line with
//! the line above it, the notes, and the lines a fix changes as they read once
//! it is applied. In color, source lines are highlighted from the lexer's
//! tokens.

use std::collections::HashMap;
use std::fmt::Write as _;
use std::io::{self, Write};
use std::ops::Range;
use std::sync::Arc;

use diagnostics::{Diagnostic, Fix, Severity};
use lexer::token::{self, Token, TokenKind};
use source::{SourceFile, Span};
use unicode_general_category::{GeneralCategory, get_general_category};

const TAB_WIDTH: usize = 4;

const RESET: &str = "\x1b[0m";
const RED: &str = "\x1b[31m";
const GREEN: &str = "\x1b[32m";
const YELLOW: &str = "\x1b[33m";
const BLUE: &str = "\x1b[34m";
const CYAN: &str = "\x1b[36m";
const WHITE: &str = "\x1b[37m";
const GREY: &str = "\x1b[90m";
const BOLD_RED: &str = "\x1b[1;31m";
const BOLD_YELLOW: &str = "\x1b[1;33m";
const BOLD_CYAN: &str = "\x1b[1;36m";
const ORANGE: &str = "\x1b[38;5;208m";
const PURPLE: &str = "\x1b[35m"; // keywords
const LIGHT_GREEN: &str = "\x1b[38;5;120m"; // strings
const LIGHT_ORANGE: &str = "\x1b[38;5;215m"; // numbers and characters
// A dark letter on a mid-tone block of the same hue, readable on both dark
// and light terminals.
const FIX_ADDED: &str = "\x1b[38;5;22;48;5;114m";

/// Writes `diagnostics`, ordered by the file and position of their primary
/// place (those without a place first), then a summary line. `colored` adds
/// terminal colors. A span outside its file's text is clamped, so printing
/// never fails on a bad span.
pub fn print(out: &mut impl Write, diagnostics: &[Diagnostic], colored: bool) -> io::Result<()> {
    let mut ordered: Vec<&Diagnostic> = diagnostics.iter().collect();
    ordered.sort_by(|a, b| match (a.labels.first(), b.labels.first()) {
        (Some(a), Some(b)) => (&a.file.name, a.span.start).cmp(&(&b.file.name, b.span.start)),
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
        p.diagnostic(d);
        match d.code.severity {
            Severity::Error => errors += 1,
            Severity::Warning => warnings += 1,
            Severity::Info => {}
        }
    }
    if errors > 0 && warnings > 0 {
        p.paint(
            RED,
            format_args!("Compilation failed with {errors} error(s) and {warnings} warning(s)"),
        );
        p.out.push('\n');
    } else if errors > 0 {
        p.paint(
            RED,
            format_args!("Compilation failed with {errors} error(s)"),
        );
        p.out.push('\n');
    } else if warnings > 0 {
        p.paint(
            ORANGE,
            format_args!("Compilation succeeded with {warnings} warning(s)"),
        );
        p.out.push('\n');
    }
    out.write_all(p.out.as_bytes())
}

struct Printer {
    out: String,
    colored: bool,
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

impl Printer {
    fn paint(&mut self, color: &str, text: impl std::fmt::Display) {
        if self.colored {
            _ = write!(self.out, "{color}{text}{}", RESET);
        } else {
            _ = write!(self.out, "{text}");
        }
    }

    fn gutter(&mut self, color: &str, number: usize) {
        let width = self.width;
        self.paint(color, format_args!("{number:>width$} | "));
    }

    fn blank_gutter(&mut self) {
        let width = self.width;
        self.paint(GREY, format_args!("{:>width$} | ", ""));
    }

    fn margin(&mut self) {
        self.blank_gutter();
        self.out.push('\n');
    }

    fn diagnostic(&mut self, d: &Diagnostic) {
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
                    "", snippet.file.name, snippet.line, snippet.column
                ),
            );
            self.out.push('\n');
            self.margin();
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
                    self.gutter(GREY, line.number - 1);
                    self.source_line(snippet.file, line.number - 1);
                    self.out.push('\n');
                }
                self.marked_line(snippet.file, line, mark_color);
                last_shown = line.number;
            }
        }
        if !snippets.is_empty() && !d.notes.is_empty() {
            self.margin();
        }

        let mut after_fixed_lines = false;
        for note in &d.notes {
            if after_fixed_lines {
                self.margin();
            }
            let (kind, color) = if note.help {
                ("help", GREEN)
            } else {
                ("note", CYAN)
            };
            let pad = self.width + 1;
            _ = write!(self.out, "{:pad$}", "");
            self.paint(color, format_args!("= {kind}: "));
            _ = writeln!(self.out, "{}", note.text);
            after_fixed_lines = self.fixed_lines(&note.fixes);
        }
        self.out.push('\n');
    }

    fn marked_line(&mut self, file: &Arc<SourceFile>, line: &MarkedLine, mark_color: &str) {
        let text = line_text(file, line.number);
        self.gutter(WHITE, line.number);
        self.source_line(file, line.number);
        self.out.push('\n');
        for mark in &line.marks {
            let end = if mark.end == 0 {
                text.chars().count() + 1
            } else {
                mark.end
            };
            let padding = cell_of(text, mark.start);
            let length = cell_of(text, end).saturating_sub(padding).max(1);
            let (color, underline) = if mark.primary {
                (mark_color, "^")
            } else {
                (BLUE, "-")
            };
            self.blank_gutter();
            _ = write!(self.out, "{:padding$}", "");
            self.paint(color, underline.repeat(length));
            for (index, message) in mark.messages.iter().enumerate() {
                if index > 0 {
                    self.out.push('\n');
                    self.blank_gutter();
                    _ = write!(self.out, "{:1$}", "", padding + length);
                }
                self.paint(color, format_args!(" {message}"));
            }
            self.out.push('\n');
        }
    }

    /// Writes line `number` of `file`, highlighted in color.
    fn source_line(&mut self, file: &Arc<SourceFile>, number: usize) {
        let line = file.line_span(number);
        if !self.colored {
            self.cells(file.slice(line), 0, None);
            return;
        }
        let tokens = self
            .tokens
            .entry(Arc::as_ptr(file))
            .or_insert_with(|| lexer::scan(file).0);
        let pieces = highlight(file, tokens, line);
        let mut column = 0;
        for (range, color) in pieces {
            column = self.cells(&file.text[range], column, color);
        }
    }

    /// Shows every line the fixes change, as it reads once they are applied,
    /// with the added text marked. It shows nothing unless every fix is
    /// drawable: inside its file, on one line, without a line break in its text
    /// and not overlapping another. Returns whether it drew anything.
    fn fixed_lines(&mut self, fixes: &[Fix]) -> bool {
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
        self.margin();
        for (file, number, fixes) in lines {
            self.gutter(GREY, number);
            let span = file.line_span(number);
            let (mut cells, mut position) = (0, span.start as usize);
            for fix in fixes {
                let range = fix.span.range();
                cells = self.cells(&file.text[position..range.start], cells, None);
                let (kept, added, kept_after) =
                    split_change(&file.text[range.clone()], &fix.new_text);
                cells = self.cells(kept, cells, None);
                cells = self.cells(added, cells, Some(FIX_ADDED));
                cells = self.cells(kept_after, cells, None);
                position = range.end;
            }
            self.cells(&file.text[position..span.end as usize], cells, None);
            self.out.push('\n');
        }
        true
    }

    /// Writes `text` starting at terminal cell `column`, expanding tabs, in
    /// `color` when one is given, and returns the column after it.
    fn cells(&mut self, text: &str, mut column: usize, color: Option<&str>) -> usize {
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
        snippets[1..].sort_by(|a, b| a.file.name.cmp(&b.file.name));
    }
    snippets
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

/// Splits `line` into pieces with their highlight color, from the file's
/// `tokens`. Text between tokens that is not white space is a comment (or a
/// character the lexer rejected, which a mark shows anyway).
fn highlight(
    file: &SourceFile,
    tokens: &[Token],
    line: Span,
) -> Vec<(Range<usize>, Option<&'static str>)> {
    let line = line.range();
    let mut pieces = Vec::new();
    let gap = |pieces: &mut Vec<_>, gap: Range<usize>| {
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
        gap(&mut pieces, position..piece.start);
        position = piece.end;
        pieces.push((piece, token_color(file, t)));
    }
    gap(&mut pieces, position..line.end);
    pieces
}

fn token_color(file: &SourceFile, t: &Token) -> Option<&'static str> {
    match t.kind {
        TokenKind::String | TokenKind::CString => Some(LIGHT_GREEN),
        TokenKind::Number | TokenKind::Char | TokenKind::ByteChar => Some(LIGHT_ORANGE),
        TokenKind::DocComment => Some(GREY),
        kind if token::keyword(file.slice(t.span)) == Some(kind) => Some(PURPLE),
        _ => None,
    }
}

fn line_number_width(d: &Diagnostic, snippets: &[Snippet]) -> usize {
    let lines = snippets
        .iter()
        .flat_map(|s| s.lines.iter().map(|l| l.number));
    let fixes = d
        .notes
        .iter()
        .flat_map(|n| &n.fixes)
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
mod tests;
