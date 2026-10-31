# Contributing to Peeper

Peeper is an experimental compiler with strict correctness and phase-boundary
requirements. Small, focused changes with executable evidence are easiest to
review.

## Before starting

1. Search [open issues](https://github.com/PeeperLanguage/compiler/issues) and
   existing pull requests for related work.
2. Discuss substantial language, ownership, IR, ABI, or package-model changes
   before implementation.
3. Keep security reports private according to [`SECURITY.md`](SECURITY.md).
4. Follow [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) in every project space.

## Development setup

Required tools:

- Rust, the edition and toolchain [`Cargo.toml`](Cargo.toml) declares. The
  runtime is built with `rustc` too, so `-target i686-linux` needs
  `rustup target add i686-unknown-linux-gnu`, and Windows needs
  `rustup target add x86_64-pc-windows-gnullvm`.
- `curl`, `tar` and `jq` (on Windows, PowerShell) for `scripts/setup.sh`
  (`scripts/setup.ps1`), which puts the LLVM toolchain in
  `build/toolchains/native`: first the pack CI builds for the machine, pinned
  in `toolchains/toolchains.lock.json`; else an installed LLVM 23 or newer,
  with clang, its linker (ld.lld; ld64.lld on macOS) and llvm-ar side by
  side, which it offers to install into `~/.peeper-dev` when there is none.
  It is the only clang peeper uses. The Linux and
  macOS packs also have `lli`, which the ignored LLVM parity test runs; an
  installed LLVM needs it too for that test.
- On macOS, Xcode's command line tools (`xcode-select --install`): their SDK
  holds the C library and headers programs are built with.
- On Windows, Git Bash, which runs `scripts/build.sh`.
- Git.

Set up the toolchain once, then build the compiler and bundled library:

```bash
bash scripts/setup.sh
bash scripts/build.sh
build/bin/peeper check x_test/<fixture>
```

## Project rules

Use each guidance file for its stated concern; do not copy its rules into new documents:

- [`RULES.md`](RULES.md): mandatory, durable code-quality and delivery requirements.
- [`rust-style.md`](rust-style.md): general rules for clean, idiomatic Rust.
- [`COMPILER_GUIDELINES.md`](COMPILER_GUIDELINES.md): compiler design-review guidance.

For a change that touches compiler flow or semantic model, inspect current implementation and read [`docs/ir-design.md`](docs/ir-design.md) and [`docs/architecture.md`](docs/architecture.md) as design notes to check against source, not permanent design authority.

`AGENTS.md` contains agent workflow, not additional human-facing code policy. Conflicts among guidance, source, tests, and explicit requirements require review.

## Change workflow

1. Start from current `main`.
2. Use a focused branch such as `feature/<name>` or `fix/<name>`.
3. Inspect existing implementations before adding a helper, abstraction, node
   walk, type serializer, or phase result.
4. Add focused regressions before fixing compiler behavior when practical.
5. Keep unrelated cleanup out of the change.
6. Run validation appropriate to every affected boundary.
7. Open a pull request with summary, validation commands, behavior changes, and
   explicit follow-up work.

## Tests and fixtures

Run focused package tests while developing. Before requesting merge, match CI:

```bash
cargo fmt --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace
bash scripts/build.sh
PEEPER="$PWD/build/bin/peeper" cargo test -p peeper --test fixtures
```

Language features and behavior changes require Peeper source coverage under
`x_test/`:

- add a positive type or runtime fixture;
- add negative fixtures for rejected semantics when applicable;
- validate fixtures with bundled `build/bin/peeper`;
- retain focused Rust unit tests near affected compiler crates.

Backend or ABI changes need coverage for every affected target width. Semantic
acceptance changes must prove downstream THIR, MIR, and backend lowerability.

## Commits

- Use imperative, specific subjects under 72 characters.
- Keep one logical change per commit.
- Do not commit generated binaries, LLVM output, temporary repros, caches, or
  local plans.
- Do not bypass signing, checks, or review requirements.

## Pull requests

A useful pull request explains:

- what changed and why;
- which phase or representation owns the behavior;
- what previous behavior was preserved or intentionally changed;
- exact validation commands and results;
- remaining risks or linked follow-up issues.

Reviews prioritize correctness, one canonical implementation, honest compiler
artifacts, clear ownership, and regression resistance over minimizing edited
call sites.

Every pull request must be approved by a human repository collaborator other than
the author, enforced by branch protection. New commits make older approval stale.

Before opening a pull request that touches source files, add these as the first two
lines of `README.md` if not already present:

```text
> [!IMPORTANT]
> Remove this line to confirm you have reviewed this PR before submitting.
```

Read your full diff, then remove those two lines yourself before requesting review.
The `peeper-bot[bot]` check fails and comments on the pull request while the
marker remains. Do not ask an agent to remove it for you — that defeats its purpose.

Disclose AI assistance in the pull-request body as `none`, `assisted`, or
`primarily generated`. The author remains responsible for every changed line and
must be able to explain production consumers, ownership, tests, and tradeoffs.
Disclosure does not replace review and is not itself a negative quality signal.

## Documentation changes

Keep commands, supported behavior, release status, and architecture claims tied
to live source or CI. Describe current contracts, not one refactor's history.

## Getting help

Use a GitHub issue for reproducible bugs, scoped feature proposals, or questions
that benefit from public discussion. Include compiler revision, host/target,
minimal source, actual output, and expected behavior.
