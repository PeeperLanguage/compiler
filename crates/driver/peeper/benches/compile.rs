//! Compile speed: loads, checks and lowers a large generated program and
//! writes its LLVM text, several times in one process, then has clang turn
//! the text into a program, and prints the fastest and the median time of
//! each phase. `PEEPER_BENCH_RUNS` sets how many compiles run, and
//! `PEEPER_BENCH_CLANG=0` leaves clang out; `PEEPER_BENCH_RELEASE=1` builds
//! in release mode, without debug info and with clang optimizing; scripts/bench-instructions.py
//! counts the instructions of the compiler alone that way, which machine load
//! moves far less than time.
//!
//! cargo bench -p peeper --bench compile

use std::collections::HashMap;
use std::fmt::Write;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::Instant;

use llvm::link::TOOLCHAIN_FOLDER;
use model::machine::Machine;

const MODULES: usize = 10;
const FUNCTIONS: usize = 2000; // in each module

fn main() {
    let folder = PathBuf::from("/peeper-bench");
    let project = project::Project {
        packages: HashMap::from([
            ("app".to_string(), folder.join("src")),
            ("core".to_string(), folder.join("core")),
        ]),
        entry: Some(folder.join("src/main.peep")),
        root: folder.clone(),
        name: "app".to_string(),
    };
    let runs: usize = std::env::var("PEEPER_BENCH_RUNS").map_or(10, |runs| {
        runs.parse().expect("PEEPER_BENCH_RUNS is a number")
    });
    let mode = match std::env::var("PEEPER_BENCH_RELEASE").as_deref() {
        Ok("1") => llvm::BuildMode::Release,
        _ => llvm::BuildMode::Debug,
    };
    let files = files(&folder);
    let lines: usize = files.values().map(|text| text.lines().count()).sum();
    println!("{MODULES} modules, {FUNCTIONS} functions each, {lines} lines; {runs} runs, {mode:?}");
    let target = Machine::host().expect("this machine is a target");
    // An installation to link in, unless clang is left out: its runtime is
    // built, and its toolchain is the one scripts/setup.sh puts in build/.
    let built = Path::new(env!("CARGO_TARGET_TMPDIR")).join("bench");
    let link = std::env::var("PEEPER_BENCH_CLANG").map_or(true, |clang| clang != "0");
    if link {
        let toolchain = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../build")
            .join(TOOLCHAIN_FOLDER)
            .canonicalize()
            .expect("scripts/setup.sh fills build/toolchains/native");
        _ = std::fs::remove_dir_all(&built); // the link of a run before, not what it names
        let linked = built.join(TOOLCHAIN_FOLDER);
        let parent = linked
            .parent()
            .expect("the toolchain folder is inside the bench folder");
        std::fs::create_dir_all(parent).expect("the bench folder is made");
        #[cfg(unix)]
        let made = std::os::unix::fs::symlink(toolchain, &linked);
        // shortcut: a folder symlink needs admin rights or Developer Mode on
        // Windows; make a junction instead once std's `junction_point` is stable.
        #[cfg(windows)]
        let made = std::os::windows::fs::symlink_dir(toolchain, &linked);
        made.expect("the toolchain is linked");
        llvm::runtime::build(&built, target).expect("the runtime builds");
    }
    let mut load = Vec::new();
    // Checking, lowering and LLVM text, which overlap: modules are written on
    // other threads while the next one is checked.
    let mut compile = Vec::new();
    let mut clang = Vec::new();
    for _ in 0..runs {
        let start = Instant::now();
        let program = project::load(&project, &files);
        load.push(start.elapsed());
        assert!(program.checkable, "{:?}", program.diagnostics);
        let start = Instant::now();
        let (ll, results) = peeper::write_program(&program, target, mode, true, |_, _, _, _| {});
        assert_eq!(
            results.diagnostics().count(),
            0,
            "the program has no errors"
        );
        assert!(results.entry_point().is_some(), "main.peep declares main");
        let text: usize = ll.iter().map(|module| module.text().len()).sum();
        compile.push(start.elapsed());
        std::hint::black_box(text);
        if link {
            let start = Instant::now();
            llvm::link::link_executable(&ll, mode, target, &built, &built.join("program"))
                .expect("clang builds the program");
            clang.push(start.elapsed());
        }
    }
    let total: Vec<Duration> = (0..runs).map(|run| load[run] + compile[run]).collect();
    let mut phases = vec![
        ("load", load),
        ("check+lower+llvm text", compile),
        ("total", total),
    ];
    if link {
        phases.push(("clang", clang));
    }
    for (phase, times) in phases {
        let (fastest, median) = summary(times);
        println!("{phase:<22} min {fastest:>8.2?}  median {median:>8.2?}");
    }
}

/// The fastest and the median of `times`.
fn summary(mut times: Vec<Duration>) -> (Duration, Duration) {
    times.sort();
    (times[0], times[times.len() / 2])
}

/// The program's files by path: `MODULES` modules, each importing the one
/// before it, a `main` calling into the last, and a prelude.
fn files(folder: &Path) -> HashMap<PathBuf, String> {
    let mut files = HashMap::from([
        (
            folder.join("src/main.peep"),
            format!(
                "import \"app:m{}\" as last;\nfn main() {{\n    let r = last::F{}(1, 2);\n}}\n",
                MODULES - 1,
                FUNCTIONS - 1
            ),
        ),
        (
            folder.join("core/global.peep"),
            "fn Exit(code: i32) {}\n".to_string(),
        ),
    ]);
    for module in 0..MODULES {
        let mut text = String::new();
        if module > 0 {
            _ = writeln!(text, "import \"app:m{}\" as previous;", module - 1);
        }
        for function in 0..FUNCTIONS {
            let call = match (module, function) {
                (0, 0) => "x".to_string(),
                (_, 0) => format!("previous::F{}(x, b)", FUNCTIONS - 1),
                _ => format!("F{}(x, b)", function - 1),
            };
            _ = write!(
                text,
                "const C{function}: i32 = {} + 1;\n\
                 fn F{function}(a: i32, b: i32) -> i32 {{\n    \
                     let x = a + C{function};\n    \
                     {{\n        \
                         let y = x * b;\n        \
                         if y > 3 {{ return y - 1; }}\n    \
                     }}\n    \
                     return {call} + 1;\n\
                 }}\n",
                function % 1000
            );
        }
        files.insert(folder.join(format!("src/m{module}.peep")), text);
    }
    files
}
