//! The machines a program can be checked and built for: what the checker
//! needs of one (the width of `usize`, how values are laid out) and what the
//! backend needs (clang's name for it, its system).

/// A machine a program is built for: one row of `MACHINES`.
#[derive(PartialEq, Eq, Debug)]
pub struct Machine {
    /// Its name for `-target` and its runtime's folder.
    pub name: &'static str,
    /// Its architecture and system as `std::env::consts` spells them, which
    /// find the host's row; `os` also picks the executables' suffix and
    /// whether the math library is linked.
    arch: &'static str,
    pub os: &'static str,
    /// What clang's `--target` names it.
    pub triple: &'static str,
    /// How many bits its pointers, and so `usize`, have.
    pub pointer_bits: u32,
    /// The bytes an `i64`, an `f64` or another 33- to 64-bit integer aligns
    /// to in a struct.
    pub i64_align: u64,
}

/// Every machine a program can be built for. A new one is a row here, and
/// its runtime built with `build-runtime <installation> <name>`.
const MACHINES: [Machine; 7] = [
    Machine {
        name: "x86_64-linux",
        arch: "x86_64",
        os: "linux",
        triple: "x86_64-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    Machine {
        name: "aarch64-linux",
        arch: "aarch64",
        os: "linux",
        triple: "aarch64-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    // A 64-bit x86 Linux runs it with its 32-bit C library.
    Machine {
        name: "i686-linux",
        arch: "x86",
        os: "linux",
        triple: "i686-linux-gnu",
        pointer_bits: 32,
        i64_align: 4,
    },
    Machine {
        name: "x86_64-macos",
        arch: "x86_64",
        os: "macos",
        triple: "x86_64-apple-macos",
        pointer_bits: 64,
        i64_align: 8,
    },
    Machine {
        name: "aarch64-macos",
        arch: "aarch64",
        os: "macos",
        triple: "arm64-apple-macos",
        pointer_bits: 64,
        i64_align: 8,
    },
    Machine {
        name: "x86_64-windows",
        arch: "x86_64",
        os: "windows",
        triple: "x86_64-w64-windows-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    Machine {
        name: "aarch64-windows",
        arch: "aarch64",
        os: "windows",
        triple: "aarch64-w64-windows-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
];

impl Machine {
    /// The machine the compiler runs on, or the error saying it has no row.
    pub fn host() -> Result<&'static Machine, String> {
        let (arch, os) = (std::env::consts::ARCH, std::env::consts::OS);
        MACHINES
            .iter()
            .find(|machine| machine.arch == arch && machine.os == os)
            .ok_or_else(|| format!("peeper has no target for this machine, {arch}-{os}"))
    }

    /// The most bytes one value may take, `isize::MAX`: LLVM reads offsets
    /// in memory as signed.
    pub fn largest_object(&self) -> u64 {
        (1 << (self.pointer_bits - 1)) - 1
    }

    /// The machine named `name`, or the error naming them all.
    pub fn named(name: &str) -> Result<&'static Machine, String> {
        MACHINES
            .iter()
            .find(|machine| machine.name == name)
            .ok_or_else(|| {
                let names: Vec<&str> = MACHINES.iter().map(|machine| machine.name).collect();
                format!(
                    "unknown target `{name}`; the targets are {}",
                    names.join(", ")
                )
            })
    }

    /// What its executables' names end with.
    pub fn executable_suffix(&self) -> &'static str {
        if self.os == "windows" { ".exe" } else { "" }
    }
}
