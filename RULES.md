# Coding Rules

This file defines mandatory engineering rules for the `compiler` repository.
For Go-specific idioms and linter rules, see [go-style.md](./go-style.md).

---

## 1) Core Principle — No pass-through wrappers, no duplicated logic

Before creating any new function, search for existing similar code first.
Use existing functions directly if behavior is identical. Do not add a function that only forwards arguments or returns results unchanged.

**Why:**
- Increases maintenance cost
- Creates multiple sources of truth
- Causes inconsistent behavior across modules and backends

**Good:**
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

**Bad:**
```go
func getTotal(items []Item) int {
    return calculateTotal(items) // pointless wrapper
}
```
```go
display1 := fmt.Sprintf("$%.2f", price1) // logic duplicated at call sites
display2 := fmt.Sprintf("$%.2f", price2)
```

A new helper is allowed only if at least one is true:
- It removes repeated logic used in 2+ places.
- It centralizes domain logic that must stay consistent (mangling, symbol lookup, receiver shaping, type formatting, ABI decisions).
- It is needed to cross a real boundary (public API, interface contract, backend abstraction).

A new helper is **not** allowed when:
- It only renames an existing function.
- It only forwards params/return unchanged.
- It is used once and does not clarify genuinely complex logic.

If multiple backends or phases share identical logic, move it to a common utility. Keep one canonical implementation for:
- type text formatting
- symbol/mangle decisions
- receiver/parameter shape conversion
- repeated diagnostic text

---

## 2) Naming and structure

- Name functions by behavior, not by location or temporary intent.
- Avoid vague names: `handle`, `processData`, `helper2`.
- Keep functions short and single-purpose.
- Prefer flat code over deeply nested code.
- Prefer data-driven logic over repeated `if/switch` blocks copied across files.

---

## 3) Change scope discipline

- Keep diffs minimal and task-focused.
- Do not refactor unrelated areas in the same change.
- Do not scatter workaround code across multiple call sites — fix at the source layer.
- Do not add conditional checks or special cases to paper over a known bug. Fix the root cause. Use a workaround only when explicitly approved and tracked with a follow-up removal task.
- Remove dead code immediately after migration.

---

## 4) Error handling and diagnostics

- Preserve root-cause context in all error messages.
- Use `%w` (not `%v`) when wrapping errors so callers can use `errors.Is` / `errors.As`.
- Do not hide failures with generic wrappers.
- Reuse shared diagnostic phrasing and constants where available.

**Bad:**
```go
return fmt.Errorf("something went wrong") // loses all context
```

**Good:**
```go
return fmt.Errorf("resolving import %q: %w", path, err)
```

For internal invariant violations that should never happen in correct code, use `panic` with a clear message rather than a silent error return. Reserve `error` returns for conditions callers are expected to handle.

---

## 5) Panics vs errors

- Return `error` for conditions callers are expected to handle (bad input, missing file, type mismatch).
- Use `panic` for internal invariant violations that indicate a compiler bug (unreachable branches, unexpected nil in a guaranteed-non-nil position).
- Never use `panic` as a substitute for proper error propagation.

```go
// compiler bug — panic is correct
default:
    panic(fmt.Sprintf("unhandled node kind %T in codegen", node))

// caller-facing failure — return error
if tok.Kind != TokenIdent {
    return nil, fmt.Errorf("expected identifier, got %s", tok)
}
```

---

## 6) Go code style

See [go-style.md](./go-style.md) for all Go-specific idioms, linter rules, and code patterns.

---

## 7) Testing requirements

For behavior changes:
- Add or update focused tests near the changed subsystem.
- Add regression tests for bugs that previously failed.
- Validate both relevant backends when backend behavior is affected.
- Run `build.sh` (or the platform-specific equivalent) to bundle all compiler libs. The compiler will be packed into `build/core/bin/`. A passing run exits with code `0` and produces the binary.

Minimum validation before commit:
- `gofmt` on all touched Go files.
- `go test ./...` for touched packages — all tests must pass.
- Targeted Ember smoke/repro if language or runtime behavior changed.

---

## 8) Commit hygiene

- Write commit messages in imperative mood, present tense: `Fix type resolution for nullable pointers`, not `Fixed` or `Fixes`.
- Keep the subject line under 72 characters.
- Commit only relevant source, test, and doc files.
- Do not commit generated binaries, build artifacts, or temporary repro executables.
- One logical change per commit — do not bundle unrelated fixes.

---

## 9) Branch protection

- Do not put new feature implementations on `main` / `master`.
- Create a `feature/<name>` branch for new features.
- Create a `fix/<name>` branch for bug fixes.

---

## 10) Agent-specific requirements

Agents must:
- Search for existing implementations before writing new logic (follows §1).
- Reuse existing functions directly when behavior is identical.
- Justify any new helper in commit rationale.
- Avoid creating compatibility wrappers unless explicitly requested.
- Follow all rules in this file and all idioms in `go-style.md`.

---

## 11) Human review checklist

Before merge, verify:
- [ ] No pass-through wrappers were introduced.
- [ ] No duplicated logic remains in touched areas.
- [ ] Shared logic was centralized when repeated.
- [ ] Error messages preserve root-cause context with `%w`.
- [ ] `panic` is only used for internal invariant violations.
- [ ] Tests cover the changed behavior and any previous failure mode.
- [ ] `gofmt` and `go test` pass cleanly.
- [ ] No unrelated files or build artifacts were included.
- [ ] Commit message is imperative, specific, and under 72 characters.