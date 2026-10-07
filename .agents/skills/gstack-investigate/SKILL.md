---
name: investigate
description: Investigate and fix Peeper compiler bugs, incorrect diagnostics, crashes, and regressions. Use when asked to debug, reproduce, find root cause, or fix broken compiler behavior, including parser, semantics, IR, backend, LSP, and package tooling. Prove the failure before implementing the smallest verified fix.
---

# Compiler investigation

Portable adaptation of the locally installed gstack investigation workflow.
Read `AGENTS.md` and `RULES.md` for edit, validation, review, and delivery gates.
This skill needs only repository tools; no global gstack scripts or services.

## 1. Pin the failure

- Read the issue, explicit user requirements, and relevant local plan.
- Check branch, status, and recent changes in the affected files. Distinguish
  pre-existing user work from the requested change.
- Record exact input, expected behavior, actual diagnostics/output, and the
  command that reproduces it. Reduce to the smallest source or test that still fails.
- For a plan-only request, investigate and write the plan without implementing.

## 2. Trace the owner

- Search symbols and their callers before reading broad directories.
- Follow input through the affected producers and consumers. For compiler bugs,
  inspect parsing, binding/type evidence, lowering, and runtime only as relevant.
- Compare the failing path with a working neighboring case. Identify which
  owner lost, duplicated, or incorrectly reconstructed a fact.
- Inspect diagnostics and source locations, node/symbol identity, evaluation
  order, mutation, caching, and phase invariants before replacing behavior.
- State one specific, testable root-cause hypothesis.

## 3. Prove the hypothesis

- Prefer a focused regression that fails on the baseline and positive controls
  that pass. Reuse existing test harnesses; do not add production APIs for tests.
- If it fails for a different reason, gather more evidence before patching.
- Use temporary probes outside tracked source. Follow the repository's experiment
  lifecycle if production instrumentation is genuinely needed.
- After three rejected hypotheses, summarize evidence and ask for a decision
  instead of stacking speculative fixes.

## 4. Fix at the canonical boundary

- Complete the repository pre-change and pre-patch gates.
- Change the existing owner rather than scattering caller workarounds or writing
  a parallel implementation. Preserve each owned behavior or explain its change.
- Keep the diff scoped to the reproduced defect and necessary coverage.
- Language changes need positive and applicable negative `x_test/` coverage,
  executed with a rebuilt `build/bin/peeper`.
- Stop at the approved step boundary unless multiple steps were explicitly requested.

## 5. Verify and hand off

- Run focused tests first, affected packages next, then required full checks once.
  Reproduce the original source failure with the repaired compiler.
- Apply [Peeper code review](../peeper-code-review/SKILL.md) to the completed diff;
  this investigation does not replace standards/spec review.
- Update the persistent local plan with red/green evidence, commands, outcomes,
  remaining risks, and resume context.
- Report symptom, proven root cause, fix, validation, review findings, and the
  required Rules check. Distinguish passed checks from checks not run.

## Routing examples

- "Fix #157: qualified range endpoints eat the loop body" → reproduce parser
  failure, inspect context propagation, test preserved enum/index behavior.
- "LSP missed a file after reopening the workspace" → reproduce the state
  sequence, inspect cache ownership/invalidation, add a focused regression.
- "Plan scoped allocators" → use `plan-eng-review`; no bug is established.
