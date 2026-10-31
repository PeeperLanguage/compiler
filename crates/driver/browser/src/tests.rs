use super::*;

fn files(entries: &[(&str, &str)]) -> HashMap<String, String> {
    let owned = entries
        .iter()
        .map(|&(name, text)| (name.to_string(), text.to_string()));
    owned.collect()
}

#[test]
fn an_entry_names_the_file_and_counts_utf16_columns() {
    // `𝄞` is two UTF-16 units, and the error's column is after it.
    let text =
        "import \"playground:other\";\nfn main() {\n    let a = c\"𝄞\"; other::Missing();\n}\n";
    let found = check(files(&[
        ("main.peep", text),
        ("other.peep", "fn Used() {}\n"),
    ]));
    let start = json!({ "line": 3, "column": 27 });
    let end = json!({ "line": 3, "column": 34 });
    let message = "unknown name `Missing` in module `other`";
    let expected = json!([{
        "severity": "error",
        "code": "T0002",
        "message": message,
        "place": { "file": "main.peep", "start": start, "end": end, "message": "" },
        "related": [],
    }]);
    assert_eq!(found, expected);
}

#[test]
fn an_entry_keeps_the_help_and_the_other_places() {
    // The reason a constant failed is at the loop, not at the constant.
    let text = "fn spin() -> i64 {\n    let mut t: i64 = 0;\n    \
        for i in 0..100000000000i64 {\n        t = t + 1;\n    }\n    return t;\n}\n\
        const X: i64 = spin();\nfn main() {}\n";
    let found = check(files(&[("main.peep", text)]));
    let related = &found[0]["related"][0];
    assert_eq!(related["message"], "this ran more than 1000000 steps");
    assert_eq!(related["start"], json!({ "line": 3, "column": 14 }));

    let text = "fn main() {\n    let value = 1;\n    let other = valeu;\n}\n";
    let found = check(files(&[("main.peep", text)]));
    let message = found[0]["message"].as_str().expect("a message is text");
    assert!(message.contains("help: "), "{message}");
}

#[test]
fn a_message_names_a_file_as_the_page_does() {
    let text = "import \"playground:othr\";\nfn main() {}\n";
    let found = check(files(&[("main.peep", text)]));
    let message = found[0]["message"].as_str().expect("a message is text");
    assert!(
        message.starts_with("module not found: othr.peep"),
        "{message}"
    );
}

#[test]
fn a_clean_program_has_no_entries() {
    let text = "fn main() {\n    println(1);\n}\n";
    assert_eq!(check(files(&[("main.peep", text)])), json!([]));
}

#[test]
fn a_failed_build_prints_as_the_terminal_does_and_does_not_start() {
    // The text, with the codes that color it taken out.
    fn plain(colored: &str) -> String {
        let mut parts = colored.split('\u{1b}');
        let first = parts.next().unwrap_or_default().to_string();
        parts.fold(first, |text, part| {
            text + part.split_once('m').map_or(part, |(_, rest)| rest)
        })
    }

    let text = "fn main() {\n    let total: i32 = true;\n    println(total);\n}\n";
    let found = run(files(&[("main.peep", text)]));
    let expected = "[T0001]: mismatched types: expected `i32`, found `bool`\n  \
        --> main.peep:2:22\n  | \n1 | fn main() {\n2 |     let total: i32 = true;\n  \
        |                      ^^^^\n  | \n  \
        = help: add `as i32` to convert, which can change the value\n  | \n\
        2 |     let total: i32 = true as i32;\n\nCompilation failed with 1 error(s)\n";
    let printed = found["diagnostics"].as_str().expect("diagnostics are text");
    assert_eq!(plain(printed), expected);
    assert!(printed.contains("\u{1b}[1;31m[T0001]"), "{printed}");
    assert_eq!(found["run"], Value::Null);
}

#[test]
fn a_syntax_error_the_parser_read_past_does_not_start() {
    let text = "fn main() {\n    println(1)\n}\n";
    let found = run(files(&[("main.peep", text)]));
    let printed = found["diagnostics"].as_str().expect("diagnostics are text");
    assert!(printed.contains("[P0002]"), "{printed}");
    assert_eq!(found["run"], Value::Null);
}

#[test]
fn a_warning_stays_apart_from_what_the_program_prints() {
    let text = "fn main() {\n    println(1);\n    return;\n    println(2);\n}\n";
    let found = run(files(&[("main.peep", text)]));
    let diagnostics = found["diagnostics"].as_str().expect("diagnostics are text");
    assert!(
        diagnostics.contains("Compilation succeeded with 1 warning(s)"),
        "{diagnostics}"
    );
    assert_eq!(
        found["run"]["output"],
        json!([{ "stream": "stdout", "text": "1\n" }])
    );
}

#[test]
fn every_core_file_is_built_in() {
    let core = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../_builtin_library/core/src");
    let mut on_disk: Vec<String> = std::fs::read_dir(core)
        .expect("the core library is there")
        .flatten()
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    on_disk.sort();
    assert_eq!(on_disk, CORE_FILES.map(|(name, _)| name));
}

/// The files under `folder`, by their path from `root`.
fn read_source(root: &Path, folder: &Path, found: &mut HashMap<String, String>) {
    for entry in std::fs::read_dir(folder)
        .expect("a fixture's folder reads")
        .flatten()
    {
        let path = entry.path();
        if path.is_dir() {
            read_source(root, &path, found);
        } else if let Ok(text) = std::fs::read_to_string(&path) {
            let name = path.strip_prefix(root).expect("the file is under its root");
            found.insert(name.to_string_lossy().replace('\\', "/"), text);
        }
    }
}

#[test]
fn check_fixtures_fail_here_with_the_same_number_of_errors() {
    let fixtures = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../x_test");
    let mut checked = 0;
    for folder in std::fs::read_dir(fixtures)
        .expect("x_test exists")
        .flatten()
    {
        let folder = folder.path();
        let Ok(text) = std::fs::read_to_string(folder.join("peeper.toml")) else {
            continue;
        };
        let manifest: toml_edit::DocumentMut =
            text.parse().expect("a fixture's peeper.toml parses");
        let test = |key: &str| manifest.get("test").and_then(|test| test.get(key));
        let outcome = test("outcome").and_then(toml_edit::Item::as_str);
        if test("mode").and_then(toml_edit::Item::as_str) != Some("check")
            || outcome != Some("failure")
            || test("target").is_some()
        {
            continue;
        }
        let name = manifest["name"].as_str().expect("a fixture has a name");
        let source = folder.join("src");
        let mut source_files = HashMap::new();
        read_source(&source, &source, &mut source_files);
        // The page's files are always one package; only imports name it here.
        let own_package = format!("\"{name}:");
        for text in source_files.values_mut() {
            *text = text.replace(&own_package, &format!("\"{PACKAGE}:"));
        }
        let found = check(source_files);
        let found = found.as_array().expect("a check gives a list");
        let errors = found
            .iter()
            .filter(|entry| entry["severity"] == "error")
            .count();
        assert!(errors > 0, "{name}: no error found");
        let wanted = test("stderr_contains").and_then(toml_edit::Item::as_array);
        let summary = wanted
            .into_iter()
            .flatten()
            .filter_map(|text| {
                text.as_str()?
                    .strip_prefix("Compilation failed with ")?
                    .strip_suffix(" error(s)")
            })
            .next();
        if let Some(summary) = summary {
            assert_eq!(errors.to_string(), summary, "{name}: {found:#?}");
        }
        checked += 1;
    }
    assert!(checked > 0, "no check fixtures found");
}

/// Every fixture that runs a program runs here too, from text as a page gives
/// it, with no backend, and does what its peeper.toml says. The fixtures test
/// compares it with the compiled program byte for byte.
#[test]
fn run_fixtures_run_here_with_the_same_results() {
    let fixtures = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../x_test");
    let mut failures = Vec::new();
    let mut ran = 0;
    for folder in std::fs::read_dir(fixtures)
        .expect("x_test exists")
        .flatten()
    {
        let folder = folder.path();
        let Ok(text) = std::fs::read_to_string(folder.join("peeper.toml")) else {
            continue;
        };
        let manifest: toml_edit::DocumentMut =
            text.parse().expect("a fixture's peeper.toml parses");
        let test = |key: &str| manifest.get("test").and_then(|test| test.get(key));
        if test("mode").and_then(toml_edit::Item::as_str) != Some("run") {
            continue;
        }
        let name = manifest["name"].as_str().expect("a fixture has a name");
        let source = folder.join("src");
        let mut source_files = HashMap::new();
        read_source(&source, &source, &mut source_files);
        let own_package = format!("\"{name}:");
        for text in source_files.values_mut() {
            *text = text.replace(&own_package, &format!("\"{PACKAGE}:"));
        }
        let target = test("target").and_then(toml_edit::Item::as_str);
        let target = Machine::named(target.unwrap_or(TARGET)).expect("the target is a machine");
        let (project, program) = load(source_files);
        let (results, interpreted) = interpret(&project, &program, target);
        let Some(interpreted) = interpreted else {
            // A fixture of a program that must not build.
            if test("outcome").and_then(toml_edit::Item::as_str) != Some("failure") {
                let errors: Vec<_> = results.diagnostics().map(|d| d.message.clone()).collect();
                failures.push(format!("{name}: did not start: {errors:?}"));
            }
            continue;
        };
        ran += 1;
        let (code, stdout, stderr) = (
            i64::from(interpreted.exit_code),
            String::from_utf8_lossy(&written(&interpreted, Stream::Stdout)).into_owned(),
            String::from_utf8_lossy(&written(&interpreted, Stream::Stderr)).into_owned(),
        );
        let expected = match test("outcome").and_then(toml_edit::Item::as_str) {
            Some("success") => code == 0,
            Some("failure") => code != 0,
            Some("exit_code") => {
                Some(code) == test("exit_code").and_then(toml_edit::Item::as_integer)
            }
            other => panic!("{name}: outcome {other:?}"),
        };
        if !expected {
            failures.push(format!("{name}: exit code {code}\n{stderr}"));
        }
        for (key, printed) in [("stdout_contains", &stdout), ("stderr_contains", &stderr)] {
            let wanted = test(key).and_then(toml_edit::Item::as_array);
            for text in wanted
                .into_iter()
                .flatten()
                .filter_map(|text| text.as_str())
            {
                // Compiled, the path starts at the project; here, at `src`.
                if !printed.contains(&text.replace("src/", "")) {
                    failures.push(format!("{name}: {key} lacks {text:?}\n{printed}"));
                }
            }
        }
    }
    assert!(ran > 0, "no run fixtures found");
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

/// All that `run` wrote to `stream`.
fn written(run: &compile::Interpreted, stream: Stream) -> Vec<u8> {
    let pieces = run.output.iter().filter(|piece| piece.stream == stream);
    pieces.flat_map(|piece| piece.text.clone()).collect()
}

/// What the program of the one file `text` does when interpreted.
fn interpreted(text: &str) -> compile::Interpreted {
    let (project, program) = load(files(&[("main.peep", text)]));
    let target = Machine::named(TARGET).expect("the target is a machine");
    let (results, interpreted) = interpret(&project, &program, target);
    interpreted.unwrap_or_else(|| {
        let errors: Vec<_> = results.diagnostics().map(|found| &found.message).collect();
        panic!("the program has errors: {errors:?}")
    })
}

#[test]
fn output_keeps_the_order_it_was_written_in() {
    let text = "import \"core:global\";\nfn main() {\n    global::Write(global::Stderr, c\"a\", 1);\n    println(1);\n    println(2);\n    global::Write(global::Stderr, c\"b\", 1);\n}\n";
    let found = run(files(&[("main.peep", text)]));
    let expected = json!([
        { "stream": "stderr", "text": "a" },
        { "stream": "stdout", "text": "1\n2\n" },
        { "stream": "stderr", "text": "b" },
    ]);
    assert_eq!(found["run"]["output"], expected);
}

#[test]
fn a_program_that_keeps_changing_streams_makes_few_pieces_and_keeps_its_text() {
    let run = interpreted(
        "import \"core:global\";\nfn main() {\n    for {\n        global::Write(global::Stderr, c\"a\", 1);\n        global::Write(global::Stdout, c\"b\", 1);\n    }\n}\n",
    );
    assert!(run.output.len() <= 10_001, "{} pieces", run.output.len());
    let bytes: usize = run.output.iter().map(|piece| piece.text.len()).sum();
    assert!(bytes >= 1 << 20, "{bytes} bytes");
    let last = run.output.last().expect("it printed");
    assert!(last.text.starts_with(b"panic: this printed more than"));
}

#[test]
fn a_program_that_prints_forever_stops_with_what_it_printed() {
    let run = interpreted("fn main() {\n    for {\n        println(1);\n    }\n}\n");
    assert_eq!(run.exit_code, 101);
    assert!(
        written(&run, Stream::Stdout).len() <= 1 << 20
            && written(&run, Stream::Stdout).starts_with(b"1\n1\n")
    );
    let stderr = String::from_utf8_lossy(&written(&run, Stream::Stderr)).into_owned();
    assert!(
        stderr.starts_with("panic: this printed more than 1048576 bytes\n  --> main.peep:3:9"),
        "{stderr}"
    );
}

#[test]
fn a_function_outside_peeper_stops_the_run_by_its_name() {
    let run = interpreted(
        "#[extern(\"puts\")]\nfn puts(text: cstr) -> i32;\nfn main() {\n    println(1);\n    puts(c\"a\");\n}\n",
    );
    assert_eq!(
        (run.exit_code, &written(&run, Stream::Stdout)[..]),
        (101, &b"1\n"[..])
    );
    let stderr = String::from_utf8_lossy(&written(&run, Stream::Stderr)).into_owned();
    assert!(
        stderr.starts_with("panic: `puts` is outside Peeper, so it cannot run here\n"),
        "{stderr}"
    );
}

#[test]
fn memory_a_program_gets_wrong_stops_it() {
    let header = "#[extern(\"malloc\")]\nfn malloc(size: usize) -> *i32;\n\
                  #[extern(\"free\")]\nfn release(block: rawptr);\n";
    let twice = format!(
        "{header}fn main() {{\n    let block = malloc(4);\n    release(block);\n    release(block);\n}}\n"
    );
    let stderr = written(&interpreted(&twice), Stream::Stderr);
    assert!(stderr.starts_with(b"panic: this frees memory that is not allocated\n"));

    let huge = format!(
        "{header}fn main() -> i32 {{\n    let block = malloc(4000000000);\n    if block == none {{\n        return 7;\n    }}\n    return 0;\n}}\n"
    );
    assert_eq!(
        interpreted(&huge).exit_code,
        7,
        "no memory gives null, as C does"
    );

    let forged_range = "fn main() {\n    let mut words = [3]u64{21474836480, 0, 0};\n    let forged = @words as *range[i32];\n    let values = [2]i32{forged.*...};\n    println(values[0]);\n}\n";
    let stderr = written(&interpreted(forged_range), Stream::Stderr);
    assert!(
        stderr.starts_with(b"panic: division by zero\n"),
        "bytes a raw pointer gives are not checked as a range made by code"
    );

    let ended = "fn same(p: *i32) -> *i32 {\n    return p;\n}\nfn gives() -> *i32 {\n    let mut n = 5;\n    return same(@n);\n}\nfn main() {\n    println(gives().*);\n}\n";
    assert_eq!(
        written(&interpreted(ended), Stream::Stdout),
        b"5\n",
        "only a constant's run ends memory: a program's reads what is there, as native code does"
    );

    let forged_address = "struct Two { first: i64, second: i64 }\nfn main() {\n    let mut word: u64 = 18446744073709551615;\n    let forged = @word as **Two;\n    println(forged.*.*.second);\n}\n";
    let stderr = written(&interpreted(forged_address), Stream::Stderr);
    assert!(stderr.starts_with(b"panic: this reads or writes memory the program does not own\n"));
}

#[test]
fn recursion_past_the_stack_stops_as_a_panic() {
    let run = interpreted(
        "fn f(n: i32) -> i32 {\n    return f(n + 1) + 1;\n}\nfn main() {\n    f(0);\n}\n",
    );
    assert_eq!(run.exit_code, 101);
    assert!(written(&run, Stream::Stderr).starts_with(b"panic: this went 100000 calls deep\n"));
}

#[test]
fn exit_ends_a_program_with_its_code_and_keeps_its_output() {
    let run = interpreted(
        "import \"core:global\";\nfn main() {\n    global::Write(global::Stderr, c\"bad\\n\", 4);\n    println(1.5, -7i128);\n    global::Exit(3);\n    println(2);\n}\n",
    );
    assert_eq!(run.exit_code, 3);
    assert_eq!(written(&run, Stream::Stdout), b"1.5 -7\n");
    assert_eq!(written(&run, Stream::Stderr), b"bad\n");

    let negative = interpreted("fn main() -> i32 {\n    return -1;\n}\n");
    assert_eq!(
        negative.exit_code, 255,
        "the low 8 bits, as Linux gives them"
    );
}

#[test]
fn printing_a_huge_value_stops_before_its_text_is_built() {
    let run = interpreted(
        "#[extern(\"malloc\")]\nfn malloc(size: usize) -> *[64000000]u8;\nfn main() {\n    let block = malloc(64000000);\n    println(&block.*);\n}\n",
    );
    assert_eq!(run.exit_code, 101);
    assert!(written(&run, Stream::Stdout).is_empty());
    assert!(written(&run, Stream::Stderr).starts_with(b"panic: this printed more than"));
}

#[test]
fn a_size_past_any_block_gives_null_and_a_negative_length_fails() {
    let run = interpreted(
        "#[extern(\"malloc\")]\nfn allocate(size: i64) -> *u8;\n\
         #[extern(\"calloc\")]\nfn cleared(count: i64, size: i64) -> *u8;\n\
         fn main() {\n    let negative: i64 = -1;\n    let huge: i64 = 1;\n    \
         println(allocate(negative) == none, cleared(negative, 1) == none);\n    \
         println(allocate(huge << 62) == none);\n    \
         let length: i32 = -1;\n    \
         println(Write(Stdout, c\"x\", length), Read(Stdin, c\"abc\", length));\n}\n",
    );
    assert_eq!(run.exit_code, 0);
    assert_eq!(written(&run, Stream::Stdout), b"true true\ntrue\n-1 -1\n");
}

#[test]
fn a_run_gives_what_a_program_printed_or_why_it_did_not_start() {
    let ran = run(files(&[(
        "main.peep",
        "fn main() -> i32 {\n    println(40 + 2);\n    return 3;\n}\n",
    )]));
    assert_eq!(ran["diagnostics"], "");
    assert_eq!(
        ran["run"],
        json!({ "output": [{ "stream": "stdout", "text": "42\n" }], "exit_code": 3 })
    );

    let broken = run(files(&[("main.peep", "fn main() {\n    missing();\n}\n")]));
    assert_eq!(broken["run"], Value::Null);
    let printed = broken["diagnostics"]
        .as_str()
        .expect("diagnostics are text");
    assert!(printed.contains("missing"), "{printed}");

    // No `main`, and then no entry file at all.
    let no_main = run(files(&[("main.peep", "fn other() {}\n")]));
    assert_eq!(no_main["run"], Value::Null);
    let printed = no_main["diagnostics"]
        .as_str()
        .expect("diagnostics are text");
    assert!(printed.contains("[M0006]"), "{printed}");
    let no_file = run(files(&[("other.peep", "fn other() {}\n")]));
    assert_eq!(no_file["run"], Value::Null);
    let printed = no_file["diagnostics"]
        .as_str()
        .expect("diagnostics are text");
    assert!(printed.contains("Compilation failed"), "{printed}");

    // A panic is a run that ended, not a diagnostic.
    let panicked = run(files(&[(
        "main.peep",
        "fn main() {\n    panic \"stop\";\n}\n",
    )]));
    assert_eq!(panicked["diagnostics"], "");
    assert_eq!(panicked["run"]["exit_code"], 101);
    assert_eq!(
        panicked["run"]["output"],
        json!([{ "stream": "stderr", "text": "panic: stop\n  --> main.peep:2:5\n" }])
    );
}
