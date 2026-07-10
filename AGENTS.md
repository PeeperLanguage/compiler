# Agent Workflow

This file defines how agents must work in this repository.

`RULES.md` defines what code is acceptable. Follow it for every code change. If this file conflicts with `RULES.md`, `RULES.md` wins for code quality, architecture, testing, branch, and commit rules.

Human-facing project rules belong in `RULES.md`. Agent-only workflow, gates, local plans, GitHub automation, and response style belong here.

---

## 1) Required pre-change check

Before editing code, answer these questions in your rationale:

1. What existing function/module already implements part of this behavior?
2. Can existing logic be reused directly instead of adding a wrapper?
3. Would this change duplicate logic across files, phases, or backends?
4. If a function is being replaced, renamed, removed, or simplified, what behavior did it previously own?
5. If a parameter becomes unused, why should it still exist?
6. If a new helper is introduced, which rule in `RULES.md` allows it?

Do not start implementation until these questions are answered from inspected code, not memory.

---

## 2) Mandatory pre-patch gate

Immediately before every code edit or `apply_patch`:

1. Re-read the relevant `RULES.md` sections for:
   - no pass-through wrappers
   - no stale aliases
   - no duplicated logic
   - behavior preservation
   - function replacement
   - change scope
   - compiler pipeline architecture, if relevant
   - testing requirements
2. Re-answer the pre-change questions against the current diff.
3. Check every planned new or changed function against:
   - pass-through wrapper ban
   - stale local alias ban
   - ignored parameter ban
   - duplicated logic ban
   - canonical implementation reuse first
   - helper allowance rules
   - behavior preservation rule
4. If any answer is unclear, weak, or based on assumption, stop and inspect code again before editing.

Do not rely on earlier turn notes or earlier same-turn checks. Re-run this gate before every patch.

---

## 3) Agent hard constraints

These are workflow reminders. Full authority stays in `RULES.md`.

- Do not add pass-through wrappers.
- Do not keep old function names as wrappers around new canonical functions.
- Do not keep old signatures while ignoring parameters.
- Do not duplicate logic that can be centralized.
- Prefer existing shared logic before introducing new helpers.
- Remove local repetition when it can be reduced without harming clarity.
- Optimize for readability and maintainability first, not only correctness.
- Do not leave touched code in repetitive or obviously cleanup-needed state.
- Keep diffs minimal and task-focused.
- Do not mix unrelated refactors into the same change.
- Do not satisfy compiler requests with shortcut paths that bypass intended phase boundaries.

---

## 4) Stepwise workflow

1. Keep a persistent local tracking file with the `*.localplan.md` naming pattern. Do not commit it.
2. Implement one approved step at a time.
3. Stop after each step and wait for review, unless user explicitly asks for multiple steps in one pass.
4. Commit only after explicit approval.
5. Keep the local plan as a full progress report, not a short scratch note.

The local plan must preserve completed work, current work, remaining work, risks, validation, and resume context in one place.

### Minimum local plan header

```text
TASK: <short task title>

STATUS: active|done|blocked

STEP: <one-line current step>

NEXT: <one-line next step>

NOTES:
 1. [x] <completed task 1>
 2. [x] <completed task 2>
 3. [ ] <pending task 3>
 4. [ ] <pending task 4>
```

### Required full local plan body

#### `DONE:`

Include:

- completed steps
- important decisions already made
- validations already run
- branch info
- commit info once something is committed

#### `CURRENT STATE:`

Include:

- current architecture/code state
- current active branch, if relevant
- current files/modules being worked on, if relevant
- constraints that still matter
- known issues that still matter

#### `STEP N:`

Include one section for each known remaining step.

Each step must include:

- goal
- why
- how to do it
- what must be maintained
- how to validate
- exact stop condition for review

#### `KNOWN RISKS:`

Include:

- pitfalls
- invariants
- easy-to-break assumptions
- files/areas that should not be modified carelessly
- assumptions future developers must preserve

#### `RESUME CHECKLIST:`

Include:

- what to read/check before continuing later
- latest relevant files
- latest validation command/results
- next expected edit or decision

### Progress checklist rule

- `NOTES:` must show main task progress at a glance.
- Use `[x]` for completed items.
- Use `[ ]` for pending items.
- Keep each checklist item short.
- Update the checklist whenever a step is completed, blocked, or added.
- Do not rewrite the local plan to only current and next step. Keep whole workflow visible.

---

## 5) Required close-out note

For each completed step, include a short `Rules check` note stating:

- whether any wrapper was added
- whether any stale alias remains
- whether any parameter is now ignored
- whether duplicated logic remains in touched areas
- whether any helper was added and why it is allowed under `RULES.md`
- whether diagnostics/validation/invariants were preserved or intentionally changed
- what validation was run

Do not overstate cleanup status. If duplication still exists in touched code, say so plainly.

### Peeper source fixture rule

- Every new language feature or behavior change must add or update a Peeper source fixture under `x_test/`.
- Go unit tests do not replace `x_test/` coverage.
- Add a positive type/runtime fixture and negative fixtures for rejected semantics when applicable.
- Validate new fixtures with the bundled `build/bin/peeper` before closing the step.

---

## 6) GitHub tracking automation

When work changes roadmap state, use `gh` to keep GitHub tracking current before moving to the next task.

One-word trigger:

- If the user says `ship`, treat it as approval to commit all current relevant work, push the branch, update or create the PR, wait for/verify checks, merge only if all required checks and review state are clean, and update related GitHub issues, milestones, and `Peeper Roadmap` project items.
- `ship` does not authorize destructive git operations, bypassing checks, skipping tests, using `--no-gpg-sign`, or committing unrelated files.

Required checks:

1. Check open PRs:
   - `gh pr list --state open --json number,title,headRefName,baseRefName,isDraft,mergeStateStatus,reviewDecision,url`
   - If an older clean PR is already contained in the current branch and user approves merge, merge it before opening/stacking more PRs.
2. Check relevant issues:
   - `gh issue list --state open --json number,title,milestone,projectItems,url --limit 20`
   - Update issue bodies when scope changes during implementation.
   - Add follow-up issues for explicit future work, especially when current implementation is intentionally tactical.
3. Check milestone:
   - Use milestone `0.2 Language Foundations` for language-model foundation work unless user says otherwise.
   - Add new follow-up issues to that milestone when they block arrays, slices, optionals, strings, ownership, allocator provenance, or IR architecture.
4. Check project:
   - Use org project `Peeper Roadmap` (`PeeperLanguage` project #2).
   - Add relevant issues/PRs to the project.
   - Move active work to `In Progress`.
   - Move merged PR items to `Done`.
5. PR body requirements:
   - Include summary, validation commands, and follow-up issue links.
   - If current work uses a tactical bridge, state hard-line future constraints in the PR body.

Known project fields for `Peeper Roadmap`:

- Project id: `PVT_kwDOET_G284BbrYm`
- Status field id: `PVTSSF_lADOET_G284BbrYmzhWaQYk`
- Status options:
  - `Todo`: `f75ad846`
  - `In Progress`: `47fc9ee4`
  - `Done`: `98236657`

Useful commands:

```bash
gh project list --owner PeeperLanguage --format json
gh project field-list 2 --owner PeeperLanguage --format json
gh project item-list 2 --owner PeeperLanguage --format json --limit 100
gh project item-edit --project-id PVT_kwDOET_G284BbrYm --id <item-id> --field-id PVTSSF_lADOET_G284BbrYmzhWaQYk --single-select-option-id <status-option-id>
```

---

## 7) Mandatory post-patch gate

Immediately after edits and before any stop, pause, or final response:

1. Review every touched function, method, and new field in edited files.
2. Remove any pass-through wrapper introduced during current step.
3. Remove any stale local alias introduced during current step.
4. Remove any ignored parameter introduced during current step, unless a real interface/API boundary requires it.
5. Remove or centralize duplicated logic in touched areas when possible within current step scope.
6. Re-check any new helper against the exact allowance rule in `RULES.md`.
7. Confirm diagnostics, validation, mutation, caching, logging, and invariant checks were preserved, moved, or intentionally removed.
8. Run focused validation for touched packages.
9. Report rule-audit result explicitly.

Do not stop at "step done" until this audit passes for touched files.

---

## 8) Agent conversation style

Respond terse like smart caveman. Technical substance stays. Fluff dies.

Rules:

- Drop articles when readable: `a`, `an`, `the`.
- Drop filler: `just`, `really`, `basically`, `sure`, `happy to`.
- Fragments OK.
- Short synonyms preferred.
- Technical terms exact.
- Code unchanged.
- Pattern: `[thing] [action] [reason]. [next step].`

Bad:

```text
Sure! I'd be happy to help you with that.
```

Good:

```text
Bug in auth middleware. Fix:
```
