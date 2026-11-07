use std::collections::HashMap;
use std::path::PathBuf;

use super::CheckedFunction;
use super::ModuleResults;
use super::Output;
use super::Results;
use super::run;
use diagnostics::Diagnostic;
use model::machine::Machine;

/// The machine the tests check for, but where they test another.
fn x86_64() -> &'static Machine {
    Machine::named("x86_64-linux").unwrap()
}

/// The program of `modules`, each an import path and its text, loaded as
/// `peeper check` loads it from `app:main`, with every file given as open
/// editor text so nothing is read from disk. Loading must find no error.
fn load(modules: &[(&str, &str)]) -> model::program::Program {
    let folder = |package: &str| match package {
        "core" => PathBuf::from("/peeper-build-test/libs/core/src"),
        _ => PathBuf::from("/peeper-build-test/src"),
    };
    let packages = HashMap::from([
        ("app".to_string(), folder("app")),
        ("core".to_string(), folder("core")),
    ]);
    let open_files = modules
        .iter()
        .map(|&(path, text)| {
            let (package, file) = path.split_once(':').unwrap();
            (
                folder(package).join(format!("{file}.peep")),
                text.to_string(),
            )
        })
        .collect();
    let project = project::Project {
        packages,
        entry: Some(folder("app").join("main.peep")),
        root: PathBuf::from("/peeper-build-test"),
        name: "app".to_string(),
    };
    let program = project::load(&project, &open_files);
    let reported: Vec<&str> = program
        .diagnostics
        .iter()
        .map(|diagnostic| diagnostic.message.as_str())
        .collect();
    assert!(program.checkable, "{reported:?}");
    program
}

/// Checks the program of `modules` (see `load`) and gives each
/// diagnostic as its code, message and the text of its first label.
fn diagnostics(modules: &[(&str, &str)]) -> Vec<(&'static str, String, String)> {
    let found = run(
        &load(modules),
        x86_64(),
        Results::default(),
        false,
        false,
        |_| {},
    );
    found
        .diagnostics()
        .map(|diagnostic| {
            let label = &diagnostic.labels[0];
            let text = label.file.slice(label.span).to_string();
            (diagnostic.code.id, diagnostic.message.clone(), text)
        })
        .collect()
}

fn expected(expected: &[(&'static str, &str, &str)]) -> Vec<(&'static str, String, String)> {
    let owned = |&(code, message, text): &(&'static str, &str, &str)| {
        (code, message.to_string(), text.to_string())
    };
    expected.iter().map(owned).collect()
}

#[test]
fn calls() {
    let found = diagnostics(&[(
        "app:main",
        "fn main() {
                let sum: i64 = add(1, 2u8);
                let wrong = add(1);
                let extra = add(1, 2, true);
                let mismatched = add(true, 2);
                let nothing = main();
                let local = 1;
                local();
                Point(1);
                add(missing, 2);
            }
            fn add(a: i64, b: i16) -> i64 { return a + b; }
            struct Point { x: i32 }",
    )]);
    assert_eq!(
        found,
        expected(&[
            (
                "T0006",
                "`add` takes 2 arguments, but 1 was given",
                "add(1)"
            ),
            (
                "T0006",
                "`add` takes 2 arguments, but 3 were given",
                "add(1, 2, true)"
            ),
            (
                "T0001",
                "mismatched types: expected `i64`, found `bool`",
                "true"
            ),
            ("T0004", "this expression has no value to bind", "main()"),
            ("T0005", "`local` is not a function", "local"),
            ("T0005", "`Point` is not a function", "Point"),
            ("T0002", "unknown name `missing`", "missing"),
        ])
    );
}

#[test]
fn other_modules() {
    let found = diagnostics(&[
        (
            "app:utils",
            "fn Add(a: i32, b: i32) -> i32 { return a + b; }
                fn hidden() {}
                const Limit: u8 = 200;",
        ),
        (
            "app:main",
            "import \"app:utils\";
                fn main() {
                    let sum = utils::Add(1, 2);
                    let wide: i16 = utils::Limit;
                    utils::hidden();
                    utils::Missing();
                    let bad: u8 = utils::Add(1, 2);
                    utils();
                }",
        ),
    ]);
    assert_eq!(
        found,
        expected(&[
            ("M0004", "`hidden` is not exported from `utils`", "hidden"),
            (
                "T0002",
                "unknown name `Missing` in module `utils`",
                "Missing"
            ),
            (
                "T0001",
                "mismatched types: expected `u8`, found `i32`",
                "utils::Add(1, 2)"
            ),
            ("T0005", "`utils` is not a function", "utils"),
        ])
    );
}

#[test]
fn module_constants() {
    let found = diagnostics(&[(
        "app:main",
        "const Three = One + 2;
            const One = 1;
            const Wide: i64 = Three;
            const Narrow: u8 = Three;
            const Zero = Three - 3;
            fn main() {
                let a = 10 / Zero;
                let b: i64 = Wide;
                let c = Missing;
            }",
    )]);
    assert_eq!(
        found,
        expected(&[
            (
                "T0001",
                "mismatched types: expected `u8`, found `i32`",
                "Three"
            ),
            ("T0004", "division by zero", "Zero"),
            ("T0002", "unknown name `Missing`", "Missing"),
        ])
    );
}

/// A constant's `false && x` and `true || x` do not run `x`, as the
/// program would not; `x` is still checked.
#[test]
fn constants_short_circuit() {
    let found = diagnostics(&[(
        "app:main",
        "fn bad() -> bool { panic \"bad\"; }
            const Skipped = false && bad() || true || bad();
            const Checked = false && 1;
            const Run = true && bad();
            fn main() {}",
    )]);
    assert_eq!(
        found,
        expected(&[
            (
                "T0001",
                "mismatched types: expected `bool`, found `i32`",
                "1"
            ),
            (
                "T0044",
                "this call cannot be evaluated at compile time",
                "bad()"
            ),
        ])
    );
}

/// A long chain of constants, each naming the next, is checked, not a
/// stack overflow.
#[test]
fn checks_a_long_chain_of_constants() {
    const DEPTH: usize = 5000;
    let text: String = (0..DEPTH)
        .map(|i| format!("const C{i} = C{};\n", i + 1))
        .chain([format!("const C{DEPTH} = 1;\nfn main() {{}}")])
        .collect();
    assert_eq!(diagnostics(&[("app:main", &text)]), []);
}

/// An expression nested far deeper than a thread's stack would hold is
/// checked and lowered: each walk moves to new stack segments as it needs
/// them, as the parser does.
#[test]
fn checks_deeply_nested_expressions() {
    const DEPTH: usize = 50_000;
    let operators = format!("x{}", " + x".repeat(DEPTH));
    let calls = format!("{}x{}", "id(".repeat(DEPTH), ")".repeat(DEPTH));
    let parentheses = format!("{}x{}", "(".repeat(DEPTH), " + 1)".repeat(DEPTH));
    let negations = format!("{}true", "! ".repeat(DEPTH));
    let text = format!(
        "fn id(n: i32) -> i32 {{ return n; }}
fn main() {{
    let x = 1;
    let a = {operators};
    let b = {calls};
    let c = {parentheses};
    let d = {negations};
}}"
    );
    assert_eq!(diagnostics(&[("app:main", &text)]), []);
}

/// Statements and types nested far deeper than a thread's stack would hold,
/// as the parser takes them, are checked and lowered: blocks in blocks, a
/// long `else if` chain, a long `||` chain in a condition, and references
/// and arrays written in themselves.
#[test]
fn checks_deeply_nested_statements() {
    const DEPTH: usize = 50_000;
    let blocks = format!("{}x = x + 1;{}", "{ ".repeat(DEPTH), " }".repeat(DEPTH));
    let branches = format!(
        "if x == 0 {{ }}{}",
        " else if x == 1 { x = 2; }".repeat(DEPTH)
    );
    let conditions = format!("if x == 0{} {{ x = 3; }}", " || x == 1".repeat(DEPTH));
    let text = format!(
        "fn main() {{
    let mut x = 1;
    {blocks}
    {branches}
    {conditions}
}}"
    );
    assert_eq!(diagnostics(&[("app:main", &text)]), []);
    // References to references are not supported yet, so the one error.
    let types = format!("fn f(a: {}i32) {{}}\nfn main() {{}}", "& ".repeat(DEPTH));
    let found = diagnostics(&[("app:main", &types)]);
    let codes: Vec<&str> = found.iter().map(|&(code, ..)| code).collect();
    assert_eq!(codes, ["T0043"], "{found:?}");
    let arrays = format!(
        "fn f(a: {}i32) {{ println(a); }}\nfn main() {{}}",
        "[1]".repeat(DEPTH)
    );
    assert_eq!(diagnostics(&[("app:main", &arrays)]), []);
    // A pointer breaks a flat array, so an error's type text recurses.
    let pointers = format!(
        "fn f(a: {}i32) {{ let b: bool = a; }}\nfn main() {{}}",
        "[1]*".repeat(DEPTH)
    );
    let found = diagnostics(&[("app:main", &pointers)]);
    let codes: Vec<&str> = found.iter().map(|&(code, ..)| code).collect();
    assert_eq!(codes, ["T0001"], "{found:?}");
    let plain = format!(
        "fn f(a: {}i32) {{ let b: bool = a; }}\nfn main() {{}}",
        "*".repeat(DEPTH)
    );
    let found = diagnostics(&[("app:main", &plain)]);
    let codes: Vec<&str> = found.iter().map(|&(code, ..)| code).collect();
    assert_eq!(codes, ["T0001"], "{found:?}");
    // Structs in structs: `layout` once walked all of them for each one.
    let chain: String = (1..DEPTH)
        .map(|i| format!("struct S{i} {{ s: S{} }}\n", i - 1))
        .collect();
    let structs = format!("struct S0 {{ n: i32 }}\n{chain}fn main() {{}}");
    assert_eq!(diagnostics(&[("app:main", &structs)]), []);
}

/// Each struct holds the last twice, so a walk of the fields, not of the
/// structs, takes 2^40 steps. This is a short program.
#[test]
fn checks_structs_holding_structs_twice() {
    let chain: String = (1..40)
        .map(|i| format!("struct S{i} {{ a: S{0}, b: S{0} }}\n", i - 1))
        .collect();
    let text = format!(
        "struct S0 {{ n: i32 }}\n{chain}fn show(s: &S39) {{ println(s.*); }}
        fn same(s: S39) -> S39 {{ return s; }}
        fn replace(to: &mut S39, s: S39) {{ to.* = s; }}
        fn borrowing(s: S39) -> S39 {{ let n = 1; let r = &n; println(r); return s; }}"
    );
    assert_eq!(diagnostics(&[("app:main", &text)]), []);
}

/// Each struct holds the last twice, and the first a reference, so the loan
/// check would keep 2^n of them apart. `MOST_HOLDERS` is 2^12: one more level
/// is refused, once, and those that hold it are not reported again.
#[test]
fn structs_hold_a_limited_number_of_references() {
    let chain = |levels: usize| -> String {
        (1..levels)
            .map(|i| format!("struct S{i} {{ a: S{0}, b: S{0} }}\n", i - 1))
            .collect()
    };
    let last = |levels: usize| {
        let chain = chain(levels);
        format!(
            "struct S0 {{ to: &i32 }}\n{chain}fn f(s: S{0}) -> S{0} {{ return s; }}",
            levels - 1
        )
    };
    assert_eq!(diagnostics(&[("app:main", &last(13))]), []);
    let found = diagnostics(&[("app:main", &last(40))]);
    let codes: Vec<&str> = found.iter().map(|&(code, ..)| code).collect();
    assert_eq!(codes, ["P0011"], "{found:?}");
}

/// No slice is longer than the largest `usize`, `u32::MAX` on a 32-bit
/// target: a constant end past it is outside every slice, though it
/// fits a `u64`, which lowering would cut to the target's width.
#[test]
fn slice_ends_fit_the_target_usize() {
    let program = load(&[(
        "app:main",
        "fn f(s: &[]i32) {
                let fits = &s[4294967295u64..];
                let past = &s[4294967296u64..];
            }
            fn main() {}",
    )]);
    let found = run(
        &program,
        Machine::named("i686-linux").unwrap(),
        Results::default(),
        false,
        false,
        |_| {},
    );
    let messages: Vec<&str> = (found.diagnostics())
        .map(|diagnostic| diagnostic.message.as_str())
        .collect();
    assert_eq!(
        messages,
        ["slice `4294967296u64..` is outside every `&[]i32`"]
    );
}

/// Asked for `main`, a program with no other error needs in its entry file a
/// `fn main()` or `fn main() -> i32` with a body, and says whether it gives
/// the exit code; an error elsewhere is reported alone.
#[test]
fn the_entry_module_needs_a_main_to_start() {
    let entry_point = |text: &str| {
        let program = load(&[("app:main", text)]);
        let found = run(&program, x86_64(), Results::default(), true, false, |_| {});
        let codes: Vec<&str> = found.diagnostics().map(|d| d.code.id).collect();
        (found.entry_point().map(|(_, exit_code)| exit_code), codes)
    };
    assert_eq!(entry_point("fn main() {}"), (Some(false), vec![]));
    assert_eq!(
        entry_point("fn main() -> i32 { return 0; }"),
        (Some(true), vec![])
    );
    for text in [
        "fn helper() {}",
        "fn main(x: i32) {}",
        "fn main() -> i64 { return 0; }",
        "#[extern(\"main\")]\nfn main();",
        "struct App { x: i32 }\nfn App::main() {}",
    ] {
        assert_eq!(entry_point(text), (None, vec!["M0006"]), "{text}");
    }
    let (found, codes) = entry_point("fn helper() { let x: i32 = true; }");
    assert!(found.is_none() && !codes.is_empty() && !codes.contains(&"M0006"));
}

/// A syntax error the parser read past is the loader's, not the checker's,
/// and still leaves the program with no entry point.
#[test]
fn a_loading_error_leaves_no_entry_point() {
    let program = load(&[("app:main", "fn main() {\n    let x: i32 = 1\n}")]);
    assert!(program.diagnostics.iter().any(Diagnostic::is_error));
    let found = run(&program, x86_64(), Results::default(), true, false, |_| {});
    assert_eq!(found.diagnostics().count(), 0, "the checker found nothing");
    assert!(found.entry_point().is_none());
}

/// The MIR `run` gives is what the path checks read, with the `scope end`s a
/// compiled body has lost (`flow::cleanup`): each way out of a block ends the
/// variables it leaves, newest first.
#[test]
fn each_way_out_of_a_block_ends_its_variables_newest_first() {
    let program = load(&[(
        "app:main",
        "fn walk(limit: i32) -> i32 {
            let first: i32 = limit;
            for first > 0 {
                let second: i32 = first;
                let third: i32 = second;
                if third == 1 {
                    break;
                }
                if third == 2 {
                    continue;
                }
                if third == 3 {
                    return third;
                }
            }
            return first;
        }
        fn main() {}",
    )]);
    let mut text = String::new();
    let results = run(
        &program,
        x86_64(),
        Results::default(),
        false,
        false,
        |output| {
            if let Output::Function(CheckedFunction {
                function,
                mir: Some(mir),
                types,
                ..
            }) = output
            {
                let file = &program.modules[function.definition.module.index()].ast.file;
                text.push_str(&mir.text(types, file, |_| ""));
            }
        },
    );
    assert_eq!(messages(&results), Vec::<String>::new());
    let ends: Vec<&str> = (text.lines())
        .map(str::trim)
        .filter(|line| line.starts_with("scope end"))
        .collect();
    let (outer, inner) = (
        ["scope end first.1", "scope end limit.0"],
        ["scope end third.3", "scope end second.2"],
    );
    let expected = [
        &outer[..], // after the loop, which `break` jumps to
        &inner,     // at the `break`
        &inner,     // at the `continue`
        &inner,     // at the `return`, with the rest
        &outer,
        &inner, // at the loop's `}`
        &outer, // at the function's `}`
    ];
    assert_eq!(ends, expected.concat());
}

/// A cycle gets one error with a place for each step, however often it
/// is closed (`A + A` closes it twice); a figure 8 is two cycles.
#[test]
fn a_cycle_is_reported_once_with_its_path() {
    let program = load(&[(
        "app:main",
        "const A = A + A;
            const B = C + D;
            const C = B;
            const D = B;
            struct Point { x: i32 }
            const E = Point::E();
            fn Point::E() -> i32 { return E; }
            fn main() { let x = A + B + E; }",
    )]);
    let found = run(&program, x86_64(), Results::default(), false, false, |_| {});
    let errors: Vec<(String, Vec<(String, String)>)> = found
        .diagnostics()
        .map(|diagnostic| {
            let labels = diagnostic.labels.iter();
            let label = |label: &diagnostics::Label| {
                (
                    label.file.slice(label.span).to_string(),
                    label.message.clone(),
                )
            };
            (diagnostic.message.clone(), labels.map(label).collect())
        })
        .collect();
    let error = |message: &str, labels: &[(&str, &str)]| {
        let labels = labels
            .iter()
            .map(|&(text, label)| (text.to_string(), label.to_string()));
        (message.to_string(), labels.collect::<Vec<_>>())
    };
    assert_eq!(
        errors,
        [
            error(
                "`A` depends on itself",
                &[
                    ("A", "`A` is defined here"),
                    ("A", "`A` uses `A`, closing the cycle"),
                ]
            ),
            error(
                "`B` depends on itself",
                &[
                    ("B", "`B` is defined here"),
                    ("C", "`B` uses `C`"),
                    ("B", "`C` uses `B`, closing the cycle"),
                ]
            ),
            error(
                "`B` depends on itself",
                &[
                    ("B", "`B` is defined here"),
                    ("D", "`B` uses `D`"),
                    ("B", "`D` uses `B`, closing the cycle"),
                ]
            ),
            // A type's function is named with its type.
            error(
                "`E` depends on itself",
                &[
                    ("E", "`E` is defined here"),
                    ("Point::E()", "`E` uses `Point::E`"),
                    ("E", "`Point::E` uses `E`, closing the cycle"),
                ]
            ),
        ]
    );
}

#[test]
fn module_constants_cannot_be_assigned() {
    let modules = [
        ("core:global", "const Stdout: i32 = 1;"),
        (
            "app:main",
            "const Limit: i32 = 1;\nfn main() { Limit = 2; Stdout = 3; main = 4; }",
        ),
    ];
    // Each constant is shown where it is declared, in its own module.
    let found = run(
        &load(&modules),
        x86_64(),
        Results::default(),
        false,
        false,
        |_| {},
    );
    let declared: Vec<(&str, &str)> = found
        .diagnostics()
        .filter_map(|diagnostic| diagnostic.labels.get(1))
        .map(|label| (label.file.slice(label.span), label.message.as_str()))
        .collect();
    assert_eq!(
        declared,
        [
            ("Limit", "declared as a constant here"),
            ("Stdout", "declared as a constant here"),
        ]
    );
    let found = diagnostics(&modules);
    assert_eq!(
        found,
        expected(&[
            ("T0018", "cannot assign to constant `Limit`", "Limit"),
            ("T0018", "cannot assign to constant `Stdout`", "Stdout"),
            (
                "T0007",
                "cannot assign to `main`, which is not a variable",
                "main"
            ),
        ])
    );
}

/// The prelude's exported names are in every module's scope; its private
/// names are not, unqualified or through an import.
#[test]
fn the_prelude_exports_only_upper_case_names() {
    let found = diagnostics(&[
            (
                "core:global",
                "const Stdout: i32 = 1;
                const hidden: i32 = 2;
                #[extern(\"peeper_rt_v1_exit\")]
                fn Exit(code: i32);",
            ),
            (
                "app:main",
                "import \"core:global\";
                fn main() { Exit(Stdout); global::Exit(global::Stdout); let Exit = hidden; global::hidden; }",
            ),
        ]);
    assert_eq!(
        found,
        expected(&[
            ("T0002", "unknown name `hidden`", "hidden"),
            ("M0004", "`hidden` is not exported from `global`", "hidden"),
        ])
    );
}

/// A struct declared in another module, here the prelude, has the field
/// names of its own module's tree.
#[test]
fn a_prelude_struct_has_its_own_fields() {
    let found = diagnostics(&[
        (
            "core:global",
            "const Stdout: i32 = 1;\nstruct Pair { first: i32, second: i64 }",
        ),
        (
            "app:main",
            "fn main() {
                    let pair = Pair.{ second = 2, first = 1 };
                    let second: i64 = pair.second;
                    let wrong = pair.secnd;
                }",
        ),
    ]);
    assert_eq!(
        found,
        expected(&[("T0010", "`Pair` has no field `secnd`", "secnd")])
    );
}

/// A struct with a field of no type, and a function with a malformed
/// `extern`, are invalid, so no function using them is lowered for a
/// backend to lay out or name.
#[test]
fn an_invalid_declaration_lowers_nothing_using_it() {
    let program = load(&[(
        "app:main",
        "struct S { x: Missing }
            fn f(s: S) {}
            #[extern(1)]
            fn Puts(code: i32) -> i32;
            fn g() -> i32 { return Puts(1); }
            fn main() {}",
    )]);
    let mut lowered = 0;
    let found = run(
        &program,
        x86_64(),
        Results::default(),
        false,
        false,
        |output| {
            if let Output::Function(CheckedFunction { mir: Some(_), .. }) = output {
                lowered += 1;
            }
        },
    );
    let extern_shape = "write `#[extern(\"name\")]` once, with the C function's name";
    assert_eq!(messages(&found), ["unknown type `Missing`", extern_shape]);
    assert_eq!(lowered, 1, "only `main` is lowered");
}

/// Compiles the program of `modules` (see `load`) after `previous`;
/// gives what it found and the paths of the modules whose bodies were
/// checked, which every module of these tests has.
fn recompile(modules: &[(&str, &str)], previous: Results) -> (Results, Vec<String>) {
    let loaded = load(modules);
    let mut checked: Vec<String> = Vec::new();
    let results = run(&loaded, x86_64(), previous, false, false, |output| {
        let Output::Function(CheckedFunction { module, .. }) = output else {
            return;
        };
        let path = &loaded.modules[module.index()].path;
        if !checked.contains(path) {
            checked.push(path.clone());
        }
    });
    (results, checked)
}

/// The message of each diagnostic of `results`, in order.
fn messages(results: &Results) -> Vec<String> {
    let message = |diagnostic: &diagnostics::Diagnostic| diagnostic.message.clone();
    results.diagnostics().map(message).collect()
}

const UTILS: (&str, &str) = (
    "app:utils",
    "fn Add(a: i32, b: i32) -> i32 { return a + b; }",
);
const OTHER: (&str, &str) = ("app:other", "fn Other() {}");
const MAIN: (&str, &str) = (
    "app:main",
    "import \"app:utils\";
        import \"app:other\";
        fn main() { let sum = utils::Add(1, 2); }
        fn wrong() { let flag: bool = 1; }",
);

#[test]
fn an_unchanged_program_is_not_checked_again() {
    let (first, checked) = recompile(&[UTILS, OTHER, MAIN], Results::default());
    assert_eq!(checked, ["app:other", "app:utils", "app:main"]);
    let found = messages(&first);
    assert_eq!(found, ["mismatched types: expected `bool`, found `i32`"]);
    let (second, checked) = recompile(&[UTILS, OTHER, MAIN], first);
    assert!(checked.is_empty(), "{checked:?}");
    assert_eq!(messages(&second), found);
}

#[test]
fn a_body_edit_checks_only_its_module() {
    let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
    let utils = (
        "app:utils",
        "fn Add(a: i32, b: i32) -> i32 { return b + a; }",
    );
    let (second, checked) = recompile(&[utils, OTHER, MAIN], first);
    assert_eq!(checked, ["app:utils"]);
    let reused: Vec<&str> = second.reused().map(|file| file.path.as_str()).collect();
    assert_eq!(
        reused,
        [
            "/peeper-build-test/src/other.peep",
            "/peeper-build-test/src/main.peep"
        ]
    );
}

#[test]
fn a_signature_edit_checks_its_importers() {
    let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
    let utils = (
        "app:utils",
        "fn Add(a: i64, b: i64) -> i64 { return a + b; }",
    );
    let (_, checked) = recompile(&[utils, OTHER, MAIN], first);
    assert_eq!(checked, ["app:utils", "app:main"]);
}

/// A type's function moved to another type is a new name for its importers.
#[test]
fn a_function_moved_to_another_type_checks_its_importers() {
    let utils = |owner: &str| {
        format!("struct Point {{ x: i32 }}\nstruct Line {{ x: i32 }}\nfn {owner}::Make() {{}}")
    };
    let (point, line) = (utils("Point"), utils("Line"));
    let (first, _) = recompile(&[("app:utils", &point), OTHER, MAIN], Results::default());
    let (_, checked) = recompile(&[("app:utils", &line), OTHER, MAIN], first);
    assert_eq!(checked, ["app:utils", "app:main"]);
}

#[test]
fn a_struct_field_edit_checks_its_importers() {
    let utils = |point: &str| format!("fn Add(a: i32, b: i32) -> i32 {{ return a + b; }}\n{point}");
    let (start, retyped, renamed) = (
        utils("struct Point { x: i32 }"),
        utils("struct Point { x: i64 }"),
        utils("struct Point { y: i64 }"),
    );
    let (first, _) = recompile(&[("app:utils", &start), OTHER, MAIN], Results::default());
    let (second, checked) = recompile(&[("app:utils", &retyped), OTHER, MAIN], first);
    assert_eq!(checked, ["app:utils", "app:main"], "a field's type");
    let (_, checked) = recompile(&[("app:utils", &renamed), OTHER, MAIN], second);
    assert_eq!(checked, ["app:utils", "app:main"], "a field's name");
}

/// `main` names `utils` nowhere, but holds its struct through `other`'s, so
/// a field's edit reaches it; an edit of a body does not.
#[test]
fn a_struct_field_edit_reaches_importers_through_a_struct() {
    let utils =
        |field: &str, body: &str| format!("struct Item {{ {field} }}\nfn Used() {{ {body} }}");
    let (start, retyped, rebuilt) = (
        utils("x: i32", ""),
        utils("x: i64", ""),
        utils("x: i64", "let a = 1;"),
    );
    let other = (
        "app:other",
        "import \"app:utils\";
        struct Holder { item: utils::Item }
        fn Other() {}",
    );
    let main = (
        "app:main",
        "import \"app:other\";
        fn main(held: other::Holder) {}",
    );
    let (first, _) = recompile(&[("app:utils", &start), other, main], Results::default());
    let (second, checked) = recompile(&[("app:utils", &retyped), other, main], first);
    assert_eq!(checked, ["app:utils", "app:other", "app:main"]);
    let (_, checked) = recompile(&[("app:utils", &rebuilt), other, main], second);
    assert_eq!(checked, ["app:utils"]);
}

/// `main` reads a field of a struct it never names, which points to itself,
/// through the pointer `other`'s function gives, so a field's edit reaches
/// it: a new type, and a new name, which is an error in `main`.
#[test]
fn a_struct_field_edit_reaches_importers_through_a_pointer() {
    let utils = |field: &str| format!("struct Item {{ next: *Item, {field} }}\nfn Used() {{}}");
    let (start, retyped, renamed) = (utils("x: i32"), utils("x: i64"), utils("y: i64"));
    let other = (
        "app:other",
        "import \"app:utils\";
        fn First() -> *utils::Item { return none; }",
    );
    let main = (
        "app:main",
        "import \"app:other\";
        fn main() { println(other::First().*.x); }",
    );
    let (first, _) = recompile(&[("app:utils", &start), other, main], Results::default());
    let (second, checked) = recompile(&[("app:utils", &retyped), other, main], first);
    assert_eq!(checked, ["app:utils", "app:other", "app:main"]);
    let (third, checked) = recompile(&[("app:utils", &renamed), other, main], second);
    assert_eq!(checked, ["app:utils", "app:other", "app:main"]);
    assert_eq!(messages(&third), ["`Item` has no field `x`"]);
}

#[test]
fn errors_come_and_go_with_edits() {
    let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
    let broken = (
        "app:utils",
        "fn Add(a: i32, b: i32) -> i32 { let c: bool = a; return a + b; }",
    );
    // `utils` is checked and `Add` has an error; its interface is
    // unchanged, so `main` is reused and keeps its own error.
    let (second, checked) = recompile(&[broken, OTHER, MAIN], first);
    assert_eq!(checked, ["app:utils"]);
    let mismatch = |found: &str| format!("mismatched types: expected `bool`, found `{found}`");
    assert_eq!(messages(&second), [mismatch("i32"), mismatch("i32")]);
    let (third, checked) = recompile(&[UTILS, OTHER, MAIN], second);
    assert_eq!(checked, ["app:utils"]);
    assert_eq!(messages(&third), [mismatch("i32")]);
}

/// A constant's value from a call depends on the called body, which no
/// interface holds: an edit of it checks the constant's module again.
#[test]
fn a_body_edit_checks_the_modules_whose_constants_ran_it() {
    let main = (
        "app:main",
        "import \"app:utils\";
            const DIVISOR = utils::Divisor();
            fn main() { let half = 1 / DIVISOR; }",
    );
    let zero = ("app:utils", "fn Divisor() -> i32 { return 0; }");
    let (first, _) = recompile(&[zero, main], Results::default());
    assert_eq!(messages(&first), ["division by zero"]);
    let two = ("app:utils", "fn Divisor() -> i32 { return 2; }");
    let (second, checked) = recompile(&[two, main], first);
    assert_eq!(checked, ["app:utils", "app:main"]);
    assert!(messages(&second).is_empty(), "{:?}", messages(&second));
}

/// The same through a function of another module that the call reaches.
#[test]
fn a_body_edit_checks_constants_that_reach_it_through_others() {
    let utils = (
        "app:utils",
        "import \"app:other\";\nfn Divisor() -> i32 { return other::Value(); }",
    );
    let main = (
        "app:main",
        "import \"app:utils\";
            const DIVISOR = utils::Divisor();
            fn main() { let half = 1 / DIVISOR; }",
    );
    let zero = ("app:other", "fn Value() -> i32 { return 0; }");
    let (first, _) = recompile(&[zero, utils, main], Results::default());
    assert_eq!(messages(&first), ["division by zero"]);
    let two = ("app:other", "fn Value() -> i32 { return 2; }");
    let (second, checked) = recompile(&[two, utils, main], first);
    assert_eq!(checked, ["app:other", "app:main"]);
    assert!(messages(&second).is_empty(), "{:?}", messages(&second));
}

/// A length is any number, so it must not read as the kind of the type after
/// it: one of these lengths is `Type::Range`'s place in the enum, which
/// `[2][length]i32` and `[2]range[i32]` would then share.
#[test]
fn an_array_length_does_not_read_as_a_type() {
    let main = ("app:main", "import \"app:utils\";\nfn main() { }");
    let range = ("app:utils", "fn Take(a: [2]range[i32]) { }");
    for length in 0..32 {
        let lengths = format!("fn Take(a: [2][{length}]i32) {{ }}");
        let (first, _) = recompile(&[("app:utils", &lengths), main], Results::default());
        let (second, checked) = recompile(&[range, main], first);
        assert_eq!(checked, ["app:utils", "app:main"], "{length}");
        assert!(messages(&second).is_empty(), "{:?}", messages(&second));
    }
}

#[test]
fn a_reused_module_reports_its_errors_once() {
    let utils = (
        "app:utils",
        "fn Bad(x: Missing) {}
            fn Good() {}",
    );
    let main = (
        "app:main",
        "import \"app:utils\";\nfn main() { utils::Bad(1); }\nfn clean() {}",
    );
    let (first, _) = recompile(&[utils, main], Results::default());
    let found = messages(&first);
    assert_eq!(found, ["unknown type `Missing`"]);
    let edited = (
        "app:main",
        "import \"app:utils\";\nfn main() { utils::Bad(2); }\nfn clean() {}",
    );
    let (second, checked) = recompile(&[utils, edited], first);
    assert_eq!(checked, ["app:main"]);
    assert_eq!(messages(&second), found);
}

/// A function run at compile time and then compiled records its facts
/// once, each with the function that explains its locals; a declaration
/// first checked while such a function is built (`inner`'s signature)
/// records its facts too.
#[test]
fn a_function_run_at_compile_time_records_its_facts_once() {
    let program = load(&[(
        "app:main",
        "const BIG = twice(2);
            fn twice(n: i32) -> i32 { let doubled = inner(n) * 2; return doubled; }
            fn inner(n: i32) -> i32 { return n; }",
    )]);
    let tree = &program.modules[0].ast;
    let mut found: Vec<(&str, &str)> = Vec::new();
    run(
        &program,
        x86_64(),
        Results::default(),
        false,
        true,
        |output| {
            let (owner, facts) = match output {
                Output::Function(CheckedFunction {
                    function, facts, ..
                }) => {
                    let declaration = &tree.decls[tree.declaration(function.definition.index)];
                    (declaration.name().text(&tree.file), facts)
                }
                Output::Facts { facts, .. } => ("module", facts),
                Output::Constant { .. } => return,
            };
            found.extend(facts.iter().map(|fact| (tree.file.slice(fact.span), owner)));
        },
    );
    // `twice` gets the facts found until it is handed on: `BIG`'s value,
    // both signatures, its body; `inner` its body.
    let expected = [
        ("twice", "twice"),
        ("i32", "twice"),
        ("i32", "twice"),
        ("2", "twice"),
        ("i32", "twice"),
        ("i32", "twice"),
        ("n", "twice"),
        ("inner", "twice"),
        ("n", "twice"),
        ("2", "twice"),
        ("doubled", "twice"),
        ("doubled", "twice"),
        ("n", "inner"),
        ("n", "inner"),
    ];
    assert_eq!(found, expected);
}

/// An array's length must fit the target's `usize`, which its `len` is:
/// 2^32 does on a 64-bit target, not on a 32-bit one, where 2^32 - 1
/// still does.
#[test]
fn array_lengths_fit_the_target_usize() {
    let program = load(&[(
        "app:main",
        // Of values that take no bytes, so only the length is too big.
        "struct Empty {}
            fn main() { let a: [4294967296]Empty; let b: [4294967295]Empty; }",
    )]);
    let at = |name| {
        let target = Machine::named(name).unwrap();
        messages(&run(
            &program,
            target,
            Results::default(),
            false,
            false,
            |_| {},
        ))
    };
    assert_eq!(
        at("i686-linux"),
        ["an array length must fit `usize`, of 32 bits"]
    );
    assert!(at("x86_64-linux").is_empty(), "{:?}", at("x86_64-linux"));
}

/// A value must take at most `isize::MAX` bytes of the target, as LLVM
/// counts the bytes of a copy as signed.
#[test]
fn values_fit_the_target() {
    let program = load(&[(
        "app:main",
        "struct Halves { a: [1073741824]u8, b: [1073741824]u8 }
            fn main() { let fits: [2147483647]u8; let over: [2147483648]u8; }",
    )]);
    let at = |name| {
        let target = Machine::named(name).unwrap();
        messages(&run(
            &program,
            target,
            Results::default(),
            false,
            false,
            |_| {},
        ))
    };
    let too_big = |text| {
        format!("`{text}` is too big for i686-linux, where a value takes at most 2147483647 bytes")
    };
    assert_eq!(
        at("i686-linux"),
        [too_big("Halves"), too_big("[2147483648]u8")]
    );
    assert!(at("x86_64-linux").is_empty(), "{:?}", at("x86_64-linux"));
}

/// An `extern` integer must be one C has on the target: at most 128 bits
/// on AArch64.
#[test]
fn extern_integers_are_ones_c_has() {
    let program = load(&[(
        "app:main",
        "#[extern(\"c_wide\")]\nfn wide(a: i128, b: i129) -> i200;",
    )]);
    let at = |name| {
        let target = Machine::named(name).unwrap();
        messages(&run(
            &program,
            target,
            Results::default(),
            false,
            false,
            |_| {},
        ))
    };
    let none =
        |text| format!("C has no `{text}` on aarch64-linux, whose integers have at most 128 bits");
    assert_eq!(at("aarch64-linux"), [none("i129"), none("i200")]);
    assert!(at("x86_64-linux").is_empty(), "{:?}", at("x86_64-linux"));
}

/// A diagnostic may point into another module's file, here where the type
/// a local is named after is declared; moving that declaration changes no
/// interface, but the importer is checked again, so the label is where the
/// type now is.
#[test]
fn a_label_in_another_file_follows_its_edit() {
    let main = ("app:main", "fn main() { let Point = 1; }");
    let prelude = ("core:global", "struct Point {}");
    let (first, _) = recompile(&[prelude, main], Results::default());
    let moved = ("core:global", "// moved down\nstruct Point {}");
    let (second, checked) = recompile(&[moved, main], first);
    assert_eq!(checked, ["app:main"]);
    let labels: Vec<(String, u32)> = (second.diagnostics())
        .flat_map(|diagnostic| &diagnostic.labels)
        .map(|label| (label.file.path.clone(), label.span.start))
        .collect();
    let declared = (
        "/peeper-build-test/libs/core/src/global.peep".to_owned(),
        21,
    );
    assert!(labels.contains(&declared), "{labels:?}");
}

#[test]
fn a_prelude_edit_checks_what_it_changes() {
    let prelude = ("core:global", "const stdout: i32 = 1;\nfn Helper() {}");
    let main = ("app:main", "fn main() { let out = stdout; }");
    let (first, _) = recompile(&[prelude, main], Results::default());
    let body = (
        "core:global",
        "const stdout: i32 = 1;\nfn Helper() { let a = 1; }",
    );
    let (second, checked) = recompile(&[body, main], first);
    assert_eq!(checked, ["core:global"]);
    let value = (
        "core:global",
        "const stdout: i32 = 2;\nfn Helper() { let a = 1; }",
    );
    let (_, checked) = recompile(&[value, main], second);
    assert_eq!(checked, ["core:global", "app:main"]);
}

/// What a copy's check finds goes to the module that called for it, with
/// that call marked, also from a copy made inside another copy; an error
/// every copy finds in the function's text is reported once.
#[test]
fn copies_report_once_to_the_calling_module() {
    let program = load(&[
        (
            "app:list",
            "fn Twice(const T: type, x: T) -> T { let wrong: i32 = true; return add(T, x); }
            fn add(const T: type, x: T) -> T { return x + x; }",
        ),
        (
            "app:main",
            "import \"app:list\";
            fn main() { list::Twice(i32, 1); list::Twice(bool, true); list::Twice(i64, 2); }",
        ),
    ]);
    let results = run(&program, x86_64(), Results::default(), false, false, |_| {});
    let found = |module: &ModuleResults| -> Vec<(String, Vec<String>)> {
        let labels = |diagnostic: &Diagnostic| {
            let label = |label: &diagnostics::Label| {
                format!("{} {}", label.file.slice(label.span), label.message)
            };
            diagnostic.labels.iter().map(label).collect()
        };
        let diagnostics = module.diagnostics.iter();
        diagnostics
            .map(|diagnostic| (diagnostic.message.clone(), labels(diagnostic)))
            .collect()
    };
    assert_eq!(found(&results.modules[0]), []);
    let call = |argument: &str| format!("list::Twice({argument})");
    assert_eq!(
        found(&results.modules[1]),
        [
            (
                "mismatched types: expected `i32`, found `bool`".to_string(),
                vec![
                    "true ".to_string(),
                    // Copies are built newest first.
                    format!(
                        "{} in `Twice<i64>`, the copy made for this call",
                        call("i64, 2")
                    ),
                ]
            ),
            (
                "operator `+` cannot be applied to `bool`".to_string(),
                vec![
                    "x + x ".to_string(),
                    "add(T, x) in `add<bool>`, the copy made for this call".to_string(),
                ]
            ),
        ]
    );
}

/// An importer's call builds a copy of a function with `const T: type`
/// parameters, so an edit of its body checks the importer again.
#[test]
fn a_type_parameter_body_edit_checks_its_importers() {
    let utils = |body: &str| format!("fn Size(const T: type) -> usize {{ return {body}; }}");
    let main = (
        "app:main",
        "import \"app:utils\";\nfn main() { utils::Size(i32); }",
    );
    let before = utils("size_of(T)");
    let (first, _) = recompile(&[("app:utils", &before), main], Results::default());
    let after = utils("align_of(T)");
    let (_, checked) = recompile(&[("app:utils", &after), main], first);
    assert_eq!(checked, ["app:main"]);
}

/// A function with `const` parameters records the facts of its own
/// signature once, though each call reads a value parameter's type again;
/// a copy records none.
#[test]
fn a_function_with_const_parameters_records_its_signature_once() {
    let program = load(&[(
        "app:main",
        "fn main() { let x = Pick(i32, 2, 3); let y = Pick(i32, 4, 5); }
            fn Pick(const T: type, const n: i64, x: T) -> T { return x; }",
    )]);
    let tree = &program.modules[0].ast;
    let mut found: Vec<&str> = Vec::new();
    run(
        &program,
        x86_64(),
        Results::default(),
        false,
        true,
        |output| {
            let facts = match output {
                Output::Function(CheckedFunction { facts, .. }) | Output::Facts { facts, .. } => {
                    facts
                }
                Output::Constant { .. } => return,
            };
            found.extend(facts.iter().map(|fact| tree.file.slice(fact.span)));
        },
    );
    // Checked at the first call, before its arguments.
    let expected = ["Pick", "i64", "T", "T", "i32", "2", "3", "x"];
    assert_eq!(found[..expected.len()], expected);
    assert_eq!(found.iter().filter(|&&text| text == "i64").count(), 1);
}
