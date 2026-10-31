# Agent Workflow

This file defines how agents must work in this repository.

`RULES.md` defines durable code-quality and delivery requirements. `AGENTS.md` owns agent workflow only. Specifications and architecture documents record language decisions and current design, but every claim must still be checked against explicit user requirements, source, tests, and engineering purpose.

When documents conflict, do not let a file's self-declared precedence settle technical correctness. Identify the conflict, inspect evidence, and ask for a decision when required. Human-facing engineering rules belong in `RULES.md`; agent-only gates, local plans, GitHub automation, and response style belong here.

For code-review requests, read [Peeper code review](.agents/skills/peeper-code-review/SKILL.md).
It captures the review workflow and user's simplicity preferences; use `RULES.md`
for durable engineering requirements.

### Automatic skill routing

Read the matching repo-local skill before starting its workflow, even when the
user does not name it. Use the skill tool when available; otherwise read its file.

| Task | Local skill |
| --- | --- |
| Investigate or fix compiler bugs, diagnostics, crashes, or regressions | [investigate](.agents/skills/gstack-investigate/SKILL.md) |
| Brainstorm implementation approaches, plan compiler features, or review architecture/execution plans | [plan-eng-review](.agents/skills/gstack-plan-eng-review/SKILL.md) |
| Review source changes, branches, or PRs; audit completed implementation | [peeper-code-review](.agents/skills/peeper-code-review/SKILL.md) |

Use investigation and planning alongside Peeper code review when each phase is
needed. Skills do not authorize implementation during a plan-only request or
change the edit, validation, review, and delivery gates below.

---

## 1) Required pre-change check

Before editing production code, answer these five lines from inspected source:

- **Need:** current non-test production behavior or consumer requiring this code.
- **Owner:** existing function/package that should own it.
- **Reuse:** existing implementation being reused or changed.
- **Shape:** why it adds no repeated fields/functions, wrapper, stale alias, or ignored parameter.
- **Exit:** what will be deleted if this is experimental and the hypothesis fails.

If `Need` names only a test, benchmark, debugging session, future feature, or possible later use, do not edit production code without explicit user approval. If replacing or deleting code, also list the validation, diagnostics, mutation, caching, and invariants it owns. Do not start implementation from memory or with any line unanswered.

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
   - relevant source, specifications, and architecture documents
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

Use `RULES.md` for durable engineering checks. Treat representation, package, and phase-boundary claims in every document as designs that may be reviewed and changed deliberately.

- Reduce local repetition without harming clarity; leave no obviously cleanup-needed touched code.
- Optimize for readability and maintainability, not only correctness.
- Keep diffs minimal and task-focused; do not mix unrelated refactors.
- Do not bypass a verified correctness boundary merely to reduce edits.
- When a bug comes from a design flaw (a wrong owner, boundary, or data flow, such as one module writing into another's state), say so plainly. Name the flaw, its root cause, and a design-level fix with its cost, then let the user choose. Do not keep the flawed design and hide it behind guards, flags, copies, or special cases. A local workaround is allowed only when labeled as a stopgap and paired with the design fix.

### Self-review marker

Before creating a commit intended for a pull request, ensure the first two lines of `README.md` are:

```text
> [!IMPORTANT]
> Remove this line to confirm you have reviewed this PR before submitting.
```

Add them if not already present. Never remove these lines yourself, even if asked to clean up, revert, or finalize the change — removing them is strictly a manual step for the human author, confirming they read the full diff before requesting review.

### Efficient execution

1. Search symbols first; do not read whole directories.
2. Read only files and line ranges needed for the next decision. Do not re-read current user attachments.
3. Run independent searches and reads in parallel.
4. Summarize an implementation decision in at most five bullets before a non-trivial patch.
5. Validate changed package first, affected packages next, and full suite once after focused checks pass.
6. Do not rerun an unchanged expensive command without new evidence.
7. Use sub-agents only for independent work whose saved effort exceeds coordination cost.
8. Keep progress updates to one or two sentences; do not repeat visible tool output.

---

## 4) Stepwise workflow

1. Keep a persistent local tracking file with the `*.localplan.md` naming pattern. Do not commit it. For explicitly read-only work, read an existing plan but do not create or modify one unless the user authorizes writes.
2. Implement one approved step at a time.
3. Stop after each step and wait for review, unless user explicitly asks for multiple steps in one pass.
4. Commit only after explicit approval.
5. Keep the local plan as a full progress report, not a short scratch note.

Experimental work must follow this lifecycle:

1. add the minimum measurement needed;
2. run and record it;
3. make the decision;
4. remove experimental production scaffolding unless explicitly approved as permanent.

A completed experiment with temporary production instrumentation still present is not complete.

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

- Agents must never approve their own pull request, submit approval through another identity, impersonate a reviewer, or weaken the human-review workflow. Only a non-author human collaborator may approve current head.
- Agents must never remove the self-review marker from `README.md`; only the human author removes it.
- After every pushed commit, treat prior human approval as stale until a human collaborator approves the new head.

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
2. Remove any pass-through wrapper, stale local alias, or ignored parameter introduced during current step, unless a real interface/API boundary requires the parameter.
3. Reread the whole diff once for repeated lines and messages; remove or centralize them when possible within current step scope.
4. Re-check any new helper against the exact allowance rule in `RULES.md`.
5. Search every new production symbol and identify its current non-test production callers. Tests and benchmarks do not count; delete symbols with none unless explicitly approved as public API.
6. If three or more sibling fields/functions share one shape, first decide whether the concept should be deleted; do not hide unjustified repetition behind a new abstraction.
7. If an experiment rejected its hypothesis, remove its production scaffolding before reporting completion.
8. Confirm diagnostics, validation, mutation, caching, logging, and invariant checks were preserved, moved, or intentionally removed.
9. Try to break the change: for each new branch, check, or message, run an input that mixes a good case with a bad one, not only the simple case.
10. Prove each new test can fail: put the bug back, watch the test fail, restore the fix. Read a test as production code: no `?`, early return, or skip condition may let it pass without checking.
11. Run `bash scripts/check.sh` and read every failure it prints. Do not filter its output or a test run's.
12. Report rule-audit result explicitly, and say which of steps 9 and 10 were not done.

Do not stop at "step done" until this audit passes for touched files. Review fixes are new code: run this gate again after them.

---

## 8) Compiler goals and design method

These are the project owner's requirements for the compiler. Every design and
every change must serve them.

### Goals, in order

1. **Fast compiles.** State the hot-path cost of every design. Measure with a
   benchmark before and after; do not guess. Profile before optimizing.
2. **Clean organization.** Each package and type has one job. No "god" structs
   that hold unrelated state (a parser holds its position and the tree it builds,
   not memory management). Data is owned by the thing it describes.
3. **Fewest items to maintain.** Fewest packages, types, fields and functions that
   do the job. No duplicated facts or logic. Do not store what can be derived.
4. **Readable code.** Descriptive, self-explaining names. Anyone can read a
   package cold.

### Design method

- **The old codebase is evidence, not a template.** It was rebuilt because its
  parts were too interconnected to fix. For every piece, ask what the compiler
  needs, then find the best design: reason it out, look at how other compilers
  solve it (Go, Rust, Zig, Odin, Clang, ...), and compare two or three options.
  Bring old code back only when it is the best option, and simplify it first.
- **Every piece passes three gates:** is it needed now (or by the agreed design)?
  Is it the simplest form? Does something already do it?
- **Unused today is not a reason to delete** a general, standalone helper.
- **Design flaws are named, not patched over.** One writer per fact; values are
  immutable once handed over; a step never writes another module's data.
- **Prefer invariants kept by construction plus a test** over hand-written guards
  that someone must remember to update.
- **Decide with data:** when options are close, build small prototypes with the
  same tests and benchmarks and show the numbers. Present options with a
  recommendation; the owner decides.
- **After each piece, ask whether it is fully ready** (faster, less code, less
  work) and finish it before starting the next.
- **Small rules:** never pass a concatenated string to a buffered writer's
  `WriteString` (write each part); render Mermaid diagrams before handing them
  over; never derive a fact from a naming convention.

---

## 9) Agent conversation style

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
