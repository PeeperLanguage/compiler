---
name: peeper-code-review
description: Review Peeper compiler changes for correctness and simple, maintainable code. Use for this repository's uncommitted changes, branches or PRs, especially requests to avoid overengineering, simplify code, reduce redundant variables/helpers/state, or find clearer equivalent solutions. Report concrete findings and smallest safe remedies; review does not imply permission to apply fixes.
---

# Peeper code review

Optimize for fewer concepts to maintain and code a new contributor can read cold.
Deletion and direct reuse beat new layers. Clear, equivalent code beats clever brevity.

## Pin scope

- Read repo `RULES.md`, `AGENTS.md`, `go-style.md` and relevant active local plan.
  Consult specs/architecture for changed behavior, then verify claims against source.
  Reference existing rules instead of creating another policy or precedence hierarchy.
- Inspect status and resolve requested baseline. For **current uncommitted changes**,
  default to `git diff HEAD -- <scope>` for final worktree state; inspect staged diff
  separately when index differs from worktree. Read untracked source/tests/fixtures.
  An empty committed range is not an empty worktree. For branch/PR review, use requested
  base/merge-base and included commits.
- Identify task work and pre-existing user edits. Cover requested source/documentation;
  do not treat unrelated edits as scope creep or overwrite them. Exclude generated
  artifacts and operational journals/local plans from implementation findings.
- Review source read-only unless user requested fixes. Follow existing agent edit gates
  and delivery authorization when implementation is requested.

## Standards and simplicity

Trace changed symbols to real callers and existing canonical owners before suggesting
abstraction, extraction or replacement. Review tests and docs as well as production.

For each changed concept, ask:

1. Does it need to exist? Recommend deletion of speculative state and stale layers.
2. Can stdlib, existing dependency or current owner do it directly? Prefer reuse.
3. Can deletion, inlining or a plain guard/loop/switch make the same behavior clearer?
4. Does a variable name a useful domain fact, prevent repeated/effectful evaluation or
   clarify an invariant? Keep it. If it only renames a clear expression, suggest removal.
5. Is a helper/type/cache/boundary justified under `RULES.md`? Keep real reuse and
   ownership/invariant boundaries; eliminate pass-through or single-use simple layers.
6. Is identical logic repeated? Use its canonical owner. Similar-looking operations
   with different semantic or validation responsibilities should remain distinct.

Recommend the smallest equivalent change. State exactly what disappears and why the
replacement is easier to understand. Fewer lines alone do not justify nested expressions,
bit tricks, generic frameworks, hidden mutation or erasing validation. Do not manufacture
cleanup findings merely to hit a quota, rename good domain variables or chase unrelated code.

## Spec and correctness

- Trace changed producers and consumers through actual compiler phases. Check source
  acceptance, lowering/materialization, backend output and runtime meaning together.
- Before deleting behavior, inventory validation/diagnostics and locations, mutation,
  caching, normalization/fallbacks, locking, ownership/lifetimes and phase invariants.
- Numeric simplification must retain intermediate truncation, signed extension and
  float rounding. Bounds must preserve exact values and trap-before-access ordering.
- Verify semantic/nominal identity, defining-owner publication and incomplete-type
  construction where affected. Do not repeat decisions already owned by checked evidence.
- Check new symbols/fields have current production consumers. Tests should prove
  behavior or invariants, not require production APIs/state only for observation.

## Evidence and validation

Use the smallest focused source/test probe needed to confirm a suspected problem.
Inspect baseline behavior when distinguishing a regression from an existing gap.
Keep temporary probes outside tracked source; add no production instrumentation.
Respect active plan's validation environment. Do not rerun unchanged expensive suites
without new edits, failures or unresolved evidence. Clearly attribute inherited results.
For language behavior, require appropriate `x_test/` coverage and bundled compiler;
for ABI/backend changes, distinguish target object validation from native execution.
Verify docs/skill edits with links, discovery/ignore rules and diff checks as relevant.

## Report

Findings first, under **Standards** and **Spec**. For each finding give:

- severity and whether correctness defect, documented breach or optional cleanup;
- file and precise lines, actual consequence and source/rule/test evidence;
- smallest safe remedy, what it removes, and behavior that must be preserved.

Distinguish introduced problems from existing gaps. Report zero findings when no
worthwhile improvement is justified. End with validation actually run, remaining
limitations and short Rules check. Keep review concise; no generic refactor wishlist.
