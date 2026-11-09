# Product

## What it is

Peeper is "an experimental systems programming language and native
compiler", pre-release, with no compatibility guarantees (`README.md`). This
repository holds the compiler, being rebuilt in Rust, plus the bundled
library (`_builtin_library`), package tooling, language server and fixtures
(`x_test`). The Go compiler it replaces is on the `legacy` branch
(`PROGRESS.md`). Pre-stable: no deprecated syntax or compatibility paths are
kept (`RULES.md` §6). One binary, `peeper` (`crates/driver/peeper`), with the
commands `check`, `fix`, `build`, `run`, `run -interpret`, `-dump
thir|mir|ll`, `init` and `lsp` (its usage text). A browser build
(`crates/driver/browser`, `wasm32-wasip1`) checks and interprets programs in a
web page with no backend (`docs/architecture.md`, Packages).

## Goals, in order (`AGENTS.md` §8)

1. **Fast compiles.** State the hot-path cost of every design; measure
   before and after (`scripts/bench-instructions.py`, `rust-style.md` Speed).
2. **Clean organization.** One job per package and type; data owned by what
   it describes; no "god" structs.
3. **Fewest items to maintain.** No duplicated facts or logic; do not store
   what can be derived.
4. **Readable code.** Names that explain themselves; any package readable cold.

Base goals (`docs/architecture.md`): parallel parsing with no data race by
design; same input, same output; the LSP re-checks only what changed.
Semantic goals (`docs/ir-design.md`): constants now, compile-time code later;
as few walks per body as possible; one owner per fact.

## Hard product decisions (owner, dated in `docs/ir-design.md`)

- IRs: THIR (typed tree per body), MIR (CFG per function, shaped like LLVM at
  `-O0`), LLVM IR as text compiled by the bundled clang. Peeper runs only its
  installation's toolchain, never one on the PATH (2026-10-28).
- Check builds THIR in one walk (names, types, constants together); no side
  tables over the AST. Scheduling is per declaration, on demand.
- Constants fold in check; compile-time calls run on the MIR interpreter
  (`eval`); both use `eval`'s operator functions, so they cannot disagree.
- Integer and float results must match LLVM bit for bit
  (`crates/middle/eval/tests/llvm_parity.rs`).
- Loans: Rust-like, a borrow lasts to the last use of a reference holding it;
  two-phase `&mut` call arguments; per-field holders (2026-11-01).
- Every declaration is checked, used or not, except the body of a function
  with `const` parameters: each copy of it is checked, so one that nothing
  calls is not (2026-11-07). Reachability ignores constant values; no
  "always true" warning (2026-10-17).
- Reuse between compiles is per module, keyed on text and imported
  interfaces (2026-10-17).

## Feature status

`PROGRESS.md` (as of `70f9e8b6`, 2026-10-28) is the table; summary:

- **Done:** functions, numbers `i1`..`i8388608`, casts, constants, variables,
  conditions, loops, ranges, type aliases, modules, moves and loans,
  warnings, `-target` (32-bit included).
- **Partly done:** arrays, structs, references, slices, raw pointers,
  ownership (no drops), strings (`cstr` and literals in `print`/`panic`),
  printing, `panic`, compile-time calls, C functions, paths, methods
  (`fn Point::name(&self)`, called `Point::name(&p)` or `p.name()`).
- **Not done (parser reads, check reports T0043):** enums, optionals,
  owned interfaces, generics, function values, default
  parameters, destructuring, block expressions, pipes, dependencies.
- **Since then (worklog, 2026-11-06 to 2026-11-07):** borrowed interfaces (`&Shape`),
  `core:memory` (`Allocator` with `Heap`, `Arena`, `Pool`, `Stack`,
  `Realloc`, `New`, `Replace`), `rawptr + usize`, `size_of`/`align_of`,
  `const` parameters (a type or a value, one copy per set of arguments),
  and libraries as projects (`build = "lib"`); `core` is one.
- **Since then (2026-11-08):** references in arrays and slices
  (`[N]&T`, `&[]&Shape`), references to references and to slices; a struct
  field or array element keeps and replaces its own loans.
- **Planned order:** lifetimes of references in structs; enums and `match`
  with `?T`; strings, owned interfaces, generics; port the Go fixtures.

## Non-goals now

Disk cache (open, `docs/ir-design.md` Open decisions); threads across
modules (`compile` walks modules one by one); managed cross toolchains and
sysroots; HTML diagnostics.
