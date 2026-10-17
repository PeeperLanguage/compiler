//! Packages: names mapped to the folders holding their source files, and the
//! import paths of the files in them.

use std::collections::HashMap;
use std::path::{Path, PathBuf};

const SOURCE_EXTENSION: &str = ".peep";

/// Maps each package name to the folder holding its source files, for example
/// "myapp" to <project>/src and "core" to <installation>/libs/core/src. An
/// import "myapp:utils/file" names <project>/src/utils/file.peep.
pub type Packages = HashMap<String, PathBuf>;

/// The import path of the source file at `file`: its package and its path in
/// that package's folder, without the extension. A file in no package's folder
/// has only its file name for a path, and the second value is false.
pub(crate) fn path_of(packages: &Packages, file: &Path) -> (String, bool) {
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
pub(crate) fn resolve(packages: &Packages, path: &str) -> Result<(String, PathBuf), String> {
    let Some((name, rest)) = path.split_once(':') else {
        return Err("import path needs a package, such as `myapp:utils/file`".to_string());
    };
    let Some(dir) = packages.get(name) else {
        return Err(format!("unknown package `{name}`"));
    };
    let last = rest.rsplit('/').next().unwrap_or(rest);
    let rest = match last.rfind('.').map(|dot| &last[dot..]) {
        Some(SOURCE_EXTENSION) => &rest[..rest.len() - SOURCE_EXTENSION.len()],
        Some(extension) => return Err(format!("invalid import extension {extension:?}")),
        None => rest,
    };
    if rest
        .split('/')
        .any(|part| part.is_empty() || part == "." || part == "..")
    {
        return Err("import path parts cannot be empty, `.` or `..`".to_string());
    }
    let file = dir.join(format!("{rest}{SOURCE_EXTENSION}"));
    Ok((format!("{name}:{rest}"), file))
}
