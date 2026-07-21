TASK: Reference origins, borrow conflicts, and reference returns

STATUS: done; PR merged and tracking closed

LATEST BRANCH:

- `feature/reference-origins`

LATEST COMMITS:

- `63611c9 Track reference origins and canonical integer indexes`
- `6b9e877 Enforce reference origins and borrow conflicts`
- `347d843 Update .gitignore`
- `74fe0e1 Update .gitignore`

MERGE AND TRACKING:

- PR #57 `Enforce reference origins and borrow conflicts` merged on 2026-07-19.
- Merge commit: `8ebaf5deddce2381e59fd02b0ed0f5740a98d650`.
- Issue #43 closed; milestone and Peeper Roadmap item are `Done`.
- Follow-up issue #56 tracks two-phase borrow support and is `Todo`.

WHAT IS DONE:

- Canonical reference origins and NLL-style liveness.
- Borrow-conflict enforcement over existing ownership CFG.
- Full-expression temporary borrows.
- Explicit `from` reference-return contracts.
- Imported, method, interface, function-type, recursive, and multi-origin return contracts.
- LSP hover/completion/rename support for return contracts.
- Review fixes:
  - optional reference parameters are tracked as call loans;
  - return-contract calls cannot extend temporary borrows;
  - receiver rename preserves fixed `from self`;
  - `referenceValue` was flattened to a named `[]referenceLoan`.
- `.gitignore` now ignores dot-prefixed folders with `.*/` while keeping `.gitignore` tracked via `!.gitignore`.

IMPORTANT CANONICAL IMPLEMENTATIONS:

- `internal/semantics/place` owns canonical storage origins and origin overlap.
- `internal/semantics/ownership/reference.go` owns safe-reference liveness and loan state.
- `internal/semantics/ownership/expr.go` owns expression access checking and call-duration loans.
- `typeinfo.ReferenceValueTarget` is the optional-aware reference classifier.
- `typeinfo.ReturnOriginSources` is the only direct/method return-origin source-slot mapper.
- LSP rename skips only fixed receiver-origin `from self`; named parameter origins remain renameable.

VALIDATION ALREADY RUN:

- `prlimit --as=2147483648 -- env GOCACHE=/tmp/peeper-go-cache go test -p 1 ./... -count=1 -timeout=240s`
- `prlimit --as=2147483648 -- env GOCACHE=/tmp/peeper-go-cache go test -race -p 1 ./... -count=1 -timeout=300s`
- `prlimit --as=2147483648 -- env GOCACHE=/tmp/peeper-go-cache go vet ./...`
- `prlimit --as=2147483648 -- env GOCACHE=/tmp/peeper-go-cache go run ./scripts/bundle.go`
- Affected positive fixtures:
  - `x_test/runtime_borrow_conflicts`
  - `x_test/runtime_temporary_borrows`
  - `x_test/runtime_reference_returns`
  - `x_test/runtime_reference_return_import`
  - `x_test/type_reference_return_contracts/src/main.peep`
- Affected negative fixtures emitted expected diagnostics:
  - `x_test/negative_borrow_conflict/src/main.peep`
  - `x_test/negative_temporary_borrow_escape/src/main.peep`
  - `x_test/negative_reference_return/src/main.peep`
- `git diff --check`
- executable-artifact scan under `x_test`

REMAINING WORK:

1. Delete ignored local clutter only after user confirms exact paths.
2. Keep deprecated function metadata as separate work in `peeper-deprecated-system`.
3. Implement two-phase borrows only under follow-up issue #56.

KNOWN LIMITATIONS:

- Two-phase borrows are not implemented. Patterns like `Both(&mut value, &value)` stay rejected.
- Reference-containing aggregates remain forbidden.
- Safe self-references remain rejected.
- Raw `@place` remains outside safe-loan enforcement, though addressability checks still apply.
- `from self` is fixed contract syntax, not a renameable receiver binding.

CLEANUP CANDIDATES:

- Safe local clutter candidate:
  - `.commandcode/`
- Old ignored local plans may be stale, but do not delete without explicit user approval:
  - `architecture-cleanup.localplan.md`
  - `array-closure.localplan.md`
  - `bitwise-operators.localplan.md`
  - `dynamic-array-construction.localplan.md`
  - `index-element-access-rules.localplan.md`
  - `language-model.localplan.md`
  - `method-hover-signature.localplan.md`
  - `numeric-literals.localplan.md`
  - `platform-runtime.localplan.md`
  - `projection-places.localplan.md`
  - `scalar-copy.localplan.md`
  - `semantic-import-completion.localplan.md`

RESUME COMMANDS:

- `git status --short --branch`
- `git log --oneline -5`
- `git status --short --branch --ignored`
- `gh pr list --state open --json number,title,headRefName,baseRefName,isDraft,mergeStateStatus,reviewDecision,url`
- `gh issue list --state open --json number,title,milestone,projectItems,url --limit 20`

DO NOT:

- Do not replace or shrink `reference-origins.localplan.md`.
- Do not delete old local plan files unless user approves exact names.
- Do not duplicate `ReturnOriginSources` mapping in ownership/typechecker.
- Do not commit `.commandcode/` or generated binaries.
