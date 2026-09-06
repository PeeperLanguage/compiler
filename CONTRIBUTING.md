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

- Go version declared in [`go.mod`](go.mod).
- LLVM Clang available as `clang` for native compilation and runtime fixtures.
- Git.

Build compiler and bundled library:

```bash
go run ./scripts/bundle.go
build/bin/peeper -version
```

## Project rules

These files are canonical; do not copy their rules into new documents:

- [`RULES.md`](RULES.md): mandatory architecture, code-quality, testing, branch,
  and commit rules.
- [`go-style.md`](go-style.md): Go-specific style and lint guidance.
- [`COMPILER_GUIDELINES.md`](COMPILER_GUIDELINES.md): compiler phase,
  representation, traversal, and incremental-analysis guidance.

For a change that touches the compiler pipeline or semantic model, read
[`docs/compiler-architecture.md`](docs/compiler-architecture.md) first. It defines
the canonical mechanisms, representation boundaries, and extension paths.
[`docs/compiler-framework/change-paths.md`](docs/compiler-framework/change-paths.md)
is the concrete file-by-file companion for common changes.

`AGENTS.md` contains automation workflow, not additional human-facing code
policy.

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
gofmt -w path/to/touched.go
go vet ./...
go test ./...
go test -race ./...
go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
```

Do not run `gofmt` over untouched files solely to create unrelated churn.

Language features and behavior changes require Peeper source coverage under
`x_test/`:

- add a positive type or runtime fixture;
- add negative fixtures for rejected semantics when applicable;
- validate fixtures with bundled `build/bin/peeper`;
- retain focused Go tests near affected compiler packages.

Backend or ABI changes need coverage for every affected target width. Semantic
acceptance changes must prove downstream HIR, MIR, and backend lowerability.

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

## Documentation changes

Keep commands, supported behavior, release status, and architecture claims tied
to live source or CI. Describe current contracts, not one refactor's history.

## Getting help

Use a GitHub issue for reproducible bugs, scoped feature proposals, or questions
that benefit from public discussion. Include compiler revision, host/target,
minimal source, actual output, and expected behavior.
