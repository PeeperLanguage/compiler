# Agent Workflow

Follow [RULES.md](RULES.md) for every code change in this repository.

## Required Pre-Change Check

Before editing code, answer these questions in your rationale:

1. What existing function/module already implements part of this behavior?
2. Can existing logic be reused directly instead of adding a wrapper?
3. Would this change duplicate logic across files, phases, or backends?
4. If a new helper is introduced, which rule in `RULES.md` allows it?

Do not start implementation until those questions are answered.

## Mandatory Pre-Patch Gate

Immediately before **every** code edit or `apply_patch`:

1. Re-read [`RULES.md`](RULES.md) sections 1, 3, 7, and 10.
2. Re-answer pre-change questions against current diff, not memory.
3. Check each planned new or changed function against:
   - pass-through wrapper ban
   - duplicated logic ban
   - existing shared logic reuse first
   - helper allowance rules
4. If any answer is unclear, weak, or based on assumption, stop and inspect code again before editing.

Do not rely on earlier turn notes or earlier same-turn checks. Re-run this gate every patch.

## Hard Constraints

- Do not add pass-through wrappers.
- Do not duplicate logic that can be centralized.
- Prefer existing shared logic before introducing new helpers.
- Always remove local repetition when it can be reduced without harming clarity.
- Optimize for readability and maintainability first, not just correctness.
- Do not leave touched code in a repetitive or obviously cleanup-needed state.
- Keep diffs minimal and task-focused.
- Do not mix unrelated refactors into the same change.
- Do not satisfy compiler requests with temporary shortcut paths that bypass intended phase boundaries.

## Compiler Pipeline Mandate (Critical)

For any compiler-flow work (`parser`, `collector`, `resolver`, `typechecker`, `HIR`, `HIR lowering`, `MIR`, `codegen`):

1. Keep real phase chain.  
   Do not collapse multiple phases into one ad-hoc function.
2. Keep phase outputs explicit data models.  
   If phase exists in architecture, represent it in code and handoff.
3. Do not fake artifacts.  
   `.hir`, `.mir`, and backend IR must come from actual lowering of previous phase model.
4. No hardcoded/manual output to satisfy sample case.  
   Output must be generated from AST/semantic inputs.
5. If scope intentionally limited, state exact boundary in code comments and close-out notes.
6. If request implies future constructs (multi-function, calls, scopes, loops), design touched code to extend without rewrite.
7. Missing phase work must be tracked as explicit TODO item in repo docs or local plan notes with impact statement.

## Anti-Shortcut Review Gate

Before marking compiler task done, confirm all:

- parser output consumed by collector
- collector output consumed by resolver
- resolver output consumed by typechecker
- typechecker output consumed by HIR lowering
- HIR consumed by MIR lowering
- MIR consumed by backend lowering
- backend output used by real toolchain step (if toolchain stage in scope)

If any link missing, status is `blocked` or `partial`, never `done`.

## Stepwise Workflow

1. Keep a persistent local tracking file with the `*.localplan.md` naming pattern. Do not commit it.
2. Implement one approved step at a time.
3. Stop after each step and wait for review.
4. Commit only after explicit approval.
5. The local plan is a full progress report, not a short scratch note. It must preserve completed work, current work, remaining work, and resume context in one place.

Minimum local plan header:

```
TASK: <short task title>
STATUS: active|done|blocked
STEP: <one-line current step>
NEXT: <one-line next step>
NOTES:
- <short note>
- <short note>
```

Required full local plan body:

- `DONE:` section
  - completed steps
  - important decisions already made
  - validations already run
  - branch/commit info once something is committed
- `CURRENT STATE:` section
  - what architecture/code state exists now
  - what constraints or known issues still matter
- `STEP N:` sections for all known remaining steps
  - goal
  - why
  - how to do it
  - what must be maintained
  - how to validate
  - exact stop condition for review
- `KNOWN RISKS:` section
  - pitfalls, invariants, or easy-to-break assumptions
- `RESUME CHECKLIST:` section
  - what to read/check before continuing later

Do not rewrite the local plan to only current and next step. Keep whole workflow visible so later steps are not forgotten.

## Required Close-Out Note

For each completed step, include a short `Rules check` note that states:

- whether any wrapper was added
- whether any duplicated logic remains in touched areas
- whether any helper was added and why it is allowed under `RULES.md`

Do not overstate cleanup status in review notes. If duplication still exists in touched code, say so plainly.

## GitHub Tracking Automation

When work changes roadmap state, use `gh` to keep GitHub tracking current before moving to the next task.

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

## Mandatory Post-Patch Gate

Immediately after edits and before any stop, pause, or final response:

1. Review every touched function, method, and new field in edited files.
2. Remove any pass-through wrapper introduced during current step.
3. Remove or centralize duplicated logic in touched areas when possible within current step scope.
4. Re-check any new helper against exact allowance rule in [`RULES.md`](RULES.md).
5. Run focused validation for touched packages.
6. Report rule-audit result explicitly.

Do not stop at "step done" until this audit passes for touched files.

## Agent conversation style:

Respond terse like smart caveman. All technical substance stay. Only fluff die.

Rules:
  Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging
  Fragments OK. Short synonyms. Technical terms exact. Code unchanged.
  Pattern: [thing] [action] [reason]. [next step].
  Not: "Sure! I'd be happy to help you with that."
  Yes: "Bug in auth middleware. Fix:"
