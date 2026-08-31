# Spec: for loop

## Summary

Complete `for` loops with range and sequence iteration plus `break` and `continue`, while preserving condition and infinite loops through the canonical compiler pipeline.

## Contract

- AST supports condition, infinite, and for-in forms plus `break`/`continue`.
- `ast.ForStmt.Cond` and `ast.ForStmt.Iterable` are mutually exclusive. Both nil means an infinite loop; both non-nil is invalid.
- Semantic analysis records one `ForIteration` evidence value per valid for-in AST node. Evidence owns hidden carrier, cursor, end, and ordinal symbols; source index/value symbols remain body-scoped.
- CFG is built before HIR. Every loop uses canonical init/header/body/latch/exit blocks; typechecker proof may route init directly to body for a guaranteed first iteration.
- HIR represents loops as `hir.For{Init, Cond, Bindings, Body, Next}`.
- Generated `Init`, `Bindings`, and `Next` statements have no AST sites. MIR executes them by matching CFG `BlockLoopInit`, `BlockLoopBody`, and `BlockLoopLatch` origins.
- `continue` transfers to latch so `Next` executes; latch transfers to header. `break` transfers to exit. Both emit required lexical scope exits first.
- Ownership cleanup is keyed by exact `cfg.SiteID`, not only AST or scope identity.

## Requirements

### R1: for-in over ranges

```peep
for i in 0..10 { ... }
for index, value in 0..10 { ... }
```

- Range form is bounded `start..end` with exclusive end.
- Bounds are evaluated once in `Init` and retained in hidden semantic symbols.
- Value binding receives current range value. Optional exposed index is `i32` and counts from zero.
- Source loop bindings are fresh, immutable, body-scoped bindings for each iteration.

### R2: for-in over sequences

- Supported sequences are fixed arrays, dynamic arrays, and slice views.
- Iterable is evaluated once. Iteration retains a loop-lifetime shared borrow of backing storage.
- Owner fixed and dynamic arrays must be addressable; temporary owner arrays are rejected.
- Hidden cursor and optional exposed index use target `usize` for length comparison and indexing, avoiding narrowing while iterating target-sized storage.
- Elements are loaded under existing copy rules. Move-only elements are rejected; users must iterate indexes and borrow such elements explicitly.
- Direct strings are rejected with guidance to use `value |> as_bytes()` or `value |> as_chars()`.

### R3: break / continue

```peep
for ... { break; continue; }
```

- `break` exits innermost loop through exit.
- `continue` exits active body scopes and transfers to latch, never directly to header.
- Both are illegal outside a loop.
- Labels are deferred; parser rejects them with a clear diagnostic.

### R4: lowering and cleanup

- CFG precedes HIR and provides canonical init/header/body/latch/exit blocks.
- MIR runs generated HIR segments by CFG block origin because generated statements intentionally have no AST sites or synthesized site IDs.
- Scope-exit cleanup on normal fallthrough, `continue`, and `break` is selected by exact `cfg.SiteID`.

## Out of scope

- Labeled break/continue.
- C-style 3-clause `for i := 0; i < n; i += 1`.
- Map iteration and direct UTF-8 string iteration.
- Moving ownership-bearing elements out of a sequence.
- `while` keyword (use `for`).

## Validation

- Positive runtime fixtures in `x_test/`: range, fixed array, dynamic array, slice, break/continue, nested loops, and ownership cleanup on normal, continue, and break paths.
- Negative fixtures: non-iterable, direct string, inclusive/unbounded range, break/continue outside loops, non-addressable owner array, mutation during loop-lifetime shared borrow, use after move, and move-only sequence elements.
- Focused CFG/HIR/MIR/ownership tests verify topology, generated-segment execution, target-sized cursor/index lowering on 386 and amd64, and exact-site cleanup from source through MIR.
- Run `go test ./...` and fixture projects with bundled `build/bin/peeper`.
