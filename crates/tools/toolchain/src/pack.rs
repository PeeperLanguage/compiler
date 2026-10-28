//! The pack of this machine's toolchain: from the LLVM release the sources
//! lock pins for it, only the tools peeper runs (clang, its linker lld,
//! llvm-ar) and lli, which the LLVM parity test runs, with clang's headers and the LLVM
//! license, in `toolchains/native` as scripts/setup.sh unpacks it. On Windows
//! the release is llvm-mingw's, kept whole: its clang needs its DLLs, the
//! `.cfg` files that pick its target and linker, and the MinGW C library and
//! headers programs are built with; it has no lli.

use std::fs;
use std::fs::File;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

use flate2::Compression;
use flate2::write::GzEncoder;
use model::machine::Machine;

use crate::SOURCES_LOCK;
use crate::Source;
use crate::SourcesLock;
use crate::Toolchain;
use crate::curl;
use crate::read_json;
use crate::run;
use crate::sha256;
use crate::write_json;

/// Builds this machine's pack in `folder` as `<id>.tar.gz`, checks it, and
/// writes `<id>.json`, its record for the toolchains lock, with the URL of
/// its file in the release `<id>` of the GitHub `repository`; prints the id.
/// The pack is built for this machine only, the one it can be checked on.
pub(crate) fn pack(repository: &str, folder: &Path) -> Result<(), String> {
    // The check runs the tools from inside `folder`, where a relative path
    // would not reach them.
    let folder = &std::path::absolute(folder)
        .map_err(|error| format!("cannot find {}: {error}", folder.display()))?;
    // The locks name systems and architectures as Go does.
    let os = match std::env::consts::OS {
        "macos" => "darwin",
        os => os,
    };
    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        arch => arch,
    };
    let family = if os == "windows" {
        "llvm-mingw"
    } else {
        "llvm"
    };
    let sources: SourcesLock = read_json(Path::new(SOURCES_LOCK))?;
    let source_id = format!("{family}-{os}-{arch}");
    let source = sources
        .assets
        .iter()
        .find(|source| source.id == source_id)
        .ok_or_else(|| format!("the sources lock pins no {source_id}"))?;
    let stage = stage(source, folder)?;

    let packed = folder.join("pack.tar.gz");
    // Names sorted and times and owners fixed, so the same files make the
    // same pack, and so the same id: a rerun publishes nothing new.
    let file = File::create(&packed).map_err(|error| cannot("make", &packed, &error))?;
    let mut archive = tar::Builder::new(GzEncoder::new(file, Compression::best()));
    archive.mode(tar::HeaderMode::Deterministic);
    archive.follow_symlinks(false);
    append_sorted(&mut archive, &stage, &stage.join("toolchains"))
        .and_then(|()| archive.into_inner()?.finish())
        .map_err(|error| cannot("write", &packed, &error))?;
    fs::remove_dir_all(&stage).map_err(|error| cannot("remove", &stage, &error))?;
    let sha256 = sha256(&packed)?;
    let version = format!("{family}-{}-r{}", source.version, &sha256[..12]);
    let id = format!("toolchain-{os}-{arch}-{version}");
    let file = format!("{id}.tar.gz");
    let pack = folder.join(&file);
    fs::rename(&packed, &pack).map_err(|error| cannot("rename", &packed, &error))?;
    check(&pack, folder)?;

    let record = Toolchain {
        url: format!("https://github.com/{repository}/releases/download/{id}/{file}"),
        size: fs::metadata(&pack)
            .map_err(|error| cannot("read", &pack, &error))?
            .len(),
        id,
        kind: "toolchain".to_owned(),
        version,
        os: os.to_owned(),
        arch: arch.to_owned(),
        sha256,
        format: "tar.gz".to_owned(),
    };
    write_json(&folder.join(format!("{}.json", record.id)), &record)?;
    println!("{}", record.id);
    Ok(())
}

/// Adds `folder`, a folder of `stage`, and all it holds to `archive`, named
/// from `stage`, each folder's entries in name order.
fn append_sorted(
    archive: &mut tar::Builder<GzEncoder<File>>,
    stage: &Path,
    folder: &Path,
) -> io::Result<()> {
    let name = folder
        .strip_prefix(stage)
        .expect("a staged file is in the stage");
    archive.append_path_with_name(folder, name)?;
    let mut entries = fs::read_dir(folder)?
        .map(|entry| entry.map(|entry| entry.path()))
        .collect::<io::Result<Vec<PathBuf>>>()?;
    entries.sort();
    for path in entries {
        if path.is_dir() && !path.is_symlink() {
            append_sorted(archive, stage, &path)?;
        } else {
            let name = path
                .strip_prefix(stage)
                .expect("a staged file is in the stage");
            archive.append_path_with_name(&path, name)?;
        }
    }
    Ok(())
}

/// Lays out the files of the pack in `folder/stage`, from the LLVM release
/// `source`, downloaded into `folder` and checked against the lock; gives the
/// stage folder, which holds `toolchains/native`.
fn stage(source: &Source, folder: &Path) -> Result<PathBuf, String> {
    let name = source.url.rsplit('/').next().unwrap_or_default();
    // The release holds one folder, named as its file.
    let top = [".tar.xz", ".zip"]
        .iter()
        .find_map(|kind| name.strip_suffix(kind))
        .ok_or_else(|| format!("{name} is neither a .tar.xz nor a .zip release"))?;
    let major = source.version.split('.').next().unwrap_or_default();
    fs::create_dir_all(folder).map_err(|error| cannot("make", folder, &error))?;
    let release = folder.join(name);
    run(curl(&source.url).arg("--output").arg(&release))?;
    let size = fs::metadata(&release)
        .map_err(|error| cannot("read", &release, &error))?
        .len();
    if size != source.size || sha256(&release)? != source.sha256 {
        return Err(format!(
            "{} is not the file the sources lock pins",
            source.url
        ));
    }

    let stage = folder.join("stage");
    let native = stage.join("toolchains/native");
    _ = fs::remove_dir_all(&stage);
    fs::create_dir_all(&native).map_err(|error| cannot("make", &native, &error))?;
    // `tar` finds the compression itself; on Windows it is the system's,
    // which reads a zip too.
    let mut unpack = Command::new("tar");
    unpack
        .arg("-xf")
        .arg(&release)
        .arg("-C")
        .arg(&native)
        .arg("--strip-components=1");
    if cfg!(windows) {
        run(&mut unpack)?;
    } else {
        // Only what the pack keeps: the whole release unpacks to several GB.
        // `bin/clang` and the linker's names are links to `clang-<major>` and
        // `lld`, so the pack renames those instead.
        let mut kept = vec![
            format!("bin/clang-{major}"),
            "bin/lld".to_owned(),
            "bin/llvm-ar".to_owned(),
            "bin/lli".to_owned(),
            format!("lib/clang/{major}/include"),
        ];
        // The name `-fuse-ld=lld` runs on this system; lld picks its object format by it.
        let linker = if cfg!(target_os = "macos") {
            // clang links every macOS program with its builtins library.
            kept.push(format!("lib/clang/{major}/lib/darwin/libclang_rt.osx.a"));
            "ld64.lld"
        } else {
            "ld.lld"
        };
        run(unpack.args(kept.iter().map(|path| format!("{top}/{path}"))))?;
        let bin = native.join("bin");
        for (file, tool) in [
            (format!("clang-{major}"), "clang"),
            ("lld".to_owned(), linker),
        ] {
            fs::rename(bin.join(&file), bin.join(tool))
                .map_err(|error| cannot("rename", &bin.join(&file), &error))?;
        }
    }
    fs::remove_file(&release).map_err(|error| cannot("remove", &release, &error))?;
    // The release has only a short notice; the license is the file at its tag.
    let license = source.license_url.replace("/blob/", "/raw/");
    run(curl(&license)
        .arg("--output")
        .arg(native.join("LICENSE.TXT")))?;
    Ok(stage)
}

/// Checks the pack `pack` works as peeper uses it, unpacked fresh into
/// `folder`: its clang compiles a C program, its llvm-ar archives it, and its
/// clang links it with its lld into a program that runs, for this machine
/// and, on x86-64 Linux, for i686 too; its lli runs LLVM text, but on
/// Windows, whose release has none. Each tool runs
/// with no environment, so none on the PATH can stand in for a missing one.
fn check(pack: &Path, folder: &Path) -> Result<(), String> {
    let unpacked = folder.join("check");
    _ = fs::remove_dir_all(&unpacked);
    fs::create_dir_all(&unpacked).map_err(|error| cannot("make", &unpacked, &error))?;
    run(Command::new("tar")
        .arg("-xzf")
        .arg(pack)
        .arg("-C")
        .arg(&unpacked))?;
    let bin = unpacked.join("toolchains/native/bin");
    let tool = |name: &str| {
        let mut tool = Command::new(bin.join(name));
        tool.env_clear().current_dir(&unpacked);
        tool
    };
    let write = |name: &str, text: &str| {
        let file = unpacked.join(name);
        fs::write(&file, text).map_err(|error| cannot("write", &file, &error))
    };
    // A Windows C runtime writes "\r\n" for "\n".
    let expect = |printed: Vec<u8>, what: &str| {
        if printed
            .strip_suffix(b"\n")
            .map(|line| line.strip_suffix(b"\r").unwrap_or(line))
            == Some(b"peeper")
        {
            Ok(())
        } else {
            Err(format!(
                "{what} printed {:?}, not \"peeper\"",
                String::from_utf8_lossy(&printed)
            ))
        }
    };

    write(
        "hello.c",
        "#include <stdio.h>\nint main(void) { puts(\"peeper\"); return 0; }\n",
    )?;
    let host = Machine::host()?;
    let sdk = llvm::link::sdk_flags(host)?;
    let hello = format!("hello{}", host.executable_suffix());
    let mut targets = vec![None];
    if host == Machine::named("x86_64-linux")? {
        let i686 = Machine::named("i686-linux")?;
        targets.push(Some(format!("--target={}", i686.triple)));
    }
    for target in &targets {
        run(tool("clang")
            .args(target)
            .args(&sdk)
            .args(["-c", "hello.c", "-o", "hello.o"]))?;
        run(tool("llvm-ar").args(["rc", "libhello.a", "hello.o"]))?;
        run(tool("clang").args(target).args(&sdk).args([
            "-fuse-ld=lld",
            "libhello.a",
            "-o",
            &hello,
        ]))?;
        let mut program = Command::new(unpacked.join(&hello));
        expect(run(program.env_clear())?, "the linked program")?;
    }
    if host.os != "windows" {
        write(
            "hello.ll",
            "@text = private constant [7 x i8] c\"peeper\\00\"\n\
             declare i32 @puts(ptr)\n\
             define i32 @main() {\n  call i32 @puts(ptr @text)\n  ret i32 0\n}\n",
        )?;
        expect(run(tool("lli").arg("hello.ll"))?, "lli")?;
    }
    fs::remove_dir_all(&unpacked).map_err(|error| cannot("remove", &unpacked, &error))
}

/// The error of a file system `action` on `path` that failed.
fn cannot(action: &str, path: &Path, error: &io::Error) -> String {
    format!("cannot {action} {}: {error}", path.display())
}
