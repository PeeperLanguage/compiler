//! Applying the fixes that diagnostics suggest without asking the programmer:
//! which of them can go in together, and writing them into the files.

use std::io::Write;
use std::path::Path;
use std::sync::Arc;

use source::SourceFile;
use source::Span;

use crate::Diagnostic;

/// Replaces a span of a file with new text: an empty span inserts, empty text
/// removes.
#[derive(Clone, Debug)]
pub struct Fix {
    pub file: Arc<SourceFile>,
    pub span: Span,
    pub new_text: String,
}

/// The fixes to apply for `all`, with the help line each comes from: each
/// diagnostic's first help line with fixes (the next ones are alternatives),
/// when it is sure; a guess is left for the programmer. A help line is left
/// out whole, so it is never applied in part, when one of its fixes is outside
/// `root` (a bundled library) or overlaps a fix taken before it.
pub fn chosen<'d>(
    all: impl IntoIterator<Item = &'d Diagnostic>,
    root: &Path,
) -> Vec<(&'d Fix, &'d str)> {
    let overlap = |a: &Fix, b: &Fix| {
        let (a_span, b_span) = (a.span, b.span);
        a.file.path == b.file.path
            && (a_span.start == b_span.start
                || a_span.start < b_span.end && b_span.start < a_span.end)
    };
    let mut chosen: Vec<(&Fix, &str)> = Vec::new();
    for diagnostic in all {
        let Some(help) = diagnostic
            .notes
            .iter()
            .find(|note| !note.fixes().is_empty())
        else {
            continue;
        };
        let fits = help.sure_fix()
            && help.fixes().iter().all(|fix| {
                Path::new(&fix.file.path).starts_with(root)
                    && !chosen.iter().any(|&(taken, _)| overlap(fix, taken))
            });
        if fits {
            chosen.extend(help.fixes().iter().map(|fix| (fix, help.text.as_str())));
        }
    }
    chosen
}

/// Writes each file `fixes` touch with its fixes applied, from the last one
/// up so the earlier spans still point at the same text, and lists each fix
/// in `out` by where it is and its help line.
pub fn write(fixes: &[(&Fix, &str)], out: &mut impl Write) -> std::io::Result<()> {
    let mut fixes = fixes.to_vec();
    fixes.sort_by(|(a, _), (b, _)| {
        let by_file = a.file.path.cmp(&b.file.path);
        by_file.then(b.span.start.cmp(&a.span.start))
    });
    for in_file in fixes.chunk_by(|(a, _), (b, _)| a.file.path == b.file.path) {
        let file = &in_file[0].0.file;
        let mut text = file.text.clone();
        for (fix, _) in in_file {
            text.replace_range(fix.span.range(), &fix.new_text);
        }
        replace_file(Path::new(&file.path), &text)?;
        for (fix, help) in in_file.iter().rev() {
            let (line, column) = file.position(fix.span.start as usize);
            writeln!(out, "fixed {}:{line}:{column}: {help}", file.path)?;
        }
    }
    Ok(())
}

/// Replaces the text of the file `path` (the file a symlink names, not the
/// link) with `text`, keeping its permissions. `text` is written whole to a
/// sibling first, so a failed write leaves the original untouched.
fn replace_file(path: &Path, text: &str) -> std::io::Result<()> {
    let path = std::fs::canonicalize(path)?;
    let mut name = path.file_name().unwrap_or_default().to_os_string();
    name.push(format!(".fix{}", std::process::id()));
    let temporary = path.with_file_name(name);
    let written = std::fs::write(&temporary, text)
        .and_then(|()| {
            std::fs::set_permissions(&temporary, std::fs::metadata(&path)?.permissions())
        })
        .and_then(|()| std::fs::rename(&temporary, &path));
    if written.is_err() {
        _ = std::fs::remove_file(&temporary);
    }
    written
}

#[cfg(test)]
mod tests {
    use std::path::Path;
    use std::sync::Arc;

    use source::SourceFile;
    use source::Span;

    use super::Fix;
    use crate::Diagnostic;

    /// Only a diagnostic's first help line is applied, and a help line is
    /// dropped whole when it is a guess, overlaps a fix already taken or
    /// reaches outside the project.
    #[test]
    fn chooses_fixes() {
        let file = Arc::new(SourceFile::new("/app/main.peep", "let a = cout"));
        let library = Arc::new(SourceFile::new("/libs/core/x.peep", "x"));
        let fix = |file: &Arc<SourceFile>, start: usize, end: usize| Fix {
            file: file.clone(),
            span: Span::new(start, end),
            new_text: String::new(),
        };
        let code = crate::UNDEFINED_NAME;
        let all = [
            Diagnostic::new(code, "")
                .help("first", vec![fix(&file, 8, 12)])
                .help("alternative", vec![fix(&file, 0, 3)]),
            Diagnostic::new(code, "").help("overlaps", vec![fix(&file, 4, 5), fix(&file, 10, 10)]),
            Diagnostic::new(code, "").help("same start", vec![fix(&file, 8, 8)]),
            Diagnostic::new(code, "").help("library", vec![fix(&file, 0, 0), fix(&library, 0, 1)]),
            Diagnostic::new(code, "").guess("a guess", vec![fix(&file, 0, 0)]),
            Diagnostic::new(code, "").help("after", vec![fix(&file, 12, 12)]),
        ];
        let chosen = super::chosen(&all, Path::new("/app"));
        let helps: Vec<&str> = chosen.iter().map(|&(_, help)| help).collect();
        assert_eq!(helps, ["first", "after"]);
    }

    /// A replaced file keeps its permissions and leaves no sibling behind; a
    /// file that is not there is an error and gets no new file.
    #[test]
    fn replaces_whole_file() {
        let folder = std::env::temp_dir().join(format!("peeper-fix-{}", std::process::id()));
        std::fs::create_dir_all(&folder).unwrap();
        let file = folder.join("a.peep");
        std::fs::write(&file, "old").unwrap();
        super::replace_file(&file, "new text").unwrap();
        assert_eq!(std::fs::read_to_string(&file).unwrap(), "new text");
        assert_eq!(std::fs::read_dir(&folder).unwrap().count(), 1);
        let missing = folder.join("b.peep");
        assert!(super::replace_file(&missing, "x").is_err());
        assert_eq!(std::fs::read_dir(&folder).unwrap().count(), 1);
        std::fs::remove_dir_all(&folder).unwrap();
    }
}
