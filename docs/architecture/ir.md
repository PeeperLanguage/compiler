# IR: THIR, CFG, and MIR

Peeper's lowering path is:

```text
AST + semantic results
  -> THIR
  -> CFG + flow/effect/ownership evidence
  -> MIR
  -> LLVM backend
```

THIR is canonical typed source representation. CFG owns execution topology. MIR
owns flat executable operations. No intermediate HIR artifact exists.

## Source files

- `internal/ir/thir/model.go`: typed source nodes and semantic evidence.
- `internal/ir/thir/build.go`: AST/typechecker-result materialization into THIR.
- `internal/ir/thir/lower.go`: exhaustive expression dispatch contract.
- `internal/ir/cfg/model.go`: control-flow graph, sites, blocks, and terminators.
- `internal/ir/cfg/build.go`: CFG construction from THIR statements.
- `internal/ir/exprlower/lower.go`: THIR expression/place lowering into shared IR expressions.
- `internal/ir/typelower`: semantic-type lowering into runtime `ir.TypeID` values.
- `internal/ir/mir/model.go`: MIR model and text rendering.
- `internal/ir/mir/module_lower.go`: THIR/CFG/evidence-to-MIR module and statement lowering.
- `internal/ir/mir/expr_lower.go`: shared IR expression-to-MIR instruction lowering.
- `internal/ir/mir/validate.go`: MIR shape and transfer validation.

## Shared identities

- `ir.NodeID` preserves source-node identity across THIR, CFG, evidence, and MIR lowering.
- `symbols.SymbolID` identifies semantic storage and callable symbols.
- `cfg.SiteID` identifies one exact ordered program point.
- `ir.TypeID` identifies one runtime type in compilation-owned `ir.TypeTable`.

Node IDs join semantic meaning. Site IDs distinguish repeated scope exits or other
program points that share one source node.

## THIR

`thir.Build` runs after base typechecking. It does not infer names or types. It
materializes published semantic facts directly on typed nodes:

- resolved semantic type;
- symbol identity;
- implicit conversions and implicit-reference arguments;
- interface implementation evidence;
- value-use classification;
- canonical place roots and projections;
- ordered struct and call arguments;
- variant construction and match plans;
- range and sequence iteration plans.

`thir.Module.Node` indexes typed nodes by source identity. Later phases use this
index instead of retaining or rediscovering AST expressions.

THIR statement and expression sets are sealed. Operation-specific interfaces such
as `ControlFlowBuilder`, `FlowAnalyzer`, `EffectBuilder`, and `ExpressionLowerer`
provide compile-time exhaustiveness while keeping each consumer's policy in its
own package.

## CFG

CFG construction consumes THIR statements and owns:

- blocks and reachability;
- ordered sites;
- branches, loop edges, returns, and variant switches;
- scope-exit program points;
- block and site adjacency.

CFG does not own expression semantics or ownership cleanup. Flow, effects,
definite initialization, and ownership publish separate evidence keyed by CFG
and source identities.

## Runtime type lowering

`internal/ir/typelower` is sole semantic-type-to-runtime-type construction
boundary. Semantic types dispatch through `RuntimeTypeVisitor`; the lowering
package owns runtime representation policy and recursive interning.

Named aggregate shells are reserved before children are lowered, which preserves
recursive named layouts without permitting illegal direct by-value recursion.

## Expression and place lowering

`exprlower` converts THIR expressions into shared `ir.Expr` trees. It owns:

- numeric, optional, reference, struct, and interface conversions;
- calls, method calls, interface calls, and compiler intrinsics;
- aggregates and variants;
- borrowing and temporary-borrow lifetime markers;
- callable/symbol names;
- flow-refined case tests and payload projections;
- source origins.

THIR places retain each projection's source and base source identity. Lowering
replays flow payload refinements at original projection boundaries, then emits
dereference, field, index, and variant-payload projections in runtime order.
This avoids AST recursion while preserving flow-sensitive addressability.

`ir.FoldExpr` and `ir.FoldPlace` fold shared expressions immediately before MIR
instruction lowering. Source control-flow topology remains unchanged because CFG,
not folding, owns branches and loops.

## MIR lowering

`mir.GenerateMIR` receives one explicit `mir.LoweringInput`:

- shared runtime type table and diagnostics;
- THIR module;
- finalized CFG module;
- flow and ownership evidence;
- module scope and published constants;
- module identity and entrypoint status.

This input is a real phase boundary and avoids importing high-level module state
into MIR.

For each body-backed THIR function, MIR lowering follows finalized CFG blocks and
sites. It resolves each site through `thir.Module.Node`, lowers statement meaning,
and uses CFG terminators for execution order. Structured loops and matches are not
rebuilt:

- loop init/body/latch operations come from THIR iteration plans plus CFG block origin;
- match targets come from CFG while payload bindings come from THIR match arms;
- return, assignment, discarded-value, scope-exit, and match cleanup come only from
  ownership plans;
- expression trees lower through `exprlower`, then existing shared-IR-to-MIR logic.

External functions and body-backed functions share THIR signature lowering.
Published module constants become MIR static data before function lowering.

## Validation boundaries

- `thir.Module.Validate` rejects missing or malformed typed evidence.
- `cfg.Module.Validate` rejects malformed topology.
- effect and ownership result validators reject inconsistent analysis evidence.
- `mir.Module.Validate` rejects invalid runtime types, places, instructions,
  terminators, calls, and return shapes before backend emission.
- LLVM emission treats validated MIR violations as internal invariants.

## Adding a language construct

1. Add AST syntax and traversal where needed.
2. Publish semantic type/symbol/conversion evidence.
3. Add or extend canonical THIR node/evidence.
4. Implement each required THIR operation-specific method; missing support fails at compile time.
5. Extend CFG only when control topology changes.
6. Extend `exprlower` or direct MIR statement lowering when runtime materialization changes.
7. Validate MIR and every affected backend/target.
8. Add positive and negative source fixtures plus focused phase tests.
