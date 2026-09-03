# Change paths through the compiler

Mandatory policy is [`RULES.md`](../../RULES.md). Durable principles are
[`COMPILER_GUIDELINES.md`](../../COMPILER_GUIDELINES.md). The framework roadmap is
[`README.md`](README.md), whose *Adding or changing a language construct* matrix
lists the questions you must answer.

**This document answers a different question: where do I actually go?** The matrix
tells you that CFG needs a decision. It does not tell you that the decision lives in
`buildStmt`, that a missing one fails
`TestEveryStatementKindHasAPhaseDecision/buildStmt`, or that you can skip the backend
entirely. That is what follows.

Each walk is traced from a change already in git, so every stop is a real file that a
real commit touched — not a plausible guess. Line numbers drift; function names and
test names are the durable part.

## How to read a walk

Every stop names the **owner** (file and function), the **decision** made there, and
**what catches you** if you skip it. That last column ranks the same three ways the
framework does:

| Rank | Meaning |
| --- | --- |
| **Automatic** | The compiler will not build. You cannot forget. |
| **Visible** | A named test fails under plain `go test ./...`. |
| **Loud** | A validator or panic fires at runtime, naming the phase. |
| **— nothing** | Nothing catches you. Read this as a warning, not as permission. |

The `— nothing` rows are deliberate. A map with the gaps drawn in is more useful than
one that pretends the coast is clear.

---

## Walk 1 — adding a syntax construct

**Traced from `2302c08` "Implement and harden for loops"**: 19 production files, 11
test files, 14 `x_test` fixtures. `git show --stat 2302c08` is the ground truth for this walk.

`for` was a good stress test because it is not one construct but two — `for i in 0..n`
(range) and `for v in array` (sequence) — plus `break`/`continue`, which are transfers
rather than statements with an effect.

### The stops, in pipeline order

**1. Token** — `frontend/token/kinds.go`, `frontend/token/keywords.go`
Does the syntax need a new keyword or token kind? `for` already existed; this change
added only `in` — one entry in `keywords.go`, one `Kind` in `kinds.go`, one help line.
*Catches you:* — nothing. A missing token surfaces as a parse error in your own test.

**2. AST node** — `frontend/ast/stmt.go`
Declare the node, give it `NodeIDHolder` and a `Location`, and implement the family
marker (`stmtNode()`, `exprNode()`, or `typeNode()`). The marker is what enrolls your
node in every contract below — there is no registry to update, and no list to forget.
`ForStmt` holds `Index`, `Value`, `Iterable`, `Cond`, `Body`.

**3. AST traversal** — same file, `forEachChild`
Every field that holds a node must be visited. `ForStmt.forEachChild` visits all five.
*Catches you:* **Visible** — `contracts.TestEveryNodeBearingFieldIsTraversed` parses
this package with `go/ast` and fails naming the field you left out.
`TestEverySubStructureFieldIsExpanded` covers fields that hold sub-structures rather
than nodes directly.

**4. Parser** — `frontend/parser/parse_stmt.go`
Produce the node for valid syntax *and* decide what a malformed header produces. The
for-loop change added recovery paths deliberately; `x_test/negative_for_malformed_header`
pins the result.
*Catches you:* — nothing structural. Your own parser tests are the only guard.

**5. Resolver** — `semantics/resolver/resolver.go`, `resolveStmt`
Which names does the construct introduce, and in which scope? For `for`, the loop
variables get a body scope.

**6. Typechecker** — `semantics/typechecker/check_stmt.go`, `checkStmt`
The semantic rule, and — the important part — **the evidence you publish**. `checkStmt`
delegates to `checkForInStmt`, which publishes
`typecheckresult.Result.ForIterations[node.ID()]`: element type, the generated cursor
and value symbols, guaranteed-entry proof, and an `IterationPlan` holding the kind
together with that kind's own state.

> Publish evidence that cannot be malformed. `IterationPlan` is a closed interface, so
> a loop cannot claim one iteration kind while carrying another's state, and consumers
> need no defensive checks. Prefer this over a kind tag beside optional fields.


> **This is the load-bearing step.** Everything downstream consumes this evidence
> rather than re-reading the source. If you publish nothing here, every later phase
> either re-derives the fact — which the framework forbids — or silently does nothing.

**7. CFG** — `ir/cfg/build.go`, `buildStmt`
Which blocks, edges, and sites does the construct create? `ForStmt` builds
`BlockLoopInit`, `BlockLoop`, `BlockLoopBody`, `BlockLoopLatch` and wires
`break`/`continue` as edges.
If CFG construction needs a semantic fact, it takes it as a **query**, not by importing
the typechecker: `cfg.BuildQueries{MatchCases, LoopGuaranteedEntry}`.
`LoopGuaranteedEntry` exists exactly because a loop with a proven-nonempty range must
not report its body as conditionally skipped.
*Catches you:* **Loud** — `cfg.Module.Validate` reports `ICE0003` when the topology you
build is malformed: an unterminated reachable block, an edge kind disagreeing with its
terminator, an adjacency recorded from only one side, or a stale reachable flag.

**8. Flow typing** — `semantics/typechecker/flow.go`, `applyConditionEdge`
Which facts differ on the true and false edges? A `for` header carries no narrowing
condition, so it is classified `ignore` with that reason — an explicit decision, not
an omission.

**9. Definite initialization** — `semantics/definiteinit/initialization.go`, `checkReads`
Which storage does the construct read? Classified `ignore` for `ForStmt`: the condition
arrives separately through the CFG site condition, so reading it here would double-count.

**10. Ownership** — `semantics/ownership/ownership.go` (`applyStmt`) and
`semantics/ownership/reference.go` (`symbolUseSequence`)
Which loans, moves, and drops occur? `applyStmt` reads the published `ForIterations`
evidence to establish the sequence carrier's borrow. Cleanup lands in
`ownershipresult.CleanupPlan`, keyed to exact CFG sites.

**11. HIR lowering** — `ir/hir/lower/module_lower.go`, `appendStmt` → `lowerForStmt`
Represent the established evidence without rediscovering it. `hir.For` has explicit
`Init`, `Cond`, `Bindings`, `Body`, `Next` blocks; `lowerForStmt` fills them from the
published symbols. Note what it does **not** do: it never re-inspects the iterable's
type to decide range-vs-sequence — it switches on the published kind.
If you add a HIR node, it needs `forEachChild` and `appendText` there too.

**12. HIR folding** — `ir/hir/fold/fold.go`, `foldStmt`
Constant folding over typed HIR. `hir.For` is handled so folding descends into all
five blocks.
*Catches you:* — nothing. HIR has no dispatch contract and no validator.

**13. MIR lowering** — `ir/mir/module_lower.go`
Lower normalized control flow and consume the cleanup plan. `hir.For` is read in
`lowerCFGFunction` (to find the loop a CFG block belongs to) and in
`lowerCFGTerminator` (to emit the header and latch).
*Catches you:* **Automatic**, partly — `mir.Instr` and `mir.Terminator` are sealed by
unexported markers, so the set is closed to the `mir` package and an instruction can no
longer be used where a terminator belongs. What still catches nothing is forgetting to
classify a new node in the backend: MIR has no dispatch contract.

**14. Backend** — `backend/llvm/`
**The for-loop change touched no backend file at all.** This is the single most
useful fact in this walk: a construct that lowers to existing MIR shapes needs zero
backend work, because MIR is the backend's only input. You owe the backend a change
only when you introduce a new MIR instruction or terminator.
*Catches you if you do:* **Loud** — `GenerateLLVMIR` panics with
`LLVM emission: unhandled MIR instruction …` / `… unhandled MIR terminator …`, pinned
by `TestGenerateLLVMIRPanicsForUnknownMIRNodes`. A block with no terminator panics too.

**15. LSP** — `internal/lsp/`
The for-loop change touched no LSP file either. Revisit only if the construct
introduces a new completion or hover surface.
*Catches you:* — nothing.

**16. Fixtures** — `x_test/`
The for-loop change added 14: four runtime (`for_range_loop`, `for_array_loop`,
`for_nested_loops`, `for_break_continue`) and ten negative. A fixture is a directory
with `peeper.toml` plus `src/`, discovered automatically.
*Catches you:* — nothing forces you to add one; the suite passes without. RULES §14
requires end-to-end regressions for behavior changes, but that requirement is enforced
by review, not by a test. This is the largest honest gap in the pipeline.

### What Walk 1 forced automatically

Adding one `stmtNode` implementation enrolls the kind in
`TestEveryStatementKindHasAPhaseDecision`, which then fails at **all nine** statement
dispatch sites until each one either handles the kind or declares why it is inert:

```
resolveStmt · checkStmt · buildStmt · appendStmt · lowerElse
applyStmt · symbolUseSequence · checkReads · applyConditionEdge
```

An `exprNode` enrolls in `TestEveryExpressionKindHasAPhaseDecision` across four sites:
`resolveExpr`, `typeExprBase`, `checkExpr`, `lowerASTExpr`.

The families as they stand: **19** statement kinds, **23** expression kinds, **12**
type kinds. Type kinds have no dispatch contract yet.

"Declare why it is inert" means an entry in `internal/contracts/node_dispatch_test.go`
with one of four decisions — `traverse`, `ignore`, `reject`, `contextual` — and a
**reason string**. Reasons are checked: `TestOmissionReasonsNameRealNodeKinds` fails on
an empty reason, an invalid decision, or a reason naming a kind that no longer exists.
Claiming a kind is inert while also handling it fails too, so the classification cannot
rot in either direction.

---

## Walk 2 — an internal lowering or optimization change

This is the short path, and it is short because of a rule rather than a coincidence.

**Traced from the shape of `ir/hir/fold/fold.go`.**

| Stop | Owner | Decision |
| --- | --- | --- |
| HIR folding | `ir/hir/fold/fold.go`, `ApplyTypedExpressionFolding` → `foldStmt`, `foldBlock` | Constant propagation over typed HIR; must descend into every block a statement owns |
| MIR lowering | `ir/mir/module_lower.go` | Normalized control flow, temporaries, cleanup emission |
| Backend | `backend/llvm/` | Physical layout, instruction selection, ABI |

### The rule that makes this path short

> **Below HIR, no phase may re-derive a source-level fact.** Consume published
> evidence or fail.

Concretely: MIR lowering does not decide whether an assignment drops its target. It
reads `CleanupPlan.BeforeAssign`. It does not decide which match fields to destroy. It
reads `MatchFieldDrops`. When you find yourself reaching back toward the AST from MIR,
that is the signal you are in the wrong phase — the fact belongs in the typechecker's
or ownership's published result, and the change belongs in Walk 1.

Three drop channels have been deleted for breaking this rule: `hir.Return.Cleanup`,
`hir.Assign.DropTarget`, and `CleanupPlan.MatchCarrierMoves`. The first two let
lowering carry an opinion of its own; the third recorded an opinion nothing consumed.
`ownershipresult.CleanupPlan` is now the single source of planned drops.

### What catches you

| Concern | Guard |
| --- | --- |
| New MIR instruction or terminator | **Loud** — `GenerateLLVMIR` panics on an unclassified node |
| Block emitted with no terminator | **Loud** — panics: `LLVM emission: block bN has no terminator` |
| Operand/type mismatch in emission | **Visible** — `TestTypedLLVMBuilderRejectsOperandMismatches` |
| Ownership evidence inconsistent with CFG or types | **Loud** — `ICE0002` from `ownershipresult.Validate` at the phase boundary |
| Folding that drops a block | — nothing |
| Wrong MIR lowering that still type-checks | — nothing but fixtures |

---

## Walk 3 — adding a type

A new `typeinfo.Type` is the change shape with the **weakest** automatic coverage. The
contract added for AST type *syntax* does not help here: `typeinfo.Type` is the semantic
type model, a different family with no contract of its own. Read this walk as a
checklist you must run manually.

If your type also needs new syntax to write it, that syntax node joins the `typeNode`
family and `TestEveryTypeKindHasAPhaseDecision` will hold you to `TypeFromSyntax` and
the binder's `addTypeDeclEdges`.

**1. Declare it** — `semantics/typeinfo/types.go`
Implement `Type`: `TypeNode()` and `Text() string`.
*Catches you:* **Automatic** — the interface will not be satisfied otherwise. This is
the only automatic guard in the entire walk.

**2. Ownership capability** — `semantics/typeinfo/capabilities.go`
This is the step that decides whether your type is safe by default. The governing rule:

> Ownership capability is baked into the type itself. Scalar → copyable → copy.
> Contains a reference, pointer, or allocation inside → move. Check the type, apply
> the rule. No per-type policy tables.

Answer, in this file: `IsImplicitCopyType`, `noCopyType`, `NeedsDrop`, and the
composite `OwnershipCapabilityOf`. Also consider `IsSizedType`, `IsLowerableType`,
`IsEquatable`, `IsOrderable`, `IsArithmetic`, `IsIntegral`, `IsCondition`.
Get this right and ownership, cleanup, and drop emission follow with no further work —
that is the whole point of the capability model.
*Catches you:* — nothing. A `default:` branch will quietly classify your type as
non-copyable, which is safe but may be wrong.

**3. HIR type lowering** — `ir/hir/lower/lower_types.go`
The largest type switch in the compiler (~31 cases). Map your type to an `ir.TypeID`.
*Catches you:* — nothing; unmapped types fall to `ir.InvalidType`.

**4. Export fingerprint** — `project/export_fingerprint.go`, `semanticTypeKey`
Incremental correctness. If your type is not keyed distinctly, a dependent module can
fail to rebuild when your type changes.
*Catches you:* — nothing, and the failure is a stale-build bug that looks like
something else entirely. Treat this stop as high-risk.

**5. Backend layout and ABI** — `backend/llvm/`
Physical size, alignment, pointee, calling convention. Backend-owned by design; do not
push layout decisions into `typeinfo`.

**6. Hover and completion** — `lsp/hover.go` (~7 type cases)
*Catches you:* — nothing; the type renders with a fallback.

**7. Fixtures** — `x_test/`
Positive runtime plus negative semantics. For anything with a target-sized
representation, cover both 32- and 64-bit.

---

## Consolidated: what the compiler actually enforces

| Contract | Guards | Location |
| --- | --- | --- |
| `TestEveryNodeBearingFieldIsTraversed` | A new child field is traversed | `internal/contracts` |
| `TestEverySubStructureFieldIsExpanded` | Sub-structure fields are expanded | `internal/contracts` |
| `TestEveryStatementKindHasAPhaseDecision` | 19 statement kinds × 9 phase sites | `internal/contracts` |
| `TestEveryExpressionKindHasAPhaseDecision` | 23 expression kinds × 4 phase sites | `internal/contracts` |
| `TestEveryTypeKindHasAPhaseDecision` | 12 type-syntax kinds × 2 phase sites | `internal/contracts` |
| `TestOmissionReasonsNameRealNodeKinds` | Inert-kind reasons stay true | `internal/contracts` |
| `ownershipresult.Validate` → `ICE0002` | Published ownership evidence matches CFG and types | pipeline, after ownership |
| `llvm.ValidateRuntimeSymbols` | Reserved runtime symbols, extern ownership | pipeline, after backend emission |
| `GenerateLLVMIR` panics | Unhandled MIR node; block with no terminator | backend emission |
| `cfg.Module.Validate` → `ICE0003` | Block and site identity, termination, edge kind vs terminator, adjacency in both directions, reachability | pipeline, after CFG construction |
| `cfg.Analyze` | Unreachable code, constant conditions, missing return — **user diagnostics, not structure** | after CFG |

## Where nothing catches you

Stated plainly, because a contributor deserves to know which parts of the walk are on
the honor system:

- **Semantic type kinds have no dispatch contract.** AST type *syntax* is covered by
  `TestEveryTypeKindHasAPhaseDecision`, but adding a `typeinfo.Type` and forgetting
  capability, lowering, or fingerprinting still compiles and passes.
- **HIR and MIR have no dispatch contract.** `mir.Instr`/`mir.Terminator` are sealed,
  so the node set is closed and the two cannot be confused, but nothing proves the
  backend classifies every member; that is still a runtime panic.
- **No structural validator exists for HIR or MIR.** CFG topology and ownership
  evidence have boundary validators; the two lowered representations do not, so a
  malformed HIR or MIR artifact is caught only when the backend trips over it.
- **Nothing requires a fixture.** A construct can reach the backend with no end-to-end
  coverage at all.
- **Nothing requires an LSP update**, so a new construct can be invisible to hover and
  completion without any signal.

These are tracked as framework workstreams 3, 4, and 7 in [`README.md`](README.md). If
you close one, delete its bullet here — a stale gap list is worse than none.

---

## Before you call it done

1. Walk the matrix in [`README.md`](README.md) and mark every row **changed**,
   **verified unchanged**, or **not applicable with reason**.
2. Run the RULES §14 minimum validation, plus what this repo has settled into:
   `gofmt`, `go test -count=1 ./...`, race on touched packages,
   `go run ./scripts/bundle.go`, `PEEPER_BIN="$PWD/build/bin/peeper" go test ./x_test`,
   `git diff --check`. §14 also requires every supported target width when the change
   touches target-sized integers, lengths, indexes, pointers, or ABI carriers.
3. **Prove each new test is not vacuous.** Revert your fix, confirm the test fails, and
   confirm the failure message is the one you expect. A test that passes for a reason
   other than its own check is a false guarantee — which is precisely what this
   framework exists to eliminate.
