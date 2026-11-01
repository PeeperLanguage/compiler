# Rules digest

A checklist of the six guidance files. It points to them; it does not replace
them. Read the named section before acting on a line.

## `RULES.md` (engineering, mandatory)

- §1 Search before writing. No pass-through wrappers, old-name aliases,
  ignored parameters, one-field wrappers without a domain reason, helpers
  used once (except §2's last case). Every new production item needs a
  current non-test production consumer (§1.1).
- §2 A helper must remove repetition (2+ uses), centralize domain logic,
  protect an invariant, cross a real boundary, or split a 60+ line function
  that does several jobs. Do not centralize code that only looks alike.
- §3 Before replacing a function, list what it owns (validation,
  diagnostics, mutation, caching, normalization, fallbacks, invariants) and
  keep or deliberately change each; test it.
- §4 Architecture changes: keep one source of truth, validate every
  producer and consumer, record the change.
- §5 Names by domain meaning; one item per `use`; enum name on every variant;
  no `pub use`. §5.1: one fact, one place, one meaning: no derived fields,
  one writer, names by meaning, no parallel lists, no needless aliases.
- §6 Minimal, task-focused diffs; fix at the source layer; remove dead code
  in the same change; no compatibility paths (pre-stable).
- §7 Keep error causes; reuse diagnostic codes; panic only on broken
  internal invariants.
- §8 `cargo fmt`, clippy clean with `-D warnings`, no global mutable state.
- §9 Tests near the change and a regression test; language changes need
  positive and negative `x_test` fixtures, validated with `build/bin/peeper`,
  on each affected target; run `bash scripts/check.sh`.
- §10 Commit only with approval; imperative subject under 72 chars; one
  logical change per commit; `feature/` or `fix/` branches (this session's
  designated branch overrides). §10.1: the README self-review marker is
  added for a PR and never removed by an agent.
- §12 Make code actually simpler.

## `AGENTS.md` (agent workflow)

- §1 Before editing production code, answer from source: Need, Owner,
  Reuse, Shape, Exit.
- §2 Before every patch, re-read the relevant `RULES.md` sections and
  re-check each new or changed function against them.
- §3 Name design flaws plainly with a design-level fix and its cost; no
  hidden workarounds. Keep diffs minimal.
- §4 Keep a `*.localplan.md` (uncommitted) with DONE, CURRENT STATE, STEP N,
  KNOWN RISKS, RESUME CHECKLIST; one step at a time unless told otherwise.
- §5 Close each step with a "Rules check" note: wrappers, aliases, ignored
  parameters, duplication, helpers, preserved behavior, validation run.
- §7 After a patch: review every touched function; prove each new test can
  fail; run `scripts/check.sh` and read every failure; report what was not
  done.
- §8 Goals in order: fast compiles, clean organization, fewest items,
  readable code. Decide with data; the owner decides between close options.
- §9 Terse replies.

## `rust-style.md`

Flat functions with early exits; at most two levels of nesting; one job per
function; decide a choice once; list every variant when it matters.
Iterator methods for computed values; loop over items; do not collect to
loop again. `Option` for absence, `Result` for failure; borrow by default;
no `unsafe`. One table per list (`macro_rules!`). Write each part into a
builder. Data in `model`, logic in the crate that builds it. Speed: measure
before and after; small hot data; skip work a cheap fact proves useless;
`FxHashMap` for hot maps; no guessed capacities.

## `RULEBOOK.md` (optional review questions)

Evidence before preference; does this code need to exist; does a boundary
earn its keep (suspect when one side only forwards); is repeated work
answering the same question; is control flow auditable; does data carry its
meaning; does the change preserve behavior; do tests prove purpose.

## `docs/architecture.md` (approved 2026-10-16)

One writer per fact; immutable once handed over; a step reads only its
inputs; no global registry; determinism from data. Module identity is one
`package:path` string; the prelude is an ordinary import; diagnostics are
owned by their module and printed sorted. Parser: Pratt tables for
expressions, recursive descent elsewhere, one kind of syntax per file.

## `docs/ir-design.md` (proposal with owner decisions)

THIR, MIR, LLVM; check builds THIR in one walk; per-declaration on-demand
scheduling; folding and the interpreter share `eval`; integers and floats
match LLVM; one conversion rule (`Types::conversion`); loan, move and
variable-state semantics as listed under MIR; reuse per module.
