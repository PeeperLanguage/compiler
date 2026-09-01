# Data Model: for loop

## AST (`internal/frontend/ast/stmt.go`)

```go
type ForStmt struct {
    Index    *Ident // optional exposed index in index, value form
    Value    *Ident // set for for-in form
    Iterable Expr   // mutually exclusive with Cond
    Cond     Expr   // nil for for-in and infinite forms
    Body     *BlockStmt
}

type BreakStmt struct    { NodeIDHolder; Location *source.Location }
type ContinueStmt struct { NodeIDHolder; Location *source.Location }
```

Valid states:

- condition loop: `Cond != nil`, `Iterable == nil`
- infinite loop: `Cond == nil`, `Iterable == nil`
- for-in loop: `Cond == nil`, `Iterable != nil`
- `Cond != nil` and `Iterable != nil`: invalid AST state

## Semantic evidence (`project.ForIteration`)

`SemanticInfo.ForIterations` is keyed by source `ForStmt.NodeID`. Typechecker-owned evidence records resolved iteration kind and owns all hidden symbols needed by later lowering:

- `Kind`: range or sequence
- `ElementType`, `CarrierType`: resolved lowering evidence
- `Carrier`: hidden loop-lifetime sequence carrier
- `Cursor`: hidden range cursor or target-`usize` sequence cursor
- `End`: hidden exclusive range end
- `Ordinal`: hidden range ordinal when exposed index is requested
- `Index`, `Value`: resolved source symbols scoped to loop body

Evidence does not own or synthesize CFG site IDs. CFG creates and finalizes sites independently before HIR.

Sequence invariants:

- fixed and dynamic owner arrays must be addressable
- carrier retains a shared borrow for whole loop lifetime
- element type must be implicitly copyable; move-only elements are rejected
- hidden cursor is target `usize`
- exposed sequence index is target `usize`; range ordinal/index remains source-default `i32`

## CFG (`internal/ir/cfg`)

Canonical topology:

```text
maybe empty:      entry → init → header ─true→ body → latch → header
                                      └false→ exit
guaranteed entry: entry → init → body → latch → header ─true→ body
                                                     └false→ exit
```

- Origins are `BlockLoopInit`, `BlockLoop`, `BlockLoopBody`, `BlockLoopLatch`, and `BlockNormal` for exit.
- Loop blocks carry source loop `NodeID` so later lowering can find corresponding `hir.For`.
- Body fallthrough and `continue` target latch; latch targets header.
- `break` targets exit.
- Break/continue append required innermost-first lexical `SiteScopeExit` sites before their jump.
- Cleanup plans key `AfterScope` entries by exact finalized `cfg.SiteID{Block, Index}`.

## HIR (`internal/ir/hir`)

```go
type For struct {
    Init     *Block
    Cond     ir.Expr
    Bindings *Block
    Body     *Block
    Next     *Block
    NodeID   NodeID
    Location *source.Location
}
```

Range example, `for index, value in start..end`:

```text
Init:     cursor = start; end = end; ordinal: i32 = 0
Cond:     cursor < end
Bindings: index = ordinal; value = cursor
Body:     source body
Next:     cursor = cursor + 1; ordinal = ordinal + 1
```

Sequence example, `for index, value in array`:

```text
Init:     carrier = shared-reference(array); cursor: usize = 0
Cond:     cursor < len(carrier)
Bindings: index: usize = cursor; value = load carrier[cursor]
Body:     source body
Next:     cursor = cursor + 1
```

Generated `Init`, `Bindings`, and `Next` statements carry source location for diagnostics but zero AST node/site identity. MIR source-statement indexing excludes zero IDs; generated segments remain reachable only through parent loop identity and CFG block origin.

## MIR / LLVM

MIR lowering is loop-aware:

- CFG `BlockLoopInit` executes `hir.For.Init`
- CFG `BlockLoopBody` executes `hir.For.Bindings` before AST-site body statements
- CFG `BlockLoopLatch` executes `hir.For.Next`
- header lowers `hir.For.Cond`; jumps preserve CFG topology
- `SiteScopeExit` cleanup uses exact `cfg.SiteID`

LLVM consumes resulting MIR normally; no second backend-specific for-in implementation exists.
