//! Packages: names mapped to the folders holding their source files, and the
//! import paths of the files in them.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use diagnostics::suggest::nearest_name;

pub(crate) const SOURCE_EXTENSION: &str = ".peep";

/// The import path of the source file at `file`: its package and its path in
/// that package's folder, without the extension. A file in no package's folder
/// has only its file name for a path, and the second value is false.
pub(crate) fn path_of(packages: &HashMap<String, PathBuf>, file: &Path) -> (String, bool) {
    for (name, dir) in packages {
        if let Ok(relative) = file.strip_prefix(dir) {
            let parts: Vec<_> = relative
                .components()
                .map(|part| part.as_os_str().to_string_lossy())
                .collect();
            let path = parts.join("/");
            let path = path.strip_suffix(SOURCE_EXTENSION).unwrap_or(&path);
            return (format!("{name}:{path}"), true);
        }
    }
    let base = file.file_name().unwrap_or_default().to_string_lossy();
    let base = base.strip_suffix(SOURCE_EXTENSION).unwrap_or(&base);
    (base.to_string(), false)
}

/// Checks an import path as written, "package:path/in/package", and returns
/// its canonical form and the file it names. The file is not checked to exist.
/// An error is the message and, for a misspelled package, the path it may
/// have meant.
pub(crate) fn resolve(
    packages: &HashMap<String, PathBuf>,
    path: &str,
) -> Result<(String, PathBuf), (String, Option<String>)> {
    let Some((name, rest)) = path.split_once(':') else {
        let message = "import path needs a package, such as `myapp:utils/file`";
        return Err((message.to_string(), None));
    };
    let Some(dir) = packages.get(name) else {
        let known = packages.keys().map(|known| (known.as_str(), 0));
        let meant = nearest_name(name, known).map(|known| format!("{known}:{rest}"));
        return Err((format!("unknown package `{name}`"), meant));
    };
    let last = rest.rsplit('/').next().unwrap_or(rest);
    let rest = match last.rfind('.').map(|dot| &last[dot..]) {
        Some(SOURCE_EXTENSION) => &rest[..rest.len() - SOURCE_EXTENSION.len()],
        Some(extension) => {
            return Err((format!("invalid import extension {extension:?}"), None));
        }
        None => rest,
    };
    if rest
        .split('/')
        .any(|part| part.is_empty() || part == "." || part == "..")
    {
        let message = "import path parts cannot be empty, `.` or `..`";
        return Err((message.to_string(), None));
    }
    let file = dir.join(format!("{rest}{SOURCE_EXTENSION}"));
    Ok((format!("{name}:{rest}"), file))
}
