//! The path checks' memory on three large functions, each a whole program
//! compiled on its own. Each shape once grew with the square or cube of its
//! size: `branching` (many branches and borrows) grew 407 MB here at 400
//! groups, and once that was fixed `peeper check` took 217 MB at 10,000;
//! `straight` (one block of borrows) took 905 MB at 20,000 groups in
//! `peeper check`, and `late` (many branches, one variable set after its
//! `let`) 347 MB at 20,000 groups.

#![cfg(target_os = "linux")] // the kernel's count of a process's memory

use std::collections::HashMap;
use std::fmt::Write;
use std::path::PathBuf;

/// One line of this process's status, in KB: `VmHWM` the most memory held
/// since the last reset, `VmRSS` what it holds now.
fn status_kb(name: &str) -> u64 {
    let status = std::fs::read_to_string("/proc/self/status").unwrap();
    let line = status.lines().find(|line| line.starts_with(name));
    let kb = line.unwrap().split_whitespace().nth(1).unwrap();
    kb.parse().unwrap()
}

/// How much more memory than it held before, in KB, this process held at
/// most while compiling `text` as a whole program without an error. The
/// kernel's peak is reset first, so an earlier function's peak does not count,
/// but the memory it freed can be reused: a reading depends on what ran before.
fn compile_growth_kb(text: String) -> u64 {
    let folder = PathBuf::from("/peeper-build-test/src");
    let main = folder.join("main.peep");
    let project = project::Project {
        packages: HashMap::from([("app".to_string(), folder)]),
        entry: main.clone(),
        root: PathBuf::from("/peeper-build-test"),
        name: "app".to_string(),
    };
    std::fs::write("/proc/self/clear_refs", "5").unwrap(); // resets `VmHWM`
    let before = status_kb("VmRSS:");
    let program = project::load(&project, &HashMap::from([(main, text)]));
    assert!(program.checkable);
    let target = model::machine::Machine::named("x86_64-linux").unwrap();
    let results = compile::run(
        &program,
        target,
        compile::Results::default(),
        None,
        false,
        |_| {},
    );
    assert_eq!(results.diagnostics().count(), 0);
    status_kb("VmHWM:") - before
}

/// `main`, of `groups` of a `struct` of references to two new variables,
/// used once by a call, then a branch, then a write that its loans no longer
/// forbid.
fn branching_function(groups: usize) -> String {
    let mut text = String::from(
        "struct Holder { value: &i32, other: &i32 }
fn use_it(h: &Holder) -> i32 { return h.value.* + h.other.*; }
fn main() {
    let mut total = 0;
",
    );
    for group in 0..groups {
        _ = write!(
            text,
            "    let mut a{group} = {group};
    let b{group} = 1;
    let h{group} = Holder.{{ value = &a{group}, other = &b{group} }};
    total = total + use_it(&h{group});
    if total > 1000 {{ total = 0; }}
    a{group} = 2;
"
        );
    }
    text.push_str("}\n");
    text
}

/// `straight`, of `groups` of a new variable, a reference to it, and a
/// read through that reference, with no branch.
fn straight_function(groups: usize) -> String {
    let mut text = String::from("fn straight() -> i32 {\n    let mut total = 0;\n");
    for group in 0..groups {
        _ = write!(
            text,
            "    let a{group} = {group};
    let r{group} = &a{group};
    total = total + r{group}.*;
"
        );
    }
    text.push_str("    return total;\n}\n");
    text
}

/// `late`, of `groups` of a new variable and a branch, with one variable
/// set after its `let`, so the variable state check walks it.
fn late_set_function(groups: usize) -> String {
    let mut text = String::from(
        "fn late() -> i32 {\n    let mut total = 0;\n    let late: i32;\n    late = 1;\n",
    );
    for group in 0..groups {
        _ = write!(
            text,
            "    let mut a{group} = {group};
    if total > 1000 {{ total = 0; }}
    a{group} = a{group} + late;
    total = total + a{group};
"
        );
    }
    text.push_str("    return total;\n}\n");
    text
}

/// One test, so no other test's memory counts in a peak.
#[test]
fn large_functions_stay_small() {
    let measured = [
        // 21 MB as it runs first here; 47 MB with a set of every holder at
        // every block's end. At 400 groups, a table of every holder and loan
        // at every block took 407 MB.
        ("branching", branching_function(4000), 30),
        // 2 MB after `branching`; 28 MB alone, keeping the live holders after
        // each instruction.
        ("straight", straight_function(3000), 8),
        // 16 MB after the others, 25 MB alone; 70 MB with a set of every
        // variable at every block.
        ("late", late_set_function(8000), 35),
    ];
    for (name, text, bound_mb) in measured {
        let growth = compile_growth_kb(text);
        assert!(
            growth < bound_mb * 1024,
            "`{name}`: {growth} KB more at the highest point"
        );
    }
}
