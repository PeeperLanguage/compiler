# Peeper

Peeper is an experimental systems programming language and native compiler. This
repository contains the compiler, bundled library, package tooling, language
server, and executable source fixtures.

> Peeper is pre-release software. Language, package, and compiler interfaces may
> change without compatibility guarantees.

## Current capabilities

- Static semantic analysis with explicit compiler phases.
- Ownership, move, borrow, pointer, and reference checks.
- Scalars, aggregates, arrays, slices, optionals, interfaces, and modules.
- THIR and MIR lowering to LLVM IR and native executables through Clang.
- Project manifests, dependency commands, source checking, and an LSP server.

See the [open roadmap](https://github.com/PeeperLanguage/compiler/issues) for
unfinished language and runtime work.

## Learning the compiler

[`Code-tour.md`](Code-tour.md) walks one `.peep` file through every phase to a native
binary, with diagrams and the real entry points at each stop.

[`docs/compiler-architecture.md`](docs/compiler-architecture.md) records current compiler mechanisms and their rationale. Verify mutable details against source when extending syntax, semantic types, graphs, or analyses.

### How the per-function callback works

The CLI and language server call [`build::compile`](crates/build/src/lib.rs)
with a closure named `use_function`. The scheduler checks and lowers one
function, calls that closure with borrowed results, then drops the function's
THIR and MIR. The callback runs synchronously inside `compile`; it is not a
separate thread or a task queued for later.

```mermaid
sequenceDiagram
    participant Caller as CLI or LSP
    participant Build as build::compile
    participant Check as Checker::body
    participant Lower as Build::lower
    participant Callback as Caller's closure

    Caller->>Build: compile(program, previous results, use_function)
    loop Each function with THIR in a module checked this compile
        Build->>Build: Query the function's signature
        Build->>Check: Check body and record names when requested
        Check-->>Build: THIR and recorded names
        Build->>Lower: Lower THIR and run path checks
        Lower-->>Build: Some(MIR) or None
        Build->>Callback: use_function(id, signature, THIR, names, MIR?, types)
        alt CLI caller
            Callback->>Callback: Append requested dumps and LLVM text
        else LSP caller
            Callback->>Callback: Convert names to cached editor information
        end
        Callback-->>Build: Return - borrowed results no longer needed
        Build->>Build: Drop this function's THIR, MIR and recorded names
    end
    Build-->>Caller: Results: diagnostics and module reuse information
```

The callback can update state captured from its caller. The
[`CLI closure`](crates/peeper/src/driver.rs) keeps generated text; the
[`LSP closure`](crates/lsp/src/lib.rs) keeps name locations and display text.
Neither needs to retain every function's IR. Types remain owned by the
scheduler for the duration of the compile; a callback must copy any information
it needs afterward into its own storage.

This is a per-function handoff, not a whole-program success notification.
Broken bodies can still provide THIR and names for editor recovery, with no
MIR. The CLI publishes dumps or links only after checking all diagnostics.
Modules reused from the previous compile produce no callbacks; the LSP keeps
their previously cached names.

## Binary installation

Linux and macOS:

```
curl --proto '=https' --tlsv1.2 -fsSL https://github.com/PeeperLanguage/compiler/releases/latest/download/install.sh | sh
```

Windows PowerShell:

```
irm https://github.com/PeeperLanguage/compiler/releases/latest/download/install.ps1 | iex
```

Bootstrap scripts detect `amd64` or `arm64` automatically, download the
release manifest and the compiler and toolchain packs for the detected host,
verify every SHA-256 digest, and activate the installation atomically. macOS
running under Rosetta installs the native `arm64` build. macOS requires
Command Line Tools and working `xcrun --sdk macosx --show-sdk-path`.

Bootstrap scripts persist the Peeper binary directory in the user PATH
idempotently; restart your terminal afterward, because a piped shell cannot
modify its parent environment.

See [`docs/distribution.md`](docs/distribution.md) for support policy, release
security, and maintainer setup.

## Source build requirements

- A Rust toolchain (`cargo`, with `rustfmt` and `clippy`) for the edition in
  [`Cargo.toml`](Cargo.toml).
- LLVM Clang available as `clang` for native `build` and `run` commands.
- Git for cloning the compiler and fetching packages.

## Build from source

```bash
git clone https://github.com/PeeperLanguage/compiler.git
cd compiler
bash scripts/build.sh
build/bin/peeper
```

`scripts/build.sh` builds:

- compiler binary: `build/bin/peeper`
- bundled libraries: `build/libs`
- runtime library: `build/runtime`

Run with no arguments, `peeper` prints its usage.

## Create a project

`peeper init` makes the current folder a project: a `peeper.toml` that names
it and a `src/main.peep` that holds `main`. It asks for the name, with the
folder's name as the default, unless one is given:

```bash
mkdir hello_peeper
cd hello_peeper
/path/to/compiler/build/bin/peeper init hello_peeper
/path/to/compiler/build/bin/peeper run
```

The generated `src/main.peep`:

```peeper
fn main() {
    write(stdout, c"Hello from Peeper!\n", 19);
}
```

A name is lowercased, and spaces and `-` become `_`. A name that is not a
package name (a letter, then letters, digits and `_`), a bundled library's name
or an existing `peeper.toml` stops it; an existing `src/main.peep` is kept.

## Commands

| Command | Purpose |
| --- | --- |
| `peeper check [path]` | Report the program's errors. |
| `peeper fix [path]` | Apply the fixes the errors suggest, then report what is left. |
| `peeper build [-o program] [path]` | Check the program and link it into an executable, named after the project or file unless `-o` names it. |
| `peeper run [path] [args]` | Build the program to a temporary file and run it with the arguments. |
| `peeper -dump [thir \| mir \| ll] [path]` | Check, then write each module's THIR, MIR and LLVM IR, or only the one named, to `build/debug/dumps`. |
| `peeper init [name]` | Make the current folder a project: write `peeper.toml` and `src/main.peep`, asking for the name unless given. |
| `peeper lsp` | Serve editors as a language server over standard input and output. |

`path` is a project folder or a single `.peep` file, and defaults to the
current folder.

## Development

```bash
cargo fmt --check
cargo clippy --all-targets -- -D warnings
cargo test
bash scripts/build.sh
```

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) before proposing a change. Mandatory engineering requirements live in [`RULES.md`](RULES.md); Rust and compiler design guidance live in [`rust-style.md`](rust-style.md) and [`COMPILER_GUIDELINES.md`](COMPILER_GUIDELINES.md).

Security reports belong through the private process in
[`SECURITY.md`](SECURITY.md), not a public issue. Community participation follows
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## License

Peeper is licensed under the [GNU General Public License v3.0](LICENSE).
