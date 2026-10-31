use std::path::Path;

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
