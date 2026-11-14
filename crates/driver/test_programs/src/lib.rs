//! The compiler's test programs, in `test_programs/<subject>/`: each is a
//! file `<name>.peep`, or a folder `<name>/` of a program of several files
//! (its `main.peep` and modules), whose name is its package's.
//!
//! The lines its files start with say what to do with it:
//!
//! - `//@ run` runs it; `//@ dump: mir` or `//@ dump: ll` dumps it; without
//!   either, it is checked;
//! - `//@ library` builds it as a library, not a program;
//! - `//@ target: i686-linux` builds it for that target;
//! - `//@ release` dumps a release build;
//! - `//@ exit-code: N` is the run's exit code, 0 when not written.
//!
//! A `//~` mark says what the compiler reports at its line: `//~ error[T0001]:
//! message` or `//~ warning[W0001]: message` a diagnostic there, `//~ label:
//! text` a label, `//~ help: text`, `//~ note: text` or `//~ fix: text` a
//! diagnostic's help, note or fix, and `//~ fixed: text` the line a fix or
//! help shows. Every diagnostic is marked; the other marks are checked when
//! written. A line of marks alone marks the nearest line above with code.
//!
//! Beside a program, `<name>.out` and `<name>.err` hold what a run writes to
//! stdout and stderr, and `<name>.mir` or `<name>.ll` its dump.

use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;

/// A test program and what it expects.
pub struct TestProgram {
    /// its package's name
    pub name: String,
    /// `<name>.peep`, or the folder `<name>/`
    pub path: PathBuf,
    /// each source file, by its path under `src/`, with its text
    pub sources: Vec<(String, String)>,
    pub mode: Mode,
    pub library: bool,
    pub target: Option<String>,
    pub release: bool,
    pub exit_code: u8,
    pub marks: Vec<Mark>,
}

/// What is done with a test program.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Check,
    Run,
    /// `-dump mir` or `-dump ll`, by that word
    Dump(&'static str),
}

/// What is reported at a line: a diagnostic (`error[T0001]: message`), or
/// one of its parts (`label: text`).
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Mark {
    /// the file's path under `src/`
    pub file: String,
    /// 1-based
    pub line: usize,
    pub text: String,
}

impl Mark {
    /// Whether this marks a diagnostic, not one of its parts.
    pub fn is_diagnostic(&self) -> bool {
        self.text.starts_with("error[") || self.text.starts_with("warning[")
    }
}

/// Every test program under the checkout's `test_programs/`, by path.
pub fn all() -> Vec<TestProgram> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../test_programs");
    let mut programs = Vec::new();
    let subjects = std::fs::read_dir(&root).expect("test_programs exists");
    for subject in subjects.map(|entry| entry.expect("a subject lists").path()) {
        for entry in std::fs::read_dir(&subject).expect("a subject folder lists") {
            let path = entry.expect("a test program lists").path();
            let is_program = path.is_dir() || path.extension().is_some_and(|ext| ext == "peep");
            if is_program {
                programs.push(TestProgram::read(path));
            }
        }
    }
    programs.sort_by(|a, b| a.path.cmp(&b.path));
    // A name is a package's, which imports name and a project is made under.
    let mut names = HashSet::new();
    for program in &programs {
        assert!(
            names.insert(&program.name),
            "two test programs are named {}",
            program.name
        );
    }
    programs
}

impl TestProgram {
    /// The program at `path`, a `.peep` file or a folder of them.
    fn read(path: PathBuf) -> TestProgram {
        let name = path.file_stem().expect("a test program has a name");
        let name = name.to_string_lossy().into_owned();
        let mut sources = Vec::new();
        if path.is_dir() {
            read_folder(&path, &path, &mut sources);
            sources.sort();
        } else {
            let text = std::fs::read_to_string(&path).expect("a test program reads");
            sources.push(("main.peep".to_string(), text));
        }
        let mut program = TestProgram {
            name,
            path,
            sources: Vec::new(),
            mode: Mode::Check,
            library: false,
            target: None,
            release: false,
            exit_code: 0,
            marks: Vec::new(),
        };
        let header = (sources.iter())
            .flat_map(|(_, text)| text.lines().map_while(|line| line.strip_prefix("//@ ")));
        for line in header {
            let (key, value) = line.split_once(": ").unwrap_or((line, ""));
            match (key, value) {
                ("run", "") => program.mode = Mode::Run,
                ("dump", "mir") => program.mode = Mode::Dump("mir"),
                ("dump", "ll") => program.mode = Mode::Dump("ll"),
                ("library", "") => program.library = true,
                ("target", target) => program.target = Some(target.to_string()),
                ("release", "") => program.release = true,
                ("exit-code", code) => program.exit_code = code.parse().expect("an exit code"),
                _ => panic!("{}: unknown `//@ {line}`", program.name),
            }
        }
        for (file, text) in &sources {
            program.marks.extend(marks(file, text));
        }
        program.sources = sources;
        program
    }

    /// The text of `peeper.toml` for this program's project.
    pub fn manifest(&self) -> String {
        let build = if self.library { "lib" } else { "program" };
        format!("name = \"{}\"\nbuild = \"{build}\"\n", self.name)
    }

    /// Writes this program as a project in `folder`, emptied first.
    pub fn write_project(&self, folder: &Path) {
        _ = std::fs::remove_dir_all(folder);
        for (file, text) in &self.sources {
            let path = folder.join("src").join(file);
            std::fs::create_dir_all(path.parent().unwrap()).expect("the source folder is made");
            std::fs::write(path, text).expect("a source file is written");
        }
        std::fs::write(folder.join("peeper.toml"), self.manifest()).expect("the manifest writes");
    }

    /// The file beside this program that ends in `extension`.
    pub fn beside(&self, extension: &str) -> PathBuf {
        self.path
            .with_file_name(format!("{}.{extension}", self.name))
    }

    /// What the file beside it that ends in `extension` holds, empty when
    /// there is none.
    pub fn expected(&self, extension: &str) -> String {
        std::fs::read_to_string(self.beside(extension)).unwrap_or_default()
    }

    /// Whether its marks say the compiler reports an error.
    pub fn has_errors(&self) -> bool {
        self.marks
            .iter()
            .any(|mark| mark.text.starts_with("error["))
    }
}

/// Reads the `.peep` files in `folder`, a program's `root` or in it.
fn read_folder(root: &Path, folder: &Path, found: &mut Vec<(String, String)>) {
    for entry in std::fs::read_dir(folder).expect("a program folder lists") {
        let path = entry.expect("a program file lists").path();
        if path.is_dir() {
            read_folder(root, &path, found);
            continue;
        }
        if path.extension().is_none_or(|extension| extension != "peep") {
            continue;
        }
        let file = path
            .strip_prefix(root)
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        found.push((
            file,
            std::fs::read_to_string(&path).expect("a source file reads"),
        ));
    }
}

/// The `//~` marks in `text`, the file `file`.
fn marks(file: &str, text: &str) -> Vec<Mark> {
    let mut found = Vec::new();
    let mut code_line = 0;
    for (index, line) in text.lines().enumerate() {
        let Some(at) = line.find("//~") else {
            if !line.trim().is_empty() {
                code_line = index + 1;
            }
            continue;
        };
        if !line[..at].trim().is_empty() {
            code_line = index + 1;
        }
        assert!(
            code_line > 0,
            "{file}:{}: a mark before any code",
            index + 1
        );
        let text = line[at + 3..].trim().to_string();
        found.push(Mark {
            file: file.to_string(),
            line: code_line,
            text,
        });
    }
    found
}

/// What `marked` and `reported` disagree on: each diagnostic must be
/// marked, as often as it is reported, and each other mark reported.
pub fn mismatches(marked: &[Mark], reported: &[Mark]) -> Vec<String> {
    let mut problems = Vec::new();
    let mut unmarked: Vec<&Mark> = reported
        .iter()
        .filter(|mark| mark.is_diagnostic())
        .collect();
    for mark in marked {
        if let Some(at) = unmarked.iter().position(|found| *found == mark) {
            unmarked.remove(at);
        } else if mark.is_diagnostic() || !reported.contains(mark) {
            problems.push(format!(
                "{}:{}: not reported: {}",
                mark.file, mark.line, mark.text
            ));
        }
    }
    for mark in unmarked {
        problems.push(format!(
            "{}:{}: not marked: {}",
            mark.file, mark.line, mark.text
        ));
    }
    problems
}

/// The diagnostics in `stderr`, a compile of the project in `folder`, as
/// marks, with what follows them: the program's own stderr. A path in them
/// is from the folder, as a page writes it; a line they show is without its
/// marks.
pub fn reported<'a>(stderr: &'a str, folder: &Path) -> (Vec<Mark>, &'a str) {
    // The compiler writes a path with `/` on every system.
    let folder = folder.to_string_lossy().replace('\\', "/");
    let sources = format!("{folder}/src/");
    let (diagnostics, program) = match stderr.find("\nCompilation ") {
        Some(at) => {
            let rest = &stderr[at + 1..];
            let end = rest.find('\n').map_or(rest.len(), |end| end + 1);
            (&stderr[..at], &rest[end..])
        }
        None => ("", stderr),
    };
    let mut found = Vec::new();
    // The diagnostic being read, and its place once read.
    let mut diagnostic: Option<(String, Option<(String, usize)>)> = None;
    let (mut file, mut line) = (String::new(), 0);
    // After a note: the lines it shows are the fixed ones.
    let mut previews = false;
    let mut mark = |file: &str, line: usize, text: String| {
        found.push(Mark {
            file: file.to_string(),
            line,
            text,
        });
    };
    for text in diagnostics.lines() {
        let trimmed = text.trim_start();
        if let Some(rest) = text.strip_prefix('[')
            && let Some((code, message)) = rest.split_once("]: ")
        {
            let severity = if code.starts_with('W') {
                "warning"
            } else {
                "error"
            };
            let message = message.replace(&format!("{folder}/"), "");
            diagnostic = Some((format!("{severity}[{code}]: {message}"), None));
            previews = false;
        } else if let Some(place) =
            (trimmed.strip_prefix("--> ")).or_else(|| trimmed.strip_prefix("::: "))
        {
            let mut parts = place.rsplitn(3, ':');
            let (_, at, path) = (parts.next(), parts.next(), parts.next());
            line = at
                .and_then(|at| at.parse().ok())
                .expect("a place has a line");
            let path = path.expect("a place has a file");
            file = path.strip_prefix(&sources).unwrap_or(path).to_string();
            if let Some((message, primary @ None)) = &mut diagnostic {
                *primary = Some((file.clone(), line));
                mark(&file, line, message.clone());
            }
        } else if let Some((number, rest)) = trimmed.split_once(" |")
            && let Ok(number) = number.parse::<usize>()
        {
            line = number;
            if previews {
                let shown = rest.split("//~").next().unwrap_or_default();
                mark(&file, line, format!("fixed: {}", shown.trim()));
            }
        } else if let Some(rest) = trimmed.strip_prefix('|')
            && !previews
            && let rest = rest.trim_start()
            && rest.starts_with(['^', '-'])
        {
            // A line may underline twice: `^^^^   ---- why`.
            let label = rest.trim_start_matches(['^', '-', ' ']);
            mark(
                &file,
                line,
                format!("label: {label}").trim_end().to_string(),
            );
        } else if let Some(note) = trimmed.strip_prefix("= ")
            && let Some((_, Some((primary_file, primary)))) = &diagnostic
        {
            mark(primary_file, *primary, note.to_string());
            previews = true;
        }
    }
    (found, program)
}
