# Peeper

Peeper is an experimental systems programming language and native compiler. This
repository contains the compiler, bundled library, package tooling, language
server, and executable source fixtures.

> Peeper is pre-release software. Language, package, and compiler interfaces may
> change without compatibility guarantees. No stable binary release is currently
> published.

## Current capabilities

- Static semantic analysis with explicit compiler phases.
- Ownership, move, borrow, pointer, and reference checks.
- Scalars, aggregates, arrays, slices, optionals, interfaces, and modules.
- HIR and MIR lowering to LLVM IR and native executables through Clang.
- Project manifests, dependency commands, source checking, and an LSP server.

See the [open roadmap](https://github.com/PeeperLanguage/compiler/issues) for
unfinished language and runtime work.

## Requirements

- Go version declared in [`go.mod`](go.mod).
- LLVM Clang available as `clang` for native `build` and `run` commands.
- Git for cloning the compiler and fetching packages.

## Build from source

```bash
git clone https://github.com/PeeperLanguage/compiler.git
cd compiler
go run ./scripts/bundle.go
build/bin/peeper -help
```

Bundling produces:

- compiler binary: `build/bin/peeper`
- bundled libraries: `build/libs`

## Create a project

`peeper init` initializes the current directory:

```bash
mkdir hello-peeper
cd hello-peeper
/path/to/compiler/build/bin/peeper init hello-peeper
/path/to/compiler/build/bin/peeper run
```

Generated source starts with:

```peeper
fn main() {
    println("Hello from Peeper!");
}
```

## Commands

| Command | Purpose |
| --- | --- |
| `peeper build [path]` | Compile a program to a native executable. |
| `peeper run [path] [args]` | Compile and run a program on the host target. |
| `peeper check [path ...]` | Check `.peep` files or directories without linking. |
| `peeper init [name]` | Initialize `peeper.toml` and `src/main.peep`. |
| `peeper get [pkg ...]` | Install manifest or named dependencies. |
| `peeper update [pkg ...]` | Update locked dependencies. |
| `peeper sniff [pkg ...]` | Preview available dependency updates. |
| `peeper list` | List direct and transitive dependencies. |
| `peeper remove <alias>` | Remove one dependency. |
| `peeper cleanup` | Remove orphaned cached dependencies. |
| `peeper lsp` | Start the language server over standard input/output. |

Run `peeper -help` for the complete command list and current aliases.

## Compiler pipeline

```text
source
  -> tokens
  -> AST
  -> name and base-type semantics
  -> typed CFG
  -> definite initialization
  -> ownership
  -> project-wide usage analysis
  -> HIR
  -> MIR
  -> LLVM IR
  -> native linker
```

Mandatory semantic checks finish before HIR optimization may remove source
control flow. [`COMPILER_GUIDELINES.md`](COMPILER_GUIDELINES.md) explains these
phase and representation boundaries.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/` | Compiler CLI entrypoint and commands. |
| `internal/` | Frontend, semantics, IR, LSP, pipeline, and backend packages. |
| `pkg/` | Reusable manifest, registry, and utility packages. |
| `_builtin_library/` | Compiler-bundled Peeper library sources. |
| `x_test/` | Positive, negative, and runtime source fixtures. |
| `scripts/` | Bundling and repository automation. |

## Development

```bash
go vet ./...
go test ./...
go test -race ./...
go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
```

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) before proposing a change. Compiler
work must also follow [`RULES.md`](RULES.md), [`go-style.md`](go-style.md), and
[`COMPILER_GUIDELINES.md`](COMPILER_GUIDELINES.md).

Security reports belong through the private process in
[`SECURITY.md`](SECURITY.md), not a public issue. Community participation follows
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## License

Peeper is licensed under the [GNU General Public License v3.0](LICENSE).
