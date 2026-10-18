//! Source files and byte spans of their text.

/// The byte range `start..end` of one file's text.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Span {
    pub start: u32,
    pub end: u32,
}

impl Span {
    pub fn new(start: usize, end: usize) -> Span {
        Span {
            start: start as u32,
            end: end as u32,
        }
    }

    /// An empty span at `at`.
    pub fn at(at: usize) -> Span {
        Span::new(at, at)
    }

    pub fn range(self) -> std::ops::Range<usize> {
        self.start as usize..self.end as usize
    }
}

/// One source file. It records where each line starts, so a byte offset becomes
/// a line and column only when a message needs them.
#[derive(Debug)]
pub struct SourceFile {
    pub path: String, // where the file was read from, shown in messages
    pub text: String,
    lines: Vec<u32>, // byte offset where each line starts
}

impl SourceFile {
    pub fn new(path: impl Into<String>, text: impl Into<String>) -> SourceFile {
        let text = text.into();
        let mut lines = Vec::with_capacity(text.bytes().filter(|&b| b == b'\n').count() + 1);
        lines.push(0);
        lines.extend(text.match_indices('\n').map(|(i, _)| i as u32 + 1));
        SourceFile {
            path: path.into(),
            text,
            lines,
        }
    }

    /// The text of `span`.
    pub fn slice(&self, span: Span) -> &str {
        &self.text[span.range()]
    }

    /// The 1-based line that holds `offset`.
    pub fn line(&self, offset: usize) -> usize {
        self.lines
            .partition_point(|&start| start as usize <= offset)
    }

    /// The 1-based line and column of `offset`; columns count characters. Before
    /// an offset inside a character (a lexer span may end there), each byte of
    /// the cut character counts as one.
    pub fn position(&self, offset: usize) -> (usize, usize) {
        let line = self.line(offset);
        let start = self.lines[line - 1] as usize;
        let columns: usize = self.text.as_bytes()[start..offset]
            .utf8_chunks()
            .map(|chunk| chunk.valid().chars().count() + chunk.invalid().len())
            .sum();
        (line, columns + 1)
    }

    /// The span of the 1-based line `n` without its line end, or an empty span
    /// at the end of the text when the file has no such line.
    pub fn line_span(&self, n: usize) -> Span {
        if n < 1 || n > self.lines.len() {
            return Span::at(self.text.len());
        }
        let start = self.lines[n - 1] as usize;
        let mut end = match self.lines.get(n) {
            Some(&next) => next as usize - 1,
            None => self.text.len(),
        };
        if end > start && self.text.as_bytes()[end - 1] == b'\r' {
            end -= 1;
        }
        Span::new(start, end)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn position() {
        let f = SourceFile::new("t.peep", "a\tb\nçd\r\nlast");
        for (offset, want) in [
            (0, (1, 1)),
            (2, (1, 3)),
            (4, (2, 1)),
            (6, (2, 2)),
            (9, (3, 1)),
            (13, (3, 5)),
        ] {
            assert_eq!(f.position(offset), want, "offset {offset}");
        }
        assert_eq!(f.position(5), (2, 2)); // inside "ç": its first byte counts
        assert_eq!(f.line_span(2), Span::new(4, 7)); // without "\r\n"
        assert_eq!(f.line_span(3), Span::new(9, 13));
        assert_eq!(f.line_span(4), Span::at(13));
    }

    #[test]
    fn empty_file() {
        let f = SourceFile::new("t.peep", "");
        assert_eq!(f.position(0), (1, 1));
        assert_eq!(f.line_span(1), Span::at(0));
    }
}
