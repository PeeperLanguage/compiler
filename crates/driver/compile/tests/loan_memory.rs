//! The loan check's memory on one large function of many branches and
//! borrows. A table of every holder and every loan for every block took
//! 411 MB for this function and passed 2 GB or 40 seconds at twice its size.

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

/// `groups` of a `struct` of references to two new variables, used once by
/// a call, then a branch, then a write that its loans no longer forbid.
fn function_of(groups: usize) -> String {
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

#[test]
fn a_large_function_stays_small() {
    let folder = PathBuf::from("/peeper-build-test/src");
    let main = folder.join("main.peep");
    let project = project::Project {
        packages: HashMap::from([("app".to_string(), folder)]),
        entry: main.clone(),
        root: PathBuf::from("/peeper-build-test"),
        name: "app".to_string(),
    };
    let program = project::load(&project, &HashMap::from([(main, function_of(400))]));
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
    // 8 MB as it is; 25 MB carrying the loans of holders no longer live.
    assert!(peak < 16 * 1024, "{peak} KB at the highest point");
}
