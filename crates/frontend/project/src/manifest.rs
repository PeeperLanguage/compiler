//! Reads peeper.toml, the file that makes a folder a project.

use std::io::ErrorKind;
use std::path::Path;

pub const FILE_NAME: &str = "peeper.toml";
/// What `is_valid_name` asks of a package name, for messages.
pub const NAME_RULE: &str = "name must start with a letter and hold only letters, digits and `_`";

/// What a project file says.
#[derive(Debug, PartialEq)]
pub struct Manifest {
    pub name: String, // the project's package name: imports of its modules start with "name:"
}

/// Whether `name` can be a package name: a letter, then letters, digits and
/// `_`.
pub fn is_valid_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

/// Reads the project file in `dir`. Folders above `dir` are not searched.
/// An error names the folder or file it is about.
pub fn read(dir: &Path) -> Result<Manifest, String> {
    let file = dir.join(FILE_NAME);
    let text = match std::fs::read_to_string(&file) {
        Ok(text) => text,
        Err(error) if error.kind() == ErrorKind::NotFound => {
            return Err(format!("{}: no {FILE_NAME} in this folder", dir.display()));
        }
        Err(error) => return Err(format!("{}: {error}", file.display())),
    };
    let document: toml_edit::DocumentMut = text
        .parse()
        .map_err(|error| format!("{}: {error}", file.display()))?;
    match document.get("name").and_then(toml_edit::Item::as_str) {
        Some(name) if is_valid_name(name) => Ok(Manifest {
            name: name.to_string(),
        }),
        _ => Err(format!("{}: {NAME_RULE}", file.display())),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn temp_dir(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("peeper-manifest-{}-{name}", std::process::id()));
        _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn reads_the_name_in_this_folder_only() {
        let root = temp_dir("read");
        std::fs::write(root.join(FILE_NAME), "name = \"myapp\"").unwrap();
        let want = Manifest {
            name: "myapp".to_string(),
        };
        assert_eq!(read(&root), Ok(want));
        let sub = root.join("src");
        std::fs::create_dir(&sub).unwrap();
        let want = format!("{}: no {FILE_NAME} in this folder", sub.display());
        assert_eq!(read(&sub), Err(want));
    }

    #[test]
    fn rejects_bad_names() {
        for name in [r#""my-app""#, r#""_app""#, r#""1app""#, r#""""#, "3"] {
            let root = temp_dir("bad");
            std::fs::write(root.join(FILE_NAME), format!("name = {name}")).unwrap();
            assert!(read(&root).is_err(), "name {name} was accepted");
        }
    }
}
