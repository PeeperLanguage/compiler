//! Pinning the latest upstream releases in the sources lock, from what
//! GitHub says of each release, so nothing is downloaded.

use std::collections::HashMap;
use std::collections::hash_map::Entry;
use std::path::Path;

use serde::Deserialize;

use crate::SOURCES_LOCK;
use crate::SourcesLock;
use crate::curl;
use crate::read_json;
use crate::run;
use crate::write_json;

/// Each file the sources lock pins: its id, the GitHub repository whose
/// latest release it follows, what that release's tag has before the
/// version, the file's name in the release, `{version}` standing for the
/// version, and the name of the repository's license file.
const SOURCES: [(&str, &str, &str, &str, &str); 5] = [
    (
        "llvm-linux-amd64",
        "llvm/llvm-project",
        "llvmorg-",
        "LLVM-{version}-Linux-X64.tar.xz",
        "LICENSE.TXT",
    ),
    (
        "llvm-linux-arm64",
        "llvm/llvm-project",
        "llvmorg-",
        "LLVM-{version}-Linux-ARM64.tar.xz",
        "LICENSE.TXT",
    ),
    (
        "llvm-darwin-arm64",
        "llvm/llvm-project",
        "llvmorg-",
        "LLVM-{version}-macOS-ARM64.tar.xz",
        "LICENSE.TXT",
    ),
    (
        "llvm-mingw-windows-amd64",
        "mstorsjo/llvm-mingw",
        "",
        "llvm-mingw-{version}-ucrt-x86_64.zip",
        "LICENSE.txt",
    ),
    (
        "llvm-mingw-windows-arm64",
        "mstorsjo/llvm-mingw",
        "",
        "llvm-mingw-{version}-ucrt-aarch64.zip",
        "LICENSE.txt",
    ),
];

/// A GitHub release, as its API gives it.
#[derive(Deserialize)]
struct Release {
    tag_name: String,
    assets: Vec<ReleaseFile>,
}

#[derive(Deserialize)]
struct ReleaseFile {
    name: String,
    browser_download_url: String,
    size: u64,
    /// `sha256:<hex>`.
    digest: Option<String>,
}

/// Pins each source's latest release in the sources lock and prints what
/// changed, a line each, for the pull request; writes the lock only when a
/// version changed. A latest release older than the one pinned is an error.
/// Uses `$GH_TOKEN` for the GitHub API when it is set.
pub(crate) fn update() -> Result<(), String> {
    let mut lock: SourcesLock = read_json(Path::new(SOURCES_LOCK))?;
    let mut releases: HashMap<&str, Release> = HashMap::new();
    let mut changed = false;
    for (id, repository, tag_prefix, file, license) in SOURCES {
        let release = match releases.entry(repository) {
            Entry::Occupied(entry) => entry.into_mut(),
            Entry::Vacant(entry) => {
                let url = format!("https://api.github.com/repos/{repository}/releases/latest");
                let mut request = curl(&url);
                request.args(["--header", "Accept: application/vnd.github+json"]);
                if let Ok(token) = std::env::var("GH_TOKEN") {
                    request
                        .arg("--header")
                        .arg(format!("Authorization: Bearer {token}"));
                }
                let text = run(&mut request)?;
                entry.insert(
                    serde_json::from_slice(&text).map_err(|error| format!("{url}: {error}"))?,
                )
            }
        };
        let version = release.tag_name.strip_prefix(tag_prefix).ok_or_else(|| {
            format!(
                "{repository}'s tag {} has no {tag_prefix}",
                release.tag_name
            )
        })?;
        let name = file.replace("{version}", version);
        let asset = release
            .assets
            .iter()
            .find(|asset| asset.name == name)
            .ok_or_else(|| format!("{repository} {version} has no {name}"))?;
        let sha256 = asset
            .digest
            .as_deref()
            .and_then(|digest| digest.strip_prefix("sha256:"))
            .ok_or_else(|| format!("GitHub gives no sha256 of {name}"))?;
        let source = lock
            .assets
            .iter_mut()
            .find(|source| source.id == id)
            .ok_or_else(|| format!("the sources lock pins no {id}"))?;
        let order = |version: &str| {
            version
                .split('.')
                .map(str::parse)
                .collect::<Result<Vec<u64>, _>>()
                .map_err(|error| format!("{id} version {version}: {error}"))
        };
        if order(version)? < order(&source.version)? {
            return Err(format!(
                "{repository}'s latest release {version} is older than the {} pinned",
                source.version
            ));
        }
        if source.version != version {
            println!("- {id}: {} → {version}", source.version);
            changed = true;
        }
        source.version = version.to_owned();
        source.url.clone_from(&asset.browser_download_url);
        source.size = asset.size;
        sha256.clone_into(&mut source.sha256);
        source.license_url = format!(
            "https://github.com/{repository}/blob/{}/{license}",
            release.tag_name
        );
    }
    if changed {
        write_json(Path::new(SOURCES_LOCK), &lock)?;
    }
    Ok(())
}
