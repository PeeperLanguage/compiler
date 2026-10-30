use super::Args;
use super::parse;
use llvm::BuildMode;
use model::machine::Machine;

use crate::driver::Goal;

/// The path and dump extensions `args` ask for, with "fix" added for
/// `peeper fix`, `("lsp", [])` for `peeper lsp`, `("init", [name])` for
/// `peeper init`, or `None`. `build` and
/// `run` give "build" or "run", "release" for -release, then -o's value
/// or the arguments.
fn parsed(args: &[&str]) -> Option<(String, Vec<String>)> {
    let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
    let (path, goal) = match parse(&args).ok()? {
        Args::Program { path, goal, .. } => (path, goal),
        Args::Lsp => return Some(("lsp".to_string(), Vec::new())),
        Args::Help => return Some(("help".to_string(), Vec::new())),
        Args::Init { name } => return Some(("init".to_string(), name.into_iter().collect())),
    };
    match goal {
        Goal::Check {
            dumps,
            apply_fixes,
            mode,
        } => {
            let mut asked: Vec<String> = dumps
                .iter()
                .map(|dump| dump.extension().to_string())
                .collect();
            asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
            if apply_fixes {
                asked.push("fix".to_string());
            }
            Some((path.display().to_string(), asked))
        }
        Goal::Build { output, mode } => {
            let mut asked = vec!["build".to_string()];
            asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
            asked.extend(output.map(|output| output.display().to_string()));
            Some((path.display().to_string(), asked))
        }
        Goal::Run { arguments, mode } => {
            let release = (mode == BuildMode::Release).then(|| "release".to_string());
            let asked = ["run".to_string()]
                .into_iter()
                .chain(release)
                .chain(arguments)
                .collect();
            Some((path.display().to_string(), asked))
        }
    }
}

#[test]
fn commands() {
    let some = |path: &str, asked: &[&str]| {
        Some((
            path.to_string(),
            asked.iter().map(|a| a.to_string()).collect(),
        ))
    };
    assert_eq!(parsed(&["check"]), some(".", &[]));
    assert_eq!(parsed(&["check", "app"]), some("app", &[]));
    assert_eq!(parsed(&["fix"]), some(".", &["fix"]));
    assert_eq!(parsed(&["fix", "app"]), some("app", &["fix"]));
    assert_eq!(parsed(&["-dump"]), some(".", &["thir", "mir", "ll"]));
    assert_eq!(parsed(&["-dump", "mir"]), some(".", &["mir"]));
    assert_eq!(
        parsed(&["-dump", "app"]),
        some("app", &["thir", "mir", "ll"])
    );
    assert_eq!(parsed(&["-dump", "thir", "app"]), some("app", &["thir"]));
    assert_eq!(
        parsed(&["-dump", "-release"]),
        some(".", &["thir", "mir", "ll", "release"])
    );
    assert_eq!(parsed(&["build"]), some(".", &["build"]));
    assert_eq!(parsed(&["build", "app"]), some("app", &["build"]));
    assert_eq!(
        parsed(&["build", "-o", "out"]),
        some(".", &["build", "out"])
    );
    assert_eq!(
        parsed(&["build", "-o", "out", "app"]),
        some("app", &["build", "out"])
    );
    assert_eq!(
        parsed(&["build", "-release", "-o", "out", "app"]),
        some("app", &["build", "release", "out"])
    );
    assert_eq!(parsed(&["run"]), some(".", &["run"]));
    assert_eq!(parsed(&["run", "--", "x"]), some(".", &["run", "x"]));
    assert_eq!(parsed(&["run", "-release"]), some(".", &["run", "release"]));
    assert_eq!(
        parsed(&["run", "app", "--", "x", "-help"]),
        some("app", &["run", "x", "-help"])
    );
    assert_eq!(parsed(&["-help"]), some("help", &[]));
    assert_eq!(parsed(&["lsp"]), some("lsp", &[]));
    assert_eq!(parsed(&["init"]), some("init", &[]));
    assert_eq!(parsed(&["init", "app"]), some("init", &["app"]));
    assert_eq!(parsed(&["check", "-target", "i686-linux"]), some(".", &[]));
    assert_eq!(
        parsed(&["-dump", "ll", "-target", "i686-linux", "app"]),
        some("app", &["ll"])
    );
    assert_eq!(
        parsed(&["build", "-target", "i686-linux", "-o", "out", "app"]),
        some("app", &["build", "out"])
    );
    assert_eq!(
        parsed(&["run", "-target", "i686-linux", "-release", "app", "--", "x"]),
        some("app", &["run", "release", "x"])
    );
    for wrong in [
        &[][..],
        &["build", "-o"],
        &["build", "a", "b"],
        &["run", "app", "x"],
        &["run", "app", "-help"],
        &["-help", "app"],
        &["-dump", "asm", "app"],
        &["check", "a", "b"],
        &["fix", "a", "b"],
        &["lsp", "app"],
        &["init", "a", "b"],
    ] {
        assert_eq!(parsed(wrong), None, "{wrong:?}");
    }
}

/// `-target` picks the target it names, and the host is the target
/// without it.
#[test]
fn targets() {
    let target = |args: &[&str]| {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        match parse(&args) {
            Ok(Args::Program { target, .. }) => target,
            _ => panic!("{args:?} names a program"),
        }
    };
    let i686 = Machine::named("i686-linux").unwrap();
    assert_eq!(target(&["check", "-target", "i686-linux"]), i686);
    assert_eq!(
        target(&["-dump", "ll", "-target", "i686-linux", "app"]),
        i686
    );
    assert_eq!(
        target(&["build", "-target", "i686-linux", "-o", "out"]),
        i686
    );
    assert_eq!(
        target(&["run", "app", "-target", "i686-linux", "--", "x"]),
        i686
    );
    assert_eq!(Ok(target(&["run", "app"])), Machine::host());
}

/// Flags come anywhere; the words left are the command and its value.
#[test]
fn flags_in_any_order() {
    let some = |path: &str, asked: &[&str]| {
        Some((
            path.to_string(),
            asked.iter().map(|a| a.to_string()).collect(),
        ))
    };
    let release_build = some("app", &["build", "release", "out"]);
    assert_eq!(
        parsed(&["build", "-o", "out", "app", "-release"]),
        release_build
    );
    assert_eq!(
        parsed(&["-release", "build", "app", "-o", "out"]),
        release_build
    );
    assert_eq!(
        parsed(&["build", "app", "-release", "-o", "out"]),
        release_build
    );
    assert_eq!(parsed(&["run", "-release"]), some(".", &["run", "release"]));
    assert_eq!(
        parsed(&["run", "app", "-release", "--", "a", "b"]),
        some("app", &["run", "release", "a", "b"])
    );
    assert_eq!(
        parsed(&["-dump", "ll", "-release", "app"]),
        some("app", &["ll", "release"])
    );
    assert_eq!(
        parsed(&["check", "app", "-dump", "mir"]),
        some("app", &["mir"])
    );
    assert_eq!(
        parsed(&["-dump", "ll", "-dump", "thir"]),
        some(".", &["thir", "ll"])
    );
    assert_eq!(
        parsed(&["fix", "-dump", "mir", "app"]),
        some("app", &["mir", "fix"])
    );
}

/// Each mistake is named.
#[test]
fn errors() {
    let error = |args: &[&str]| {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        parse(&args).err()
    };
    // It lists every target, so a new one changes it.
    let unknown = Machine::named("arm").unwrap_err();
    assert!(unknown.starts_with("unknown target `arm`; the targets are x86_64-linux, "));
    for (args, wanted) in [
        (&[][..], "no command given"),
        (&["app"], "unknown command `app`"),
        (&["build", "-x"], "unknown flag -x"),
        (&["run", "app", "-x"], "unknown flag -x"),
        (
            &["build", "app", "-o"],
            "-o needs the program's path after it",
        ),
        (
            &["build", "-o", "-release"],
            "-o needs the program's path after it",
        ),
        (&["build", "-o", "a", "-o", "b"], "-o is given twice"),
        (
            &["run", "-target"],
            "-target needs a target's name after it",
        ),
        (&["build", "-target", "arm"], unknown.as_str()),
        (
            &["run", "-target", "i686-linux", "-target", "i686-linux"],
            "-target is given twice",
        ),
        (
            &["init", "-target", "i686-linux"],
            "`init` does not take -target",
        ),
        (&["run", "-release", "-release"], "-release is given twice"),
        (&["-help", "-help"], "-help is given twice"),
        (&["check", "-o", "out"], "`check` does not take -o"),
        (&["check", "-release"], "`check` does not take -release"),
        (&["run", "-dump"], "`run` does not take -dump"),
        (&["run", "app", "-help"], "`run` does not take -help"),
        (&["-help", "-release"], "`-help` does not take -release"),
        (&["build", "app", "--", "x"], "`build` does not take --"),
        (&["build", "a", "b"], "unexpected `b`"),
        (&["run", "a", "b"], "unexpected `b`"),
        // `ll` does not follow `-dump`, so it is the path.
        (&["-dump", "-release", "ll", "app"], "unexpected `app`"),
        (&["lsp", "app"], "unexpected `app`"),
        (&["-help", "app"], "unexpected `app`"),
    ] {
        assert_eq!(error(args).as_deref(), Some(wanted), "{args:?}");
    }
}
