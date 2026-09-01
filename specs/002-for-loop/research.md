# Research: for loop

## D1: Where does iteration evidence live?

Decision: typechecker-owned `project.ForIteration` evidence is keyed by source `ForStmt.NodeID` and owns resolved iteration kind, types, source bindings, and hidden carrier/cursor/end/ordinal symbols.

Rationale: semantic analysis has type and target information needed to choose range width, sequence carrier shape, target `usize` cursor/index, and range `i32` ordinal. HIR consumes this evidence instead of rediscovering iterable semantics.

Alternatives:

- parser desugaring: rejected because parser lacks semantic and target information
- HIR rediscovery: rejected because it duplicates semantic decisions
- generated statement/site IDs in evidence: rejected because evidence owns symbols, while CFG owns sites

## D2: What is canonical phase order and loop shape?

Decision: build CFG before HIR. CFG gives every lowered loop canonical init/header/body/latch/exit topology.

Rationale: ownership and cleanup depend on real control-flow paths before HIR/MIR lowering. Distinct init, body, and latch origins provide stable execution points for generated loop segments.

```text
entry → init → header ─true→ body → latch → header
                  └false→ exit
```

Alternatives:

- header/body/exit with body-to-header back edge: rejected because `continue` would skip increment
- HIR-first synthesized CFG sites: rejected because generated loop operations are not AST statements

## D3: How are generated HIR segments executed?

Decision: represent all loops as `hir.For{Init, Cond, Bindings, Body, Next}`. Generated `Init`, `Bindings`, and `Next` statements have no AST sites. MIR executes them from CFG `BlockLoopInit`, `BlockLoopBody`, and `BlockLoopLatch` origins.

Rationale: CFG is already built when HIR is generated. Inventing generated AST/Site IDs would create false source identity and conflict with CFG site ownership. Block origin plus loop `NodeID` gives MIR exact placement without a parallel lowering path.

Alternatives:

- a minimal loop shape without explicit generated segments: rejected because one-time evaluation, per-iteration binding, and latch work need explicit phase data
- new `hir.ForIn`: rejected because it duplicates loop lowering
- relying on ordinary AST-site MIR scheduling alone: rejected because MIR must schedule generated segments by CFG origin

## D4: How do break, continue, and cleanup interact?

Decision: CFG converts AST `break`/`continue` to existing jumps through a loop-target stack. `continue` targets latch; `break` targets exit. Required lexical scope-exit sites precede each transfer.

Rationale: latch must execute `Next` before returning to header. Cleanup can differ at multiple exits that share one scope identity, so ownership plans and MIR lookup use exact finalized `cfg.SiteID`, not scope or AST node ID alone.

Alternatives:

- bypassing latch on `continue`: rejected because it skips `Next`
- HIR-level break/continue nodes: rejected because CFG already owns transfer resolution
- cleanup keyed only by scope ID: rejected because distinct CFG sites would collide

## D5: What are sequence ownership rules?

Decision: sequence iteration evaluates iterable once and keeps a shared borrow of backing storage for loop lifetime. Fixed and dynamic owner arrays must be addressable. Sequence elements must be implicitly copyable; move-only elements are rejected.

Rationale: carrier must remain valid across header, body, latch, `continue`, and `break` paths. Addressability prevents borrowing temporary owner storage. Rejecting move-only elements avoids implicit repeated moves from indexed places; users can iterate indexes and borrow elements explicitly.

Alternatives:

- move owner array into hidden carrier: rejected because for-in is non-consuming and source remains usable after loop
- defer owned-element behavior: rejected because current contract must reject it explicitly
- end shared borrow after init: rejected because generated loads continue through every iteration

## D6: What types do sequence indexes use?

Decision: sequence cursor and exposed index binding are target `usize`; range ordinal/index remains source-default `i32`.

Rationale: sequence lengths and physical indexes are target-sized, so narrowing the cursor could wrap while storage traversal continued. Direct `usize` binding keeps cursor, length, projection, MIR, and LLVM operands representable on both 32- and 64-bit targets. Range ordinal is ordinary source arithmetic and retains default integer wrapping semantics.

## D7: Which iterables are accepted?

Decision: bounded exclusive ranges (`start..end`), fixed arrays, dynamic arrays, and slice views. Direct strings are rejected with guidance to use `as_bytes()` or `as_chars()`. Maps and direct UTF-8 string iteration remain deferred.

Rationale: explicit string views preserve existing indexing semantics and avoid implicit encoding behavior.

## D8: Labels

Decision: grammar may accept labels, but parser rejects them with a clear "labels not supported yet" diagnostic.

Rationale: labeled control flow requires named-loop tracking through semantics and CFG; it is outside core loop scope.
