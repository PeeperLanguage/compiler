//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [project folder | file.peep]
       peeper -dump [thir | mir] [project folder | file.peep]
  check  report the program's errors
  -dump  also write each module's THIR and MIR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors";

/// What the arguments ask for.
pub struct Args {
    pub path: PathBuf,    // the project folder or file
    pub dumps: Vec<Dump>, // what to dump; none for `check`
}

/// The arguments after the program's name; `None` when they make no sense.
pub fn parse(args: &[String]) -> Option<Args> {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let kind = |mode: &str| Dump::ALL.into_iter().find(|kind| kind.extension() == mode);
    let (dumps, path): (Vec<Dump>, &str) = match args.as_slice() {
        ["check"] => (Vec::new(), "."),
        ["check", path] => (Vec::new(), path),
        ["-dump"] => (Dump::ALL.to_vec(), "."),
        ["-dump", mode] if kind(mode).is_some() => (kind(mode).into_iter().collect(), "."),
        ["-dump", path] => (Dump::ALL.to_vec(), path),
        ["-dump", mode, path] if kind(mode).is_some() => (kind(mode).into_iter().collect(), path),
        _ => return None,
    };
    Some(Args {
        path: PathBuf::from(path),
        dumps,
    })
}

#[cfg(test)]
mod tests {
    use super::parse;

    /// The path and dump extensions `args` ask for, or `None`.
    fn parsed(args: &[&str]) -> Option<(String, Vec<&'static str>)> {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        let parsed = parse(&args)?;
        let dumps = parsed.dumps.iter().map(|dump| dump.extension()).collect();
        Some((parsed.path.display().to_string(), dumps))
    }

    #[test]
    fn commands() {
        let some = |path: &str, dumps: &[&'static str]| Some((path.to_string(), dumps.to_vec()));
        assert_eq!(parsed(&["check"]), some(".", &[]));
        assert_eq!(parsed(&["check", "app"]), some("app", &[]));
        assert_eq!(parsed(&["-dump"]), some(".", &["thir", "mir"]));
        assert_eq!(parsed(&["-dump", "mir"]), some(".", &["mir"]));
        assert_eq!(parsed(&["-dump", "app"]), some("app", &["thir", "mir"]));
        assert_eq!(parsed(&["-dump", "thir", "app"]), some("app", &["thir"]));
        for wrong in [
            &[][..],
            &["build"],
            &["-dump", "ll", "app"],
            &["check", "a", "b"],
        ] {
            assert_eq!(parsed(wrong), None, "{wrong:?}");
        }
    }
}
