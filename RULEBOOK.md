# Peeper Compiler Design Review Guide

This file contains optional questions for evaluating compiler designs. Mandatory engineering requirements live in [`RULES.md`](./RULES.md); agent workflow lives in [`AGENTS.md`](./AGENTS.md); current implementation maps live in [`docs/architecture/`](./docs/architecture/).

This guide does not define language semantics, pipeline order, package ownership, or required representations. Those choices must be checked against explicit requirements, source, tests, and engineering purpose.

## Evidence before preference

Separate these questions:

1. What does implementation do now?
2. What behavior is intentionally required?
3. Which tests protect that behavior?
4. Is current design best way to provide it?

Source proves current behavior, not ideal design. Tests may preserve a mistake. Documents and comments may be stale. When evidence conflicts, identify conflict rather than choosing whichever file claims authority.

## Does this code need to exist?

Before adding code, ask:

- Does existing code already own this behavior?
- Can that owner be changed directly?
- Would new code create another source of truth?
- Does abstraction remove concepts or only move them?
- What observable behavior or invariant proves need?

Prefer deletion, direct reuse, and clear parameters before wrappers, interfaces, registries, or factories.

## Does boundary earn its keep?

Boundary is useful when it represents present distinction such as:

- different owner or lifetime;
- independently reusable behavior;
- independently testable invariant;
- external API or process boundary;
- transformation whose input and output have meaningfully different guarantees.

Boundary is suspect when:

- both sides always change together;
- one side only forwards to other;
- type wraps one value without enforcing domain distinction;
- interface has one implementation and no substitution need;
- understanding one operation requires unnecessary file hopping.

Do not preserve or remove boundary merely because architecture document names it.

## Is repeated work necessary?

Repeated syntax handling can be correct when parser, semantic analysis, lowering, and backend each make distinct decisions. Repetition becomes harmful when same fact is independently rediscovered and can diverge.

Ask:

- Are these walks answering same question?
- Could one producer publish evidence consumers already need?
- Would sharing traversal hide distinct transfer rules or evaluation order?
- Is exhaustive switch serving compiler completeness?
- Does proposed reuse reduce maintenance without obscuring semantics?

Prefer one canonical semantic decision. Keep distinct mechanisms visible when their rules differ.

## Is control flow easy to audit?

For tree, graph, block, or collection walks:

- make traversal order visible;
- use ordinary loops when index is not semantic;
- make subtree skips and early exits explicit;
- avoid hidden mutation or diagnostics inside generic helpers;
- document order only when behavior depends on it.

For loops and cleanup, verify initialization, condition, body, backedge, exit, and every cleanup path. Include zero iterations, one iteration, termination, `continue`, `break`, `return`, nesting, and failure where relevant.

## Does data carry enough meaning?

For each value crossing a boundary, ask:

- Who created it?
- What invariant does it establish?
- What identity keys it?
- Who may consume it?
- Does absence mean “not applicable,” invalid input, or missing compiler work?
- Is mutation, ownership, caching, or generation lifetime explicit?

Do not add result models by default. Use them when they make a real handoff clearer than direct calls.

## Does change preserve behavior?

Before replacing code, inspect diagnostics, validation, mutation, caching, normalization, logging, fallback behavior, and invariant checks. Simpler-looking code is not equivalent when any of those disappear.

Architecture changes should compare at least two viable options. Prefer smallest option that removes real duplication or coupling without changing language behavior or hiding completeness checks.

## Do tests prove purpose?

Useful tests prove observable runtime results, diagnostics, cleanup, phase evidence, stable identity, concurrency behavior, or backend representation. Avoid tests that only mirror implementation structure unless that structure is itself required invariant.

Language behavior changes need source fixtures in `x_test/` plus focused subsystem tests. Architecture-only refactors should keep existing behavior and validate every affected producer and consumer.

## Final review questions

- Did change create another owner for existing behavior?
- Did it add wrapper, stale alias, ignored parameter, or speculative abstraction?
- Did it reduce repeated work or merely relocate it?
- Are names and control flow understandable without architecture tour?
- Are important identities, mutations, and validation boundaries still explicit?
- Could new contributor extend this area without touching unrelated modules?
- Which nearby explicit code should remain unabstracted?
