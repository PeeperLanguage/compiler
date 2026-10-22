//! Compile speed: loads, checks and lowers a large generated program and
//! writes its LLVM text, several times in one process, and prints the
//! fastest and the median time of each phase. Linking is left out: it is
//! clang's time, not the compiler's. `PEEPER_BENCH_RUNS` sets how many
//! compiles run; scripts/bench-instructions.py counts their instructions,
//! which machine load moves far less than time.
//!
//! cargo bench -p peeper --bench compile

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::Instant;

use model::ModuleId;

const MODULES: usize = 10;
const FUNCTIONS: usize = 2000; // in each module
const POINTER_BITS: u32 = 64;

fn main() {
    let folder = PathBuf::from("/peeper-bench");
    let project = project::Project {
        packages: HashMap::from([
            ("app".to_string(), folder.join("src")),
            ("core".to_string(), folder.join("core")),
        ]),
        entry: folder.join("src/main.peep"),
        root: folder.clone(),
        name: "app".to_string(),
    };
    let runs: usize = std::env::var("PEEPER_BENCH_RUNS").map_or(10, |runs| {
        runs.parse().expect("PEEPER_BENCH_RUNS is a number")
    });
    let files = files(&folder);
    let lines: usize = files.values().map(|text| text.lines().count()).sum();
    println!("{MODULES} modules, {FUNCTIONS} functions each, {lines} lines; {runs} runs");
    let mut load = Vec::new();
    let mut check = Vec::new(); // checking and lowering, as `compile::run` does them
    let mut llvm = Vec::new();
    for _ in 0..runs {
        let start = Instant::now();
        let program = project::load(&project, &files);
        load.push(start.elapsed());
        assert!(program.checkable, "{:?}", program.diagnostics);
        let mut ll: Vec<llvm::Module> = (0..program.modules.len())
            .map(|index| llvm::Module::new(&program, ModuleId::from_index(index), POINTER_BITS))
            .collect();
        let mut writing = Duration::ZERO; // LLVM text, timed apart from the compile around it
        let start = Instant::now();
        let results = compile::run(
            &program,
            POINTER_BITS,
            compile::Results::default(),
            false,
            |&compile::CheckedFunction {
                 definition,
                 signature,
                 mir,
                 types,
                 ..
             }| {
                let mir = mir.expect("the program has no errors");
                let start = Instant::now();
                ll[definition.module.index()].add_function(definition, signature, mir, types);
                writing += start.elapsed();
            },
        );
        check.push(start.elapsed() - writing);
        assert_eq!(
            results.diagnostics().count(),
            0,
            "the program has no errors"
        );
        let start = Instant::now();
        let text: usize = ll.iter().map(|module| module.text().len()).sum();
        llvm.push(writing + start.elapsed());
        std::hint::black_box(text);
    }
    let total: Vec<Duration> = (0..runs)
        .map(|run| load[run] + check[run] + llvm[run])
        .collect();
    for (phase, times) in [
        ("load", load),
        ("check+lower", check),
        ("llvm text", llvm),
        ("total", total),
    ] {
        let (fastest, median) = summary(times);
        println!("{phase:<12} min {fastest:>8.2?}  median {median:>8.2?}");
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
            "fn exit(code: i32) {}\n".to_string(),
        ),
    ]);
    for module in 0..MODULES {
        let mut text = String::new();
        if module > 0 {
            text.push_str(&format!("import \"app:m{}\" as previous;\n", module - 1));
        }
        for function in 0..FUNCTIONS {
            let call = match (module, function) {
                (0, 0) => "x".to_string(),
                (_, 0) => format!("previous::F{}(x, b)", FUNCTIONS - 1),
                _ => format!("F{}(x, b)", function - 1),
            };
            text.push_str(&format!(
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
            ));
        }
        files.insert(folder.join(format!("src/m{module}.peep")), text);
    }
    files
}
