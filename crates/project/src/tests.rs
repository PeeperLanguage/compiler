use std::fmt::Write;

use super::*;

/// The module of `program` at the import path `path`.
fn module_named<'p>(program: &'p Program, path: &str) -> &'p Module {
    let module = program.modules.iter().find(|module| module.path == path);
    module.unwrap_or_else(|| panic!("no module {path}"))
}

/// The paths of `program`'s modules, in its order.
fn order(program: &Program) -> Vec<&str> {
    program
        .modules
        .iter()
        .map(|module| module.path.as_str())
        .collect()
}

/// Writes `files` (paths relative to a new temporary folder) and returns that
/// folder and its packages: "myapp" in src, "core" in libs/core/src.
fn write(test: &str, files: &[(&str, &str)]) -> Project {
    let root = std::env::temp_dir().join(format!("peeper-project-{}-{test}", std::process::id()));
    _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(&root).unwrap();
    for (name, text) in files {
        let file = root.join(name);
        std::fs::create_dir_all(file.parent().unwrap()).unwrap();
        std::fs::write(file, text).unwrap();
    }
    let packages = HashMap::from([
        ("myapp".to_string(), root.join("src")),
        ("core".to_string(), root.join("libs/core/src")),
    ]);
    Project {
        packages,
        entry: root.join("src/main.peep"),
        root,
        name: "myapp".to_string(),
    }
}

/// "module: CODE message" for every diagnostic, sorted.
/// Each diagnostic as the path of the module its first label is in, its
/// code and its message, sorted.
fn messages(program: &Program) -> Vec<String> {
    let module = |d: &Diagnostic| {
        let file = Path::new(&d.labels.first()?.file.path);
        Some(program.module_of(file)?.path.as_str())
    };
    let mut out: Vec<String> = program
        .diagnostics
        .iter()
        .map(|d| {
            format!(
                "{}: {} {}",
                module(d).unwrap_or("program"),
                d.code.id,
                d.message
            )
        })
        .collect();
    out.sort();
    out
}

fn imports(program: &Program, module: &Module) -> Vec<String> {
    let alias = |import: &Import| {
        let name = import.name(module, &program.modules);
        name.map_or("", |(name, _)| name)
    };
    let path = |import: &Import| &program.modules[import.module.index()].path;
    module
        .imports
        .iter()
        .map(|i| format!("{}={}", alias(i), path(i)))
        .collect()
}

/// `..` and `.` are dropped as text, and nothing goes above the root.
#[test]
fn canonical_paths() {
    let here = std::env::current_dir().unwrap();
    for (path, want) in [
        ("/a/./b/../c", PathBuf::from("/a/c")),
        ("/a/b/../../..", PathBuf::from("/")),
        ("x/../y", here.join("y")),
    ] {
        assert_eq!(canonical_path(Path::new(path)).unwrap(), want, "{path}");
    }
}

/// On Windows a drive letter is upper case and a `\\?\` prefix is dropped,
/// as an editor and `std::fs::canonicalize` spell the same file apart.
#[cfg(windows)]
#[test]
fn canonical_windows_paths() {
    for (path, want) in [
        (r"c:\Work Dir\main.peep", r"C:\Work Dir\main.peep"),
        (r"\\?\C:\a\b", r"C:\a\b"),
        (r"C:/a/./b", r"C:\a\b"),
        (r"\\?\UNC\server\share\x", r"\\server\share\x"),
    ] {
        assert_eq!(
            canonical_path(Path::new(path)).unwrap(),
            PathBuf::from(want),
            "{path}"
        );
    }
}

/// A file named from the current folder gets an absolute root, the form
/// `load` gives its files' paths.
#[test]
fn a_single_file_has_an_absolute_root() {
    let project = find(Path::new("src/lib.rs"), Path::new("")).unwrap();
    let here = std::env::current_dir().unwrap();
    assert_eq!(project.root, here.join("src"));
    assert_eq!(project.entry, here.join("src/lib.rs"));
}

#[test]
fn create_makes_a_project_find_reads() {
    let root = write("create", &[("libs/core/src/global.peep", "")]).root;
    let create = |name| create(&root, name, &root.join("libs"));
    assert_eq!(
        create("my-app").map_err(|e| e.contains(manifest::NAME_RULE)),
        Err(true)
    );
    let taken = "project name `core` is taken by a bundled library";
    assert_eq!(create("core"), Err(taken.to_string()));
    assert!(!root.join(manifest::FILE_NAME).exists());
    assert_eq!(create("hello"), Ok(true));
    let found = find(&root, &root.join("libs")).unwrap();
    assert_eq!(
        (found.name.as_str(), &found.entry),
        ("hello", &root.join("src/main.peep"))
    );
    assert_eq!(std::fs::read_to_string(&found.entry).unwrap(), HELLO);
    // A second create keeps the project as it is.
    let error = create("other").unwrap_err();
    assert!(error.ends_with("peeper.toml: already exists"), "{error}");
    assert_eq!(find(&root, &root.join("libs")).unwrap().name, "hello");
    // A main.peep already there is kept.
    std::fs::remove_file(root.join(manifest::FILE_NAME)).unwrap();
    std::fs::write(&found.entry, "fn main() {}").unwrap();
    assert_eq!(create("hello"), Ok(false));
    assert_eq!(
        std::fs::read_to_string(&found.entry).unwrap(),
        "fn main() {}"
    );
}

#[test]
fn loads_nested_imports() {
    let project = write(
        "nested",
        &[
            (
                "src/main.peep",
                "import \"myapp:a\";\nimport \"myapp:b/c\" as c2;\nfn main() {}",
            ),
            ("src/a.peep", "import \"myapp:b/c\";\nfn A() {}"),
            ("src/b/c.peep", "fn C() {}"),
            ("libs/core/src/global.peep", "fn Exit(code: i32) {}"),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(messages(&program), Vec::<String>::new());
    assert_eq!(
        order(&program).join(" "),
        "core:global myapp:b/c myapp:a myapp:main"
    );
    assert_eq!(
        imports(&program, module_named(&program, "myapp:main")),
        ["a=myapp:a", "c2=myapp:b/c", "=core:global"]
    );
    assert!(
        module_named(&program, "core:global").imports.is_empty(),
        "the prelude imports itself"
    );
}

#[test]
fn import_errors() {
    let main = [
        r#"import "myapp:missing";"#,
        r#"import "myapp:./x";"#,
        r#"import "myapp:../x";"#,
        r#"import "myapp:";"#,
        r#"import "other:x";"#,
        r#"import "x/y";"#,
        r#"import "myapp:x.txt";"#,
        r#"import "myapp:a";"#,
        r#"import "myapp:c" as a;"#,
        r#"import "myapp:a.peep";"#, // the same module as myapp:a under the same name: kept once
        r#"import "myapp:b";"#,
    ]
    .join("\n");
    let project = write(
        "errors",
        &[
            ("src/main.peep", &main),
            ("src/a.peep", ""),
            ("src/b.peep", "fn f( {"), // a syntax error belongs to b's own diagnostics
            ("src/c.peep", ""),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    let missing = project.root.join("src/missing.peep");
    let want = [
        format!("myapp:main: M0001 module not found: {}", missing.display()),
        "myapp:main: M0003 import path needs a package, such as `myapp:utils/file`".to_string(),
        "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
        "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
        "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
        r#"myapp:main: M0003 invalid import extension ".txt""#.to_string(),
        "myapp:main: M0003 unknown package `other`".to_string(),
        "myapp:main: M0005 import alias already in use".to_string(),
    ];
    let got: Vec<String> = messages(&program)
        .into_iter()
        .filter(|m| m.starts_with("myapp:main: "))
        .collect();
    assert_eq!(got, want);
    assert!(!program.checkable);
    let b = messages(&program)
        .into_iter()
        .find(|m| m.starts_with("myapp:b: "));
    let expected_token = format!("myapp:b: {} ", diagnostics::EXPECTED_TOKEN.id);
    assert!(b.is_some_and(|b| b.starts_with(&expected_token)));
}

/// A missing module's guess is a module next to it, never the module
/// that imports it, which would import itself.
#[test]
fn guesses_a_missing_module_but_not_itself() {
    let main = "import \"myapp:shapez\";\nimport \"myapp:mainn\";\nfn main() {}";
    let project = write("guess", &[("src/main.peep", main), ("src/shapes.peep", "")]);
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    let guesses: Vec<&str> = program
        .diagnostics
        .iter()
        .flat_map(|d| d.notes.iter().map(|note| note.text.as_str()))
        .collect();
    assert_eq!(guesses, ["did you mean \"myapp:shapes\"?"]);
}

#[test]
fn reports_imports_at_their_line() {
    let project = write(
        "line",
        &[("src/main.peep", "fn main() {}\nimport \"myapp:missing\";\n")],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    let main = module_named(&program, "myapp:main");
    assert_eq!(program.diagnostics.len(), 1, "{:?}", messages(&program));
    let label = &program.diagnostics[0].labels[0];
    assert_eq!(main.ast.file.position(label.span.start as usize), (2, 1));
}

/// Each place of the import cycles `program` found, as the importer's
/// path and the place's message, then each note.
fn cycle_steps(program: &Program) -> Vec<String> {
    let cycles = program.diagnostics.iter();
    let cycles = cycles.filter(|d| d.code == diagnostics::CYCLIC_IMPORT);
    let steps = |d: &Diagnostic| {
        let importer = |label: &diagnostics::Label| {
            let file = Path::new(&label.file.path);
            let path = &program.module_of(file).unwrap().path;
            format!("{path}: {}", label.message)
        };
        let notes = d.notes.iter().map(|note| note.text.clone());
        d.labels
            .iter()
            .map(importer)
            .chain(notes)
            .collect::<Vec<_>>()
    };
    cycles.flat_map(steps).collect()
}

#[test]
fn reports_a_cycle_with_each_import() {
    let project = write(
        "cycle",
        &[
            ("src/main.peep", r#"import "myapp:a";"#),
            ("src/a.peep", r#"import "myapp:b";"#),
            ("src/b.peep", r#"import "myapp:a";"#),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        messages(&program),
        ["myapp:a: M0002 modules import each other in a cycle"]
    );
    assert_eq!(
        cycle_steps(&program),
        [
            "myapp:a: `myapp:a` imports `myapp:b`",
            "myapp:b: `myapp:b` imports `myapp:a`, closing the cycle",
        ]
    );
}

/// A cycle through the prelude, closed by a written import of it, is
/// reported once, at that import.
#[test]
fn a_cycle_through_the_prelude_is_reported_once() {
    let project = write(
        "prelude_cycle",
        &[
            ("src/main.peep", "fn main() {}"),
            ("libs/core/src/global.peep", r#"import "core:x";"#),
            ("libs/core/src/x.peep", r#"import "core:global";"#),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        cycle_steps(&program),
        [
            "core:global: `core:global` imports `core:x`",
            "core:x: `core:x` imports `core:global`, closing the cycle",
        ]
    );
}

/// The prelude's implicit import has no place; its step is a note.
#[test]
fn a_cycle_through_the_implicit_prelude_import_notes_it() {
    let project = write(
        "implicit_cycle",
        &[
            ("src/main.peep", "fn main() {}"),
            ("libs/core/src/global.peep", r#"import "core:x";"#),
            ("libs/core/src/x.peep", "fn F() {}"),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        cycle_steps(&program),
        [
            "core:global: `core:global` imports `core:x`",
            "`core:x` imports `core:global` implicitly, as every module does, closing the cycle",
        ]
    );
}

#[test]
fn redundant_prelude_import() {
    let project = write(
        "prelude",
        &[
            ("src/main.peep", r#"import "core:global";"#),
            ("libs/core/src/global.peep", ""),
        ],
    );
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        messages(&program),
        ["myapp:main: S0004 `core:global` is imported automatically"]
    );
    // the implicit import is kept beside the written one
    assert_eq!(
        imports(&program, module_named(&program, "myapp:main")),
        ["global=core:global", "=core:global"]
    );
}

#[test]
fn a_single_file_cannot_import() {
    let mut project = write(
        "single",
        &[
            ("tool.peep", "import \"myapp:a\";\nfn main() {}"),
            ("libs/core/src/global.peep", ""),
        ],
    );
    project.entry = project.root.join("tool.peep");
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        messages(&program),
        ["tool: M0003 a single file cannot import; check its project folder instead"]
    );
    assert_eq!(
        imports(&program, module_named(&program, "tool")),
        ["=core:global"],
        "the prelude still comes in"
    );
}

#[test]
fn unreadable_and_non_utf8_entries() {
    let mut project = write("unreadable", &[("src/bad.peep", "fn \u{0} main")]);
    std::fs::write(project.root.join("src/bad.peep"), b"fn \xff main() {}").unwrap();
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    let got = messages(&program);
    assert!(
        got.len() == 1 && got[0].starts_with("program: M0001 read module: "),
        "{got:?}"
    );
    project.entry = project.root.join("src/bad.peep");
    let program = load(&project, &HashMap::<PathBuf, String>::new());
    assert_eq!(
        messages(&program),
        ["myapp:bad: L0001 file is not valid UTF-8"]
    );
}

/// A wide, deep project; the order must be the same every run.
#[test]
fn loads_many_modules_in_a_stable_order() {
    let mut files = vec![("libs/core/src/global.peep".to_string(), String::new())];
    let mut main = String::new();
    for i in 0..200 {
        _ = writeln!(main, "import \"myapp:m{i}\";");
        let mut module = String::new();
        for j in i + 1..(i + 4).min(200) {
            _ = writeln!(module, "import \"myapp:m{j}\";");
        }
        module.push_str("fn f() {}\n");
        files.push((format!("src/m{i}.peep"), module));
    }
    files.push(("src/main.peep".to_string(), main));
    let files: Vec<(&str, &str)> = files
        .iter()
        .map(|(n, t)| (n.as_str(), t.as_str()))
        .collect();
    let project = write("many", &files);
    let first = load(&project, &HashMap::<PathBuf, String>::new());
    assert!(messages(&first).is_empty());
    assert_eq!(first.modules.len(), 202);
    for (index, module) in first.modules.iter().enumerate() {
        for import in &module.imports {
            assert!(
                import.module.index() < index,
                "{} before {}",
                module.path,
                first.modules[import.module.index()].path
            );
        }
    }
    for _ in 0..5 {
        assert_eq!(
            order(&load(&project, &HashMap::<PathBuf, String>::new())),
            order(&first),
            "order differs between runs"
        );
    }
}

#[test]
fn open_files_win_over_disk() {
    let project = write(
        "open",
        &[
            ("src/main.peep", "import \"myapp:a\";\nfn main() {}"),
            ("src/a.peep", "fn A( {"),
        ],
    );
    let open_files = HashMap::from([
        (
            project.root.join("src/a.peep"),
            "import \"myapp:b\";\nfn A() {}".to_string(),
        ),
        (project.root.join("src/b.peep"), "fn B() {}".to_string()), // not on disk
    ]);
    let program = load(&project, &open_files);
    assert_eq!(messages(&program), Vec::<String>::new());
    assert_eq!(module_named(&program, "myapp:b").ast.file.text, "fn B() {}");
}

#[test]
fn path_of_a_file() {
    let project = write("path", &[]);
    assert_eq!(
        path_of(&project.packages, &project.root.join("src/deep/x.peep")),
        ("myapp:deep/x".to_string(), true)
    );
    assert_eq!(
        path_of(&project.packages, &project.root.join("tool.peep")),
        ("tool".to_string(), false)
    );
    assert_eq!(
        resolve(&project.packages, "myapp:a.b.peep"),
        Ok(("myapp:a.b".to_string(), project.root.join("src/a.b.peep")))
    );
}
