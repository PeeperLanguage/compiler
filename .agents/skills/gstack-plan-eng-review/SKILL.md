---
name: plan-eng-review
description: Brainstorm implementation approaches, plan Peeper compiler features, and review architecture or execution plans before coding. Use for requests to plan, compare designs, decide compiler boundaries, or review an implementation approach. Ground options in current source, reuse, language semantics, invariants, and executable fixtures.
---

# Compiler engineering plan review

Portable adaptation of the locally installed gstack engineering-plan workflow.
Read `AGENTS.md` and `RULES.md` for scope, local-plan, edit, and delivery gates.
This skill needs only repository tools; no global gstack scripts or services.

## 1. Establish scope

- Use the issue, user request, or named plan as the review target. Ask only when
  the target or an implementation-critical requirement is actually ambiguous.
- Read relevant source, callers, tests, specs, and architecture documents. Verify
  document claims against behavior; do not treat historical plans as task status.
- Record the current need, existing owner, canonical implementation to reuse,
  proposed shape, and experimental exit condition.
- Planning does not authorize production edits. An explicit request to implement
  permits execution after the repository gates and necessary decisions are met.

## 2. Compare approaches

- For unsettled design, propose two or three meaningfully different approaches.
  Include direct reuse or deletion when it can meet the requirement.
- Explain behavior, affected owners, invariant risks, migration cost, and test
  obligations. Recommend the smallest complete solution, not the smallest shortcut.
- For a proven defect with a clear owner, do not invent competing designs to fill
  a quota. Use `investigate` to establish root cause and evaluate the focused fix.
- Ask for a decision when language semantics, ownership, ABI, or incompatible
  architecture choices remain open. Separate recommendations from approved choices.

## 3. Review four areas

### Architecture and data flow

- Identify producers, consumers, and the fact crossing each affected boundary.
- Inventory validation, diagnostics/locations, identity, mutation, caching,
  evaluation order, and ownership/lifetime obligations before moving responsibility.
- Use a compact ASCII diagram when it clarifies nontrivial flow.
- Name concrete failure modes and how the plan preserves or tests the boundary.

### Code quality

- Prefer canonical owners and direct calls. Check proposed functions/fields for
  current production consumers and the exact helper allowance in `RULES.md`.
- Remove wrappers, stale aliases, ignored parameters, and duplicated decisions.
  Similar-looking code with distinct semantics may correctly remain separate.
- Keep unrelated refactors outside the task. Do not add speculative frameworks.

### Tests

- Map acceptance and rejection cases to existing package tests and `x_test/`.
- Include a red baseline for bugs, preserved neighboring syntax/diagnostics,
  source locations and bindings, and downstream lowerability/runtime evidence.
- Distinguish native execution from cross-target object validation. Cover affected
  target widths where numeric, pointer, layout, or ABI behavior requires it.
- Specify focused, affected, full, bundle/fixture, and diff-check commands.

### Performance

- Check whether traversal, copying, allocations, cache lifetime, or invalidation
  change. Measure only a concrete relevant cost; do not invent a benchmark project.
- Record no material performance change when that is the source-backed conclusion.

## 4. Produce an executable plan

- Use the persistent `*.localplan.md` format in `AGENTS.md`.
- Each step names goal, reason, files/owners, invariants, validation, and review stop.
- Preserve completed work and risks; do not reduce the plan to a scratch checklist.
- Report verified findings, recommendation, decisions still needed, and next step.
- During implementation, use [Peeper code review](../peeper-code-review/SKILL.md)
  for completed source changes. Plan review does not substitute for diff review.

## Routing examples

- "Plan #157" → inspect parser owner, delimiter contexts, preserved enum syntax,
  and existing range tests; produce focused fix/validation steps.
- "Brainstorm default-argument snapshot semantics" → compare evaluation-order
  and ownership models, surface the language decision before implementation.
- "Review this PR diff" → use `peeper-code-review` for standards/spec findings.
