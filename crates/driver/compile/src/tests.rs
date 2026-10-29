use std::collections::HashMap;
use std::path::PathBuf;

use super::CheckedFunction;
use super::Output;
use super::Results;
use super::run;
use model::machine::Machine;

/// The machine the tests check for, but where they test another.
fn x86_64() -> &'static Machine {
    Machine::named("x86_64-linux").unwrap()
}

/// The program of `modules`, each an import path and its text, loaded as
/// `peeper check` loads it from `app:main`, with every file given as open
/// editor text so nothing is read from disk. Loading must find no error.
fn load(modules: &[(&str, &str)]) -> project::Program {
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
        entry: folder("app").join("main.peep"),
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
    let found = run(&load(modules), x86_64(), Results::default(), false, |_| {});
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

/// No slice is longer than the largest `usize`, `u32::MAX` on a 32-bit
/// target: a constant end past it is outside every slice, though it
/// fits a `u64`, which lowering would cut to the target's width.
#[test]
fn slice_ends_fit_the_target_usize() {
    let program = load(&[(
        "app:main",
        "fn f(s: [&]i32) {
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
        |_| {},
    );
    let messages: Vec<&str> = (found.diagnostics())
        .map(|diagnostic| diagnostic.message.as_str())
        .collect();
    assert_eq!(
        messages,
        ["slice `4294967296u64..` is outside every `[&]i32`"]
    );
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
            fn main() { let x = A + B; }",
    )]);
    let found = run(&program, x86_64(), Results::default(), false, |_| {});
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
        ]
    );
}

#[test]
fn module_constants_cannot_be_assigned() {
    let found = diagnostics(&[
        ("core:global", "const Stdout: i32 = 1;"),
        (
            "app:main",
            "const Limit: i32 = 1;\nfn main() { Limit = 2; Stdout = 3; main = 4; }",
        ),
    ]);
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
    let found = run(&program, x86_64(), Results::default(), false, |output| {
        if let Output::Function(CheckedFunction { mir: Some(_), .. }) = output {
            lowered += 1;
        }
    });
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
    let results = run(&loaded, x86_64(), previous, false, |output| {
        let Output::Function(CheckedFunction { definition, .. }) = output else {
            return;
        };
        let path = &loaded.modules[definition.module.index()].path;
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
    run(&program, x86_64(), Results::default(), true, |output| {
        let (owner, facts) = match output {
            Output::Function(CheckedFunction {
                definition, facts, ..
            }) => {
                let declaration = &tree.decls[tree.declaration(definition.index)];
                (declaration.name().text(&tree.file), facts)
            }
            Output::Facts { facts, .. } => ("module", facts),
            Output::Constant { .. } => return,
        };
        found.extend(facts.iter().map(|fact| (tree.file.slice(fact.span), owner)));
    });
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
        messages(&run(&program, target, Results::default(), false, |_| {}))
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
        messages(&run(&program, target, Results::default(), false, |_| {}))
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
        messages(&run(&program, target, Results::default(), false, |_| {}))
    };
    let none =
        |text| format!("C has no `{text}` on aarch64-linux, whose integers have at most 128 bits");
    assert_eq!(at("aarch64-linux"), [none("i129"), none("i200")]);
    assert!(at("x86_64-linux").is_empty(), "{:?}", at("x86_64-linux"));
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
