//! Builds the LLVM toolchain packs peeper ships and keeps the two toolchain
//! locks up to date; the workflows in .github run it in the checkout.
//!
//! cargo run -p toolchain -- pack <repository> <folder>
//! cargo run -p toolchain -- lock <record>...
//! cargo run -p toolchain -- update-sources

mod pack;
mod sources;

use std::fs::File;
use std::io::Read;
use std::path::Path;
use std::process::Command;
use std::process::ExitCode;

use serde::Deserialize;
use serde::Serialize;
use sha2::Digest;
use sha2::Sha256;

const USAGE: &str = "usage: toolchain pack <repository> <folder>
       toolchain lock <record>...
       toolchain update-sources";

/// The upstream releases the packs are built from, which scripts/setup.sh
/// also installs when there is no pack.
const SOURCES_LOCK: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../../toolchains/toolchain-sources.lock.json"
);

/// The packs CI built and published, which scripts/setup.sh downloads.
const TOOLCHAINS_LOCK: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../../toolchains/toolchains.lock.json"
);

#[derive(Serialize, Deserialize)]
struct SourcesLock {
    schema_version: u32,
    kind: String,
    assets: Vec<Source>,
}

/// A file of an upstream release, pinned.
#[derive(Serialize, Deserialize)]
struct Source {
    id: String,
    version: String,
    url: String,
    size: u64,
    sha256: String,
    license: String,
    license_url: String,
}

#[derive(Serialize, Deserialize)]
struct ToolchainsLock {
    schema_version: u32,
    kind: String,
    toolchains: Vec<Toolchain>,
}

/// A pack CI built for one machine, published at `url`; `pack` writes it as
/// the record `lock` puts in the lock.
#[derive(Serialize, Deserialize)]
struct Toolchain {
    id: String,
    kind: String,
    version: String,
    os: String,
    arch: String,
    url: String,
    size: u64,
    sha256: String,
    format: String,
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let done = match args.as_slice() {
        ["pack", repository, folder] => pack::pack(repository, Path::new(folder)),
        ["lock", records @ ..] if !records.is_empty() => lock(records),
        ["update-sources"] => sources::update(),
        _ => {
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    };
    match done {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("toolchain: {error}");
            ExitCode::FAILURE
        }
    }
}

/// Puts each pack record in the toolchains lock in place of the pack for the
/// same machine, and prints what changed, a line each, for the pull request.
fn lock(records: &[&str]) -> Result<(), String> {
    let mut lock: ToolchainsLock = read_json(Path::new(TOOLCHAINS_LOCK))?;
    for record in records {
        let record: Toolchain = read_json(Path::new(record))?;
        let machine = format!("{}-{}", record.os, record.arch);
        let old = lock
            .toolchains
            .iter_mut()
            .find(|toolchain| toolchain.os == record.os && toolchain.arch == record.arch);
        if let Some(old) = old {
            if old.version != record.version {
                println!("- {machine}: {} → {}", old.version, record.version);
            }
            *old = record;
        } else {
            println!("- {machine}: added {}", record.version);
            lock.toolchains.push(record);
        }
    }
    lock.toolchains
        .sort_by(|a, b| (&a.os, &a.arch).cmp(&(&b.os, &b.arch)));
    write_json(Path::new(TOOLCHAINS_LOCK), &lock)
}

fn read_json<T: for<'de> Deserialize<'de>>(path: &Path) -> Result<T, String> {
    let text = std::fs::read_to_string(path)
        .map_err(|error| format!("cannot read {}: {error}", path.display()))?;
    serde_json::from_str(&text).map_err(|error| format!("{}: {error}", path.display()))
}

/// Writes `value` to `path` as the locks are written: two-space indents and a
/// last newline.
fn write_json(path: &Path, value: &impl Serialize) -> Result<(), String> {
    let mut text = serde_json::to_string_pretty(value).expect("a lock is JSON");
    text.push('\n');
    std::fs::write(path, text).map_err(|error| format!("cannot write {}: {error}", path.display()))
}

/// Runs `command` and gives what it printed; what it printed as errors is
/// shown only when it fails.
fn run(command: &mut Command) -> Result<Vec<u8>, String> {
    let program = command.get_program().to_string_lossy().into_owned();
    let output = command
        .output()
        .map_err(|error| format!("cannot run {program}: {error}"))?;
    if output.status.success() {
        return Ok(output.stdout);
    }
    Err(format!(
        "{program} failed ({})\n{}",
        output.status,
        String::from_utf8_lossy(&output.stderr).trim_end()
    ))
}

/// A curl call that fetches `url`, following redirects and retrying, and
/// fails on an HTTP error.
fn curl(url: &str) -> Command {
    let mut curl = Command::new("curl");
    curl.args([
        "--fail",
        "--location",
        "--retry",
        "3",
        "--silent",
        "--show-error",
    ])
    .arg(url);
    curl
}

/// The sha256 of the file at `path`, in hex.
fn sha256(path: &Path) -> Result<String, String> {
    let cannot = |error| format!("cannot read {}: {error}", path.display());
    let mut file = File::open(path).map_err(cannot)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0; 1 << 20];
    loop {
        let read = file.read(&mut buffer).map_err(cannot)?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect())
}
