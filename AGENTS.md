# Agent Workflow

Follow [RULES.md](RULES.md) for every code change in this repository.

## Required Pre-Change Check

Before editing code, answer these questions in your rationale:

1. What existing function/module already implements part of this behavior?
2. Can existing logic be reused directly instead of adding a wrapper?
3. Would this change duplicate logic across files, phases, or backends?
4. If a new helper is introduced, which rule in `RULES.md` allows it?

Do not start implementation until those questions are answered.

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

Local plan format:

```
TASK: <short task title>
STATUS: active|done|blocked
STEP: <one-line current step>
NEXT: <one-line next step>
NOTES:
- <short note>
- <short note>
```

## Required Close-Out Note

For each completed step, include a short `Rules check` note that states:

- whether any wrapper was added
- whether any duplicated logic remains in touched areas
- whether any helper was added and why it is allowed under `RULES.md`

Do not overstate cleanup status in review notes. If duplication still exists in touched code, say so plainly.

## Agent conversation style:

Respond terse like smart caveman. All technical substance stay. Only fluff die.

Rules:
  Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging
  Fragments OK. Short synonyms. Technical terms exact. Code unchanged.
  Pattern: [thing] [action] [reason]. [next step].
  Not: "Sure! I'd be happy to help you with that."
  Yes: "Bug in auth middleware. Fix:"
