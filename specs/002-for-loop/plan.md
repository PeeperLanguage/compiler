# Implementation Plan: for loop

Feature branch: `feature/for-loop`
Spec: `specs/002-for-loop/spec.md`

## Technical Context

- Pipeline: tree-sitter grammar → hand-written parser (`internal/frontend/parser`) → AST → resolver/typechecker/ownership (`internal/semantics`) → CFG (`internal/ir/cfg`) → HIR (`internal/ir/hir`) → MIR (`internal/ir/mir`) → LLVM backend (`internal/backend/llvm`). CFG is built before HIR.
- Canonical loop model: `ast.ForStmt` plus semantic `ForIteration` evidence → CFG init/header/body/latch/exit topology → `hir.For{Init, Cond, Bindings, Body, Next}` → MIR blocks.
- Semantic evidence owns hidden loop symbols. CFG owns control-flow sites and exact `cfg.SiteID` values; generated HIR loop segments do not invent AST sites.
- MIR maps CFG `BlockLoopInit`, `BlockLoopBody`, and `BlockLoopLatch` origins to HIR `Init`, `Bindings`, and `Next` respectively.

## Constitution Check

- No pass-through wrappers or stale aliases: use existing AST, semantic evidence, CFG, HIR, and MIR models directly.
- No duplicated lowering: semantic analysis resolves iteration shape once; CFG owns transfers; HIR materializes resolved evidence; MIR executes segments by block origin; backend consumes MIR.
- Behavior preservation: condition, infinite, for-in, break, continue, ownership, and cleanup all retain phase boundaries.
- Change scope: implement and validate loop behavior only; do not add a parallel for-in IR or backend path.

## Phase 0: Research (resolved)

See `research.md`. Key decisions:

1. `ast.ForStmt.Cond` and `Iterable` are mutually exclusive; both nil means infinite loop.
2. Typechecker-owned `ForIteration` evidence owns hidden symbols for lowering.
3. CFG is built before HIR with canonical init/header/body/latch/exit topology.
4. `continue` targets latch and `break` targets exit after lexical scope exits.
5. Generated HIR segments have no AST sites; MIR schedules them by CFG block origin.
6. Sequence iteration keeps a loop-lifetime shared borrow, requires addressable owner arrays, and rejects move-only elements.

## Phase 1: Design

See `data-model.md` and `quickstart.md`.

### Steps (each stops for review)

**Step 1 — Parser and AST**

- Add `ast.BreakStmt` and `ast.ContinueStmt`; extend `ast.ForStmt` with flat `Index`, `Value`, and `Iterable` fields.
- Enforce `Iterable`/`Cond` mutual exclusion; both nil represents an infinite loop.
- Parse condition, single-binding, and index/value forms. Reject deferred labels clearly.
- Validate with parser unit tests.

**Step 2 — semantic evidence and ownership**

- Resolver registers source index/value bindings in body scope and validates loop-control nesting.
- Typechecker accepts exclusive bounded ranges, fixed arrays, dynamic arrays, and slices; rejects direct strings and non-iterables.
- Populate `ForIteration` with source bindings and hidden carrier/cursor/end/ordinal symbols.
- Use target `usize` for sequence cursor and exposed index; keep range ordinal/index as source-default `i32`.
- Require owner arrays to be addressable; retain sequence backing storage through a loop-lifetime shared borrow; reject conflicting mutation and moved iterables.
- Reject move-only sequence elements instead of deferring owned-element behavior.
- Validate with typechecker and ownership tests plus negative fixtures.

**Step 3 — CFG topology and cleanup sites**

- Build canonical init/header/body/latch/exit blocks before HIR.
- Route body fallthrough and `continue` through latch; route latch to header; route `break` to exit.
- Emit lexical scope-exit sites before loop-control jumps.
- Keep cleanup plans keyed by exact finalized `cfg.SiteID` so equal scope IDs on distinct paths do not collide.
- Validate topology, nested targets, unreachable recovery, and cleanup-path sites with CFG/ownership tests.

**Step 4 — HIR and MIR lowering**

- Lower every loop to `hir.For{Init, Cond, Bindings, Body, Next}`.
- Range `Init` evaluates bounds once; sequence `Init` captures shared carrier and zero `usize` cursor.
- Sequence `Bindings` copies hidden `usize` cursor directly to exposed `usize` index and copies element from indexed storage.
- Do not assign AST node/site IDs to generated `Init`, `Bindings`, or `Next` statements.
- MIR executes generated segments from CFG `BlockLoopInit`, `BlockLoopBody`, and `BlockLoopLatch` origins, then lowers ordinary body statements through CFG sites.
- Validate HIR shape, MIR segment placement, target-width cursor operations, and exact-site cleanup.

**Step 5 — fixtures and end-to-end validation**

- Positive runtime fixtures cover range, fixed array, dynamic array, slice, break/continue, nested loops, and cleanup on normal, continue, and break paths.
- Negative fixtures cover non-iterable, direct string, inclusive/unbounded range, break/continue outside loops, temporary owner arrays, moved iterables, mutation under loop borrow, and move-only elements.
- Validate each fixture from its project root with bundled `build/bin/peeper`, then run full Go tests.

## Risks

- Skipping `Next` on `continue`: prevented by dedicated latch target and MIR block-origin mapping.
- Cleanup collision between paths sharing one scope ID: prevented by exact `cfg.SiteID` keys.
- Borrow ending too early: sequence carrier loan must remain live until loop exit, including break/continue paths.
- Target-width mismatch: sequence cursor, length, projection index, and exposed index remain target `usize`; 386 and amd64 pipeline tests validate MIR and LLVM operands.
- Accidental owner-array move: owner arrays require addressable storage and are captured by shared reference.

## Validation commands

```sh
go test ./internal/...
go test ./x_test/
build/bin/peeper run x_test/for_range_loop
```
