//! The machines a program can be checked and built for: what the checker
//! needs of one (the width of `usize`, how values are laid out) and what the
//! backend needs (clang's name for it, its system).

use crate::semantic_types::Integer;

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
    /// The target `rustc` builds its runtime for.
    pub rust_triple: &'static str,
    /// How many bits its pointers, and so `usize`, have.
    pub pointer_bits: u32,
    /// The bytes an `i64`, an `f64` or another 33- to 64-bit integer aligns
    /// to in a struct.
    pub i64_align: u64,
    /// How its C code passes values, as clang lowers it.
    pub c_abi: CAbi,
}

/// The C calling conventions of the machines, as clang lowers C's integers
/// in each (measured with clang 23, `-S -emit-llvm`).
#[derive(PartialEq, Eq, Debug, Clone, Copy)]
pub enum CAbi {
    /// x86-64 System V, of Linux and macOS.
    SystemV,
    /// i386 System V.
    I386,
    /// Windows x64.
    Windows64,
    /// AAPCS64, of Linux and Windows on Arm.
    Aapcs64,
    /// Apple's AAPCS64, which extends narrow integers as x86 does.
    Darwin64,
}

/// Every machine a program can be built for. A new one is a row here, and
/// its runtime built with `build-runtime <installation> <name>`.
const MACHINES: [Machine; 7] = [
    Machine {
        name: "x86_64-linux",
        arch: "x86_64",
        os: "linux",
        triple: "x86_64-linux-gnu",
        rust_triple: "x86_64-unknown-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::SystemV,
    },
    Machine {
        name: "aarch64-linux",
        arch: "aarch64",
        os: "linux",
        triple: "aarch64-linux-gnu",
        rust_triple: "aarch64-unknown-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::Aapcs64,
    },
    // A 64-bit x86 Linux runs it with its 32-bit C library.
    Machine {
        name: "i686-linux",
        arch: "x86",
        os: "linux",
        triple: "i686-linux-gnu",
        rust_triple: "i686-unknown-linux-gnu",
        pointer_bits: 32,
        i64_align: 4,
        c_abi: CAbi::I386,
    },
    Machine {
        name: "x86_64-macos",
        arch: "x86_64",
        os: "macos",
        triple: "x86_64-apple-macos",
        rust_triple: "x86_64-apple-darwin",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::SystemV,
    },
    Machine {
        name: "aarch64-macos",
        arch: "aarch64",
        os: "macos",
        triple: "arm64-apple-macos",
        rust_triple: "aarch64-apple-darwin",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::Darwin64,
    },
    Machine {
        name: "x86_64-windows",
        arch: "x86_64",
        os: "windows",
        triple: "x86_64-w64-windows-gnu",
        rust_triple: "x86_64-pc-windows-gnullvm",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::Windows64,
    },
    Machine {
        name: "aarch64-windows",
        arch: "aarch64",
        os: "windows",
        triple: "aarch64-w64-windows-gnu",
        rust_triple: "aarch64-pc-windows-gnullvm",
        pointer_bits: 64,
        i64_align: 8,
        c_abi: CAbi::Aapcs64,
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

    /// The most bits an integer of its C code has (`__BITINT_MAXWIDTH__`).
    pub fn widest_c_integer(&self) -> u32 {
        match self.c_abi {
            CAbi::Aapcs64 | CAbi::Darwin64 => 128,
            CAbi::SystemV | CAbi::I386 | CAbi::Windows64 => Integer::MAX_BITS,
        }
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
