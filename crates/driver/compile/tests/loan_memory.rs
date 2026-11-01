//! The loan check's memory on two large functions. One has many branches
//! and borrows: a table of every holder and every loan for every block took
//! 411 MB for it and passed 2 GB or 40 seconds at twice its size. The other
//! is one block of borrows: a set of every holder kept after each of its
//! instructions took 905 MB for 20,000 of its groups.

#![cfg(target_os = "linux")] // the kernel's count of a process's memory

use std::collections::HashMap;
use std::fmt::Write;
use std::path::PathBuf;

/// The most memory this process has held so far, in KB.
fn peak_kb() -> u64 {
    let status = std::fs::read_to_string("/proc/self/status").unwrap();
    let line = status.lines().find(|line| line.starts_with("VmHWM:"));
    let kb = line.unwrap().split_whitespace().nth(1).unwrap();
    kb.parse().unwrap()
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

/// One test, so no other test's memory counts in its peak.
#[test]
fn large_functions_stay_small() {
    let folder = PathBuf::from("/peeper-build-test/src");
    let main = folder.join("main.peep");
    let project = project::Project {
        packages: HashMap::from([("app".to_string(), folder)]),
        entry: main.clone(),
        root: PathBuf::from("/peeper-build-test"),
        name: "app".to_string(),
    };
    let text = branching_function(400) + &straight_function(3000);
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
    let peak = peak_kb();
    // 13 MB as it is; 32 MB keeping the live holders after each instruction
    // of `straight`. `main` alone took 8 MB, and 25 MB carrying the loans of
    // holders no longer live.
    assert!(peak < 20 * 1024, "{peak} KB at the highest point");
}
