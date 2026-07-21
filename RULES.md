# Coding Rules

This file defines mandatory engineering rules for the `compiler` repository.

These rules apply to humans and agents. For agent workflow, see [AGENTS.md](./AGENTS.md). For Go-specific idioms and linter rules, see [go-style.md](./go-style.md).

If files conflict, use this order:

1. `RULES.md` for repository code quality, compiler architecture, testing, branch, and commit rules.
2. `go-style.md` for Go idioms and linter patterns.
3. `AGENTS.md` for agent workflow only.
4. Personal style skills for general preference only.

---

## 1) Core principle — no pass-through wrappers, no duplicated logic, no stale aliases

Before creating, renaming, replacing, removing, or simplifying any function, search for existing similar code first.

Use existing functions directly if behavior is identical.

Do not add or keep a function that only forwards arguments or returns results unchanged.

Do not add struct which only has one single field. There is no point of that then. For future work, explicitly add comments so its not removed by later cleanup.

Do not create aliases without any valid reason.

Do not keep an old local function name as a wrapper around a new canonical function.

Do not keep an old function signature while ignoring one or more parameters.

### Why

Pass-through wrappers and stale aliases are harmful because they:

- increase maintenance cost
- add unnecessary call indirection
- hide the real implementation
- create multiple sources of truth
- make behavior harder to audit
- preserve obsolete API shapes after behavior has changed
- can silently remove validation, diagnostics, mutation, caching, logging, or invariant checks
- can mislead future developers into thinking old behavior still exists

Code clarity matters more than reducing the number of edited call sites. If clean design requires updating all call sites, update all call sites.

### Good

```go
result := calculateTotal(items)
```

```go
func formatPrice(p float64) string {
	return fmt.Sprintf("$%.2f", p)
}

display1 := formatPrice(100)
display2 := formatPrice(200)
```

### Bad

```go
func getTotal(items []Item) int {
	return calculateTotal(items) // pointless wrapper
}
```

```go
display1 := fmt.Sprintf("$%.2f", price1) // duplicated formatting logic
display2 := fmt.Sprintf("$%.2f", price2)
```

---

## 2) Function replacement rule

When replacing an old function with a new canonical function:

1. Delete the old function if it becomes a pure wrapper.
2. Update call sites to use the canonical function directly.
3. Remove unused parameters from the call path.
4. Verify whether the old function had extra behavior.
5. Preserve, move, or intentionally remove that behavior with tests and commit rationale.

### Bad

```go
func foldExpr(expr ir.Expr, diag *diagnostics.DiagnosticBag, env map[string]ir.ConstValue) ir.Expr {
	return ir.FoldExpr(expr, env)
}
```

This is forbidden even though the signatures differ.

Problems:

- It only calls `ir.FoldExpr`.
- It keeps old local name `foldExpr`.
- It ignores `diag`.
- It falsely suggests diagnostics still happen.
- It hides canonical implementation.
- It avoids updating call sites.
- It leaves stale API shape in codebase.

### Good

```go
folded := ir.FoldExpr(expr, env)
```

Use the canonical function directly at the call site.

---

## 3) Behavior preservation rule

A function is not equivalent to another function if it performs additional work.

Additional work includes:

- diagnostics
- validation
- mutation
- caching
- logging
- normalization
- type conversion
- fallback handling
- invariant checks
- backend-specific behavior
- phase-specific compiler behavior

### Example

```go
func foldExpr(expr ir.Expr, diag *diagnostics.DiagnosticBag, env map[string]ir.ConstValue) ir.Expr {
	folded := ir.FoldExpr(expr, env)
	checkConstantArrayIndex(folded, diag)
	return folded
}
```

This function is not equivalent to:

```go
ir.FoldExpr(expr, env)
```

because it also checks constant array indexes and emits diagnostics.

This replacement is unsafe:

```go
func foldExpr(expr ir.Expr, diag *diagnostics.DiagnosticBag, env map[string]ir.ConstValue) ir.Expr {
	return ir.FoldExpr(expr, env)
}
```

It silently removes diagnostics while keeping the old function name and signature.

Before simplifying, deleting, or replacing such a function, verify one of the following:

- extra behavior was moved to a new canonical location
- extra behavior is obsolete and intentionally removed
- all affected call sites now perform required behavior explicitly
- focused regression test proves intended behavior
- commit rationale explains why behavior changed

Never remove behavior just because the remaining function body looks simpler.

---

## 4) Helper creation rule

A new helper is allowed only if at least one condition is true:

- It removes repeated logic used in 2 or more places.
- It centralizes domain logic that must stay consistent.
- It protects a non-obvious invariant.
- It crosses a real architectural boundary.
- It makes genuinely complex logic easier to read, test, or maintain.

Examples of domain logic that may deserve a helper:

- type text formatting
- symbol lookup
- name mangling
- receiver shaping
- parameter shape conversion
- ABI decisions
- diagnostic construction
- constant evaluation
- constant validation
- backend-independent semantic checks
- backend-specific lowering rules

A new helper is not allowed when:

- it only renames an existing function
- it only forwards parameters unchanged
- it only returns another function's result unchanged
- it preserves an old signature while ignoring parameters
- it is used once and does not clarify genuinely complex logic
- it hides removed behavior
- it exists only to avoid updating call sites
- it duplicates logic already available elsewhere

When in doubt, prefer the existing canonical function.

---

## 5) Search-before-write rule

Before writing new logic, search for existing implementations.

Search for:

- same function name
- similar helper names
- same diagnostic message
- same type formatting logic
- same mangle/symbol logic
- same backend behavior
- same lowering behavior
- same validation behavior
- same test cases

Do not create new logic until you know whether a canonical implementation already exists.

If existing behavior is identical, reuse it directly.

If existing behavior is almost identical, consider whether the existing function should be extended or generalized instead of creating a second implementation.

---

## 6) One canonical implementation rule

If multiple phases or backends share identical logic, move it to a shared location.

Keep one canonical implementation for:

- type text formatting
- symbol lookup
- name mangling
- receiver shaping
- parameter conversion
- ABI decisions
- constant folding
- constant validation
- diagnostic text
- reusable semantic checks

Do not copy the same logic into several packages.

Do not fix the same bug in multiple call sites if it can be fixed at the source layer.

---

## 7) Compiler pipeline architecture

For compiler-flow work such as `parser`, `collector`, `resolver`, `typechecker`, `HIR`, `HIR lowering`, `MIR`, and `codegen`:

1. Keep the real phase chain. Do not collapse multiple phases into one ad-hoc function.
2. Keep phase outputs as explicit data models. If a phase exists in the architecture, represent it in code and handoff.
3. Do not fake artifacts. `.hir`, `.mir`, and backend IR must come from actual lowering of the previous phase model.
4. Do not hardcode/manual-output a sample case. Output must be generated from AST/semantic inputs.
5. If scope is intentionally limited, state exact boundary in code comments, local plan, and close-out notes.
6. If a request implies future constructs such as multi-function, calls, scopes, loops, arrays, slices, optionals, strings, ownership, allocator provenance, or IR architecture, design touched code to extend without rewrite.
7. Missing phase work must be tracked as an explicit TODO item in repo docs, issue tracker, or local plan notes with impact statement.

Do not satisfy compiler requests with temporary shortcut paths that bypass intended phase boundaries.

---

## 8) Naming and structure

Name functions by behavior, not by location or temporary intent.

Avoid vague names:

- `handle`
- `processData`
- `helper`
- `helper2`
- `doThing`
- `fixStuff`

Keep functions short and single-purpose.

Prefer flat code over deeply nested code.

Prefer data-driven logic over repeated copied `if` or `switch` blocks.

Add comments only when they explain:

- why code exists
- what invariant must hold
- what phase boundary matters
- what assumption future maintainers must preserve
- why an unusual implementation is intentional

Do not add comments that merely repeat obvious code.

---

## 9) Refactor safety rule

After renaming, removing, splitting, or merging functions:

- Re-check every edited function against its declared purpose.
- Verify the function body still matches its name.
- Verify the function body still matches its parameters.
- Verify the function body still matches its return type.
- Remove stale logic copied from the old function.
- Remove unused parameters.
- Remove obsolete wrappers.
- Update call sites directly.
- Run relevant tests.

Do not leave refactor debris.

### Bad

```go
func checkConstantArrayIndex(expr ir.Expr, diag *diagnostics.DiagnosticBag) {
	// diagnostic traversal logic...

	return ir.FoldExpr(expr, env)
}
```

This is invalid because:

- function has no return type
- `env` is not in scope
- function name says it checks diagnostics, not folds expressions
- folding logic leaked into a validation function

### Good

```go
func checkConstantArrayIndex(expr ir.Expr, diag *diagnostics.DiagnosticBag) {
	// diagnostic traversal logic only
}
```

Each function must keep one clear responsibility.

---

## 10) Change scope discipline

Keep diffs minimal and task-focused.

Do not refactor unrelated areas in the same change.

Do not scatter workaround code across multiple call sites.

Fix bugs at the source layer whenever possible.

Do not add conditional checks or special cases to hide a known bug.

Use a workaround only when explicitly approved and tracked with a follow-up removal task.

Remove dead code immediately after migration.

When removing or replacing a function:

- Search all call sites.
- Update call sites to the canonical implementation.
- Do not preserve the old function as a wrapper.
- Do not keep unused parameters.
- Do not keep obsolete behavior accidentally.
- Do not remove behavior silently.

---

## 11) Error handling and diagnostics

Preserve root-cause context in all error messages.

Use `%w`, not `%v`, when wrapping errors so callers can use `errors.Is` and `errors.As`.

Do not hide failures behind generic wrappers.

### Bad

```go
return fmt.Errorf("something went wrong")
```

### Good

```go
return fmt.Errorf("resolving import %q: %w", path, err)
```

Reuse shared diagnostic phrasing and constants where available.

Do not create slightly different diagnostic messages for the same failure.

Centralize repeated diagnostic construction when the same diagnostic is emitted from multiple places.

---

## 12) Panics vs errors

Return `error` for conditions callers are expected to handle.

Use `panic` only for internal invariant violations that indicate a compiler bug.

Use `panic` for:

- unreachable branches
- impossible IR states
- unexpected nil values in guaranteed-non-nil positions
- unhandled enum or node kinds that indicate incomplete compiler implementation

Return `error` for:

- bad user input
- missing files
- parse failures
- type mismatches
- invalid imports
- expected validation failures

### Correct panic

```go
default:
	panic(fmt.Sprintf("unhandled node kind %T in codegen", node))
```

### Correct error

```go
if tok.Kind != TokenIdent {
	return nil, fmt.Errorf("expected identifier, got %s", tok)
}
```

Never use `panic` as a substitute for proper error propagation.

Never silently ignore an internal invariant violation.

---

## 13) Go code style

See [go-style.md](./go-style.md) for all Go-specific idioms, linter rules, and code patterns.

At minimum:

- Run `gofmt` on all touched Go files.
- Keep imports clean.
- Prefer simple, idiomatic Go.
- Avoid clever code when straightforward code is clearer.
- Avoid global state unless truly necessary.
- Avoid package-level variables unless they represent immutable shared definitions or approved state.

---

## 14) Testing requirements

For behavior changes:

- Add or update focused tests near the changed subsystem.
- Add regression tests for bugs that previously failed.
- Validate both relevant backends when backend behavior is affected.
- Run targeted Peeper smoke/repro tests if language or runtime behavior changed.
- Run `go run ./scripts/bundle.go` with no args to bundle compiler and packaged libraries when packaging or bundled libraries may be affected.

A passing bundle run exits with code `0`, copies `_builtin_library` into `build/libs/`, and produces `build/bin/peeper`.

### When removing, renaming, or replacing a function

Verify:

- all call sites were searched
- call sites use canonical function directly where appropriate
- no stale wrapper remains
- no parameter is silently ignored
- old behavior was checked for diagnostics, validation, mutation, caching, logging, and invariant checks
- preserved behavior has tests
- intentionally removed behavior is explained in commit rationale
- tests prove intended behavior

Do not accept a refactor that only makes tests pass by deleting checks.

### Minimum validation before commit

- `gofmt` on all touched Go files.
- `go test ./...` for touched packages.
- Targeted smoke/repro test if language or runtime behavior changed.
- Backend validation when backend behavior is affected.
- `go run ./scripts/bundle.go` when compiler packaging or bundled libraries may be affected.

---

## 15) Commit hygiene

Write commit messages in imperative mood and present tense.

Good:

```text
Fix type resolution for nullable pointers
```

Bad:

```text
Fixed type resolution for nullable pointers
```

Bad:

```text
Fixes type resolution for nullable pointers
```

Rules:

- Keep subject line under 72 characters.
- Commit only relevant source, test, and documentation files.
- Do not commit generated binaries.
- Do not commit build artifacts.
- Do not commit temporary repro executables.
- One logical change per commit.
- Do not bundle unrelated fixes.
- Mention important behavior changes in the commit body.
- Justify any new helper in the commit body.

---

## 16) Branch protection

Do not put new feature implementations on `main` or `master`.

Use `feature/<name>` for new features.

Use `fix/<name>` for bug fixes.

Before starting work, check the current branch.

If task is a feature or bug fix and current branch is `main` or `master`, create the correct branch first.

---

## 17) Agent note

Agents must also follow [AGENTS.md](./AGENTS.md).

Keep agent workflow details in `AGENTS.md`. Keep code quality and architecture rules here.

---

## 18) Human review checklist

Before merge, verify:

- [ ] No pass-through wrappers were introduced.
- [ ] No old function was kept only as a renamed wrapper around a new function.
- [ ] No wrapper silently ignores parameters from an old signature.
- [ ] No stale local alias remains after refactor.
- [ ] All call sites were updated to use canonical functions directly where appropriate.
- [ ] No duplicated logic remains in touched areas.
- [ ] Shared logic was centralized when repeated.
- [ ] Existing diagnostics were preserved or intentionally moved/removed.
- [ ] Existing validation was preserved or intentionally moved/removed.
- [ ] Existing mutation/caching/logging behavior was preserved or intentionally moved/removed.
- [ ] Existing invariant checks were preserved or intentionally moved/removed.
- [ ] Any behavior removal is justified in commit rationale.
- [ ] Compiler phase boundaries are preserved.
- [ ] No fake `.hir`, `.mir`, or backend IR artifact was introduced.
- [ ] Error messages preserve root-cause context with `%w`.
- [ ] `panic` is only used for internal invariant violations.
- [ ] Tests cover changed behavior.
- [ ] Regression tests cover previous failure modes.
- [ ] Backend behavior was validated where relevant.
- [ ] `gofmt` passes.
- [ ] `go test` passes for touched packages.
- [ ] Bundle/smoke validation was run when needed.
- [ ] No unrelated files were included.
- [ ] No generated binaries or build artifacts were included.
- [ ] Commit message is imperative, specific, and under 72 characters.

---

## 19) Function replacement review checklist

Use this checklist whenever a function is removed, renamed, replaced, or simplified.

```text
Function replacement check:
- What function changed?
- What was old behavior?
- Did old function call another function directly?
- Did old function also perform diagnostics?
- Did old function also perform validation?
- Did old function mutate state?
- Did old function cache anything?
- Did old function log anything?
- Did old function protect an invariant?
- Did old function normalize or convert data?
- Did old function have backend-specific behavior?
- Did old function have phase-specific compiler behavior?
- Is any parameter now unused?
- If yes, why does that parameter still exist?
- Can call sites use canonical function directly?
- If yes, were call sites updated directly?
- If no, what real boundary requires keeping a wrapper?
- Was removed behavior preserved, moved, or intentionally deleted?
- What test proves intended behavior?
```

If this checklist cannot be answered clearly, refactor is not ready.

---

## 20) Golden rule

Do not make code look simpler by hiding behavior.

Make code actually simpler by removing stale layers, updating call sites, preserving important behavior, and keeping one clear canonical implementation.
