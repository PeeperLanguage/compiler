# Compiler Engineering Rules

This file defines mandatory, durable engineering requirements for the `compiler` repository.

Guidance is separated by concern:

- [`RULES.md`](./RULES.md): code quality, validation, branch, and commit requirements.
- [`AGENTS.md`](./AGENTS.md): agent workflow, gates, local plans, and handoff requirements.
- [`go-style.md`](./go-style.md): Go-specific idioms and linter guidance.
- [`RULEBOOK.md`](./RULEBOOK.md): optional design-review questions.
- [`docs/architecture/`](./docs/architecture/): source-verified snapshots of current implementation.

No document's self-declared precedence proves a technical or semantic claim correct. When guidance conflicts, inspect explicit requirements, source behavior, tests, and engineering purpose; resolve conflict rather than silently choosing one file.

## 1. Reuse before writing

Before adding, replacing, renaming, removing, or simplifying code, search for existing implementations, callers, tests, diagnostics, and related backend or phase behavior.

Use existing code directly when behavior is identical. Extend canonical owner when behavior is nearly identical and one implementation can remain clear.

Do not add or keep:

- pass-through wrappers;
- old-name compatibility wrappers after local callers migrate;
- aliases without present domain purpose;
- signatures that retain ignored parameters unless a real interface or public API contract requires them;
- duplicated semantic decisions;
- one-field wrappers without present domain distinction, invariant, ownership boundary, or public API contract;
- helpers used once unless they clarify genuinely complex logic or protect non-obvious invariant;
- abstraction whose only purpose is avoiding call-site updates.

Possible future work is not sufficient justification.

## 2. Helpers and shared logic

New helper is allowed only when it:

- removes repeated logic used in at least two places;
- centralizes domain logic that must stay consistent;
- protects non-obvious invariant;
- crosses real ownership, lifetime, phase, or API boundary; or
- makes genuinely complex logic easier to read, test, or maintain.

Keep one canonical implementation when independent copies could diverge, especially for type relations and formatting, symbol lookup, name mangling, receiver or parameter shaping, ABI decisions, constant evaluation, diagnostics, and reusable semantic checks.

Do not centralize merely because code looks similar. Distinct language semantics, transfer functions, validation responsibilities, or backend rules may need explicit implementations.

## 3. Function replacement and behavior preservation

Before replacing or deleting function, identify everything it owns:

- validation and diagnostics;
- mutation and caching;
- normalization and conversion;
- logging and fallback handling;
- invariant checks;
- phase-specific or backend-specific behavior.

Then:

1. update callers to canonical implementation;
2. remove obsolete wrapper, alias, and unused parameters;
3. preserve, move, or intentionally remove every owned behavior;
4. add tests for preserved or deliberately changed behavior;
5. explain intentional behavior changes in review and commit rationale.

Code is not equivalent merely because remaining return value matches. Never make code look simpler by hiding behavior.

## 4. Architecture changes

Compiler architecture is reviewable. Current phase order, package boundaries, result models, identity schemes, and lowering strategy belong in source-verified architecture documents, not permanent coding rules.

For architecture or compiler-flow changes:

1. preserve observable language behavior and verified correctness invariants unless approved design change says otherwise;
2. understand current producers, consumers, validation, diagnostics, mutation, caching, and failure paths before moving responsibility;
3. keep one source of truth for semantic decision when recomputation can diverge;
4. use explicit handoffs when they clarify real boundary, but do not require result model or interface where direct code is clearer;
5. generate artifacts from real input transformations; never hardcode sample output or fake intermediate artifacts;
6. avoid knowingly blocking approved near-term requirements without adding speculative abstractions for hypothetical features;
7. record deliberate architecture changes and migration boundaries;
8. track intentionally missing or tactical work in repository issue or design tracking, including affected behavior and completion or removal impact;
9. validate every affected producer and consumer, including supported backends.

Do not bypass verified correctness boundary merely to reduce diff. Do not preserve boundary merely because existing document names it.

## 5. Naming, structure, and control flow

Name code by domain behavior. Avoid vague names such as `handle`, `processData`, `helper`, `doThing`, and `fixStuff` unless domain gives them precise meaning.

Prefer:

- short, single-purpose functions;
- flat control flow and early returns when clearer;
- ordinary `if`, `switch`, and `for range` over clever compression;
- data-driven handling when it removes repeated decisions;
- explicit mutation, evaluation order, ownership transitions, and cleanup;
- comments that explain why, invariant, boundary, or unusual tradeoff.

Do not comment what code already states.

## 6. Change scope

Keep diffs minimal and task-focused. Do not mix unrelated refactors, scatter workarounds across callers, or add special cases that hide known defect.

Fix defect at source layer when possible. Use workaround only with explicit approval and tracked removal work.

Remove dead code and migration debris in same change. Do not trade correctness for fewer edited files.

## 7. Errors, diagnostics, and panics

Preserve root-cause context and error identity. When wrapping Go error, use `%w` so callers can use `errors.Is` and `errors.As`.

Reuse shared diagnostic codes, phrasing, and construction when failure class is same. Keep source location and relevant type/name context.

Return errors for expected failures such as invalid user input, missing files, parse/type/import failures, and expected validation failures.

Use `panic` only for broken internal invariants such as unreachable branches, impossible IR states, guaranteed-non-nil violations, or unhandled closed node/effect kinds. Never use panic instead of expected error propagation, and never silently ignore internal invariant violation.

## 8. Go style

Follow [`go-style.md`](./go-style.md). At minimum:

- run `gofmt` on touched Go files;
- keep imports clean;
- prefer simple, idiomatic Go;
- avoid global mutable state unless ownership and synchronization require it;
- avoid package variables unless they represent immutable definitions or approved shared state.

## 9. Testing and validation

Behavior changes require focused tests near changed subsystem and regression coverage for previous failure.

Language behavior changes require:

- positive Peeper source fixture under `x_test/`;
- negative fixtures for rejected semantics when applicable;
- focused Go tests for affected evidence and invariants;
- validation with bundled `build/bin/peeper`.

Additional requirements:

- validate every affected backend;
- validate every affected supported target width for target-sized integers, lengths, indexes, pointers, layouts, or ABI carriers;
- ensure operands used by one backend instruction have matching backend types;
- reject target-sized values that cannot be represented without loss; never rely on backend truncation to make invalid source compile;
- require every semantically accepted construct, synthesized member, and conformance to be materializable by all affected lowering and backend stages;
- when semantic discovery changes accepted methods or conformance, validate accepted/rejected behavior through all affected lowering and backend stages, not typechecker alone;
- run `go run ./scripts/bundle.go` when packaging or bundled libraries may be affected.

Minimum validation before commit:

- `gofmt` on touched Go files;
- focused `go test` commands for touched packages;
- `go test ./...` when change scope or repository policy requires full validation;
- targeted source fixture or smoke validation for language/runtime changes;
- affected backend and target validation;
- bundle validation when packaging or built-ins change;
- `git diff --check`.

Do not claim validation passed unless command ran and passed. Do not make tests pass by deleting meaningful checks.

## 10. Commit and branch hygiene

Do not commit without explicit approval.

Use imperative, present-tense commit subject under 72 characters. Keep one logical change per commit. Include only relevant source, tests, and documentation. Do not commit generated binaries, build artifacts, or temporary repro files.

Mention important behavior changes and justify non-obvious helpers or compromises in commit body.

Do not implement feature directly on `main` or `master`. Use `feature/<name>` for features and `fix/<name>` for fixes. Check branch before editing.

## 11. Review checklist

Before completion, verify:

- [ ] Existing implementation and callers were searched before writing.
- [ ] No pass-through wrapper, stale alias, unjustified ignored parameter, or unjustified one-field wrapper was introduced.
- [ ] New helpers satisfy Section 2.
- [ ] No duplicated semantic decision remains in touched area when one owner is clearer.
- [ ] Diagnostics, validation, mutation, caching, normalization, logging, fallback behavior, and invariants were preserved or intentionally changed.
- [ ] Verified correctness boundaries were preserved or changed with rationale and tests.
- [ ] Generated artifacts come from real transformations.
- [ ] Error chains and diagnostic context remain intact.
- [ ] Panics represent only internal invariant failures.
- [ ] Tests prove behavior or invariant, not only absence of crash.
- [ ] Language changes include required `x_test/` fixtures.
- [ ] Affected backends and target widths were validated.
- [ ] Formatting and focused tests pass.
- [ ] Bundle and executable fixture validation ran when required.
- [ ] Diff contains no unrelated work or generated artifacts.
- [ ] Commit message and branch comply when commit is requested.

If any item cannot be answered clearly, change is not ready.

## 12. Golden rule

Make code actually simpler: remove stale layers, update callers, preserve important behavior, and keep one clear implementation for each decision that needs one owner.
