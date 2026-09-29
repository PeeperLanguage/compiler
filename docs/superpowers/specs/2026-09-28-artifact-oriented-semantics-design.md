# Artifact-Oriented Semantics Design

## Status

Proposed architectural migration for Peeper's semantic pipeline.

Branch: `feature/artifact-oriented-semantics`

Base: `5ca24b57` (`Fix type table descriptor publication`)

## Intent

Peeper should be maintainable by one person without requiring a repository-wide mental model for ordinary compiler work. A developer following one source expression or function through the compiler should encounter a small number of durable representations with obvious ownership, not a new persistent `Result` object for each pass.

The migration therefore changes the organizing rule from:

> each compiler phase publishes its own result object

into:

> persistent objects represent durable compiler concepts; passes are operations over those concepts and keep temporary state private.

The intended high-level flow is:

```text
AST
 ↓
collect / bind / resolve
 ↓
THIR
 ↓
CFG
 ↓
Analysis
 ↓
MIR
 ↓
LLVM
```

`Module` remains the owner of current-generation artifacts. Producers and consumers should depend on the narrow artifact they need instead of treating `Module` as a semantic service locator.

## Goals

1. Remove persistent `typecheckresult.Result`, `flowresult.Result`, `effect.Result`, `ownershipresult.Result`, and `constantresult.Result` as module lifecycle objects.
2. Make THIR the single durable output of base typechecking.
3. Make one `analysis.Module` the durable output of CFG semantic analysis.
4. Keep effect extraction and definite-initialization state transient inside analysis execution.
5. Keep published module constant values with generation-owned symbol state; keep evaluator query caches local to an evaluator run.
6. Reduce pipeline lifecycle states so they correspond to durable artifacts rather than internal analysis steps.
7. Preserve current diagnostics, validation boundaries, semantic behavior, deterministic identities, and incremental invalidation correctness throughout the migration.
8. Leave the compiler in a working, testable state after each migration slice.

## Non-goals

This migration does not:

- implement function-level cache hits or persistent caches;
- cache THIR, CFG, flow, effects, or ownership across generations in a new format;
- introduce a generic `Pass`, dependency-injection framework, or universal semantic database;
- combine AST, THIR, CFG, analysis, and MIR into one god object;
- eliminate side tables merely because they are side tables;
- remove the current visitor/double-dispatch boundaries for closed semantic sets;
- redesign the type system, ownership rules, CFG topology, or MIR semantics;
- split or merge unrelated frontend packages solely for package-count reduction.

Incremental function reuse is revisited only after the durable artifact model is simpler.

## Design rule: artifact, operation, or scratch state

Every piece of semantic state must fit one of three categories.

### Durable artifact

A representation that has an independent semantic meaning and is consumed by multiple later operations.

Examples:

- AST
- THIR
- CFG
- `analysis.Module`
- MIR
- backend output

A durable artifact may live on `module.Module` and participates in phase retention/reset.

### Operation

A compiler step that transforms or analyzes artifacts. An operation does not automatically receive a module field or result package.

Examples:

- base typechecking
- constant finalization
- flow analysis
- effect extraction
- definite-initialization checking
- ownership checking

### Scratch state

Temporary maps, worklists, caches, and evidence needed only while an operation runs. Scratch state remains private to its owning package/function and does not participate in module lifecycle management.

Examples:

- typechecker AST-to-THIR evidence while typechecking is in progress;
- constant evaluator lazy query cache;
- effect stream used between analysis passes;
- definite-initialization lattice states and worklists.

The default is operation-local scratch state. New persistent module fields require a durable-artifact justification.

## Target module shape

The semantic artifact portion of `module.Module` should converge toward:

```go
AST      *ast.Module
THIR     *thir.Module
CFG      *cfg.Module
Analysis *analysis.Module
MIR      *mir.Module
LLVMIR   string

ModuleScope *symbols.Scope
SymbolIndex *symbols.Index
```

`ModuleScope` and `SymbolIndex` remain separate for this migration. Whether they later become one semantic-environment object is an independent decision and is intentionally out of scope.

These current fields disappear:

```go
Constants    *constantresult.Result
Typechecking *typecheckresult.Result
Flow         *flowresult.Result
Effects      effect.Result
Ownership    ownershipresult.Result
```

## Base typechecking publishes THIR

### Current problem

Base typechecking currently publishes a cross-package `typecheckresult.Result`, after which THIR construction re-joins AST nodes, symbol state, and typechecker evidence. The intermediate result contains base types, call decisions, match/iteration plans, generated/default-expression state, AST references, symbols, and types.

That object is not independently meaningful after THIR exists. It also exposes generation-owned implementation details to later architecture and makes incremental reuse appear to require replaying a large collection of maps.

### Target ownership

The typechecker owns an unexported evidence store for the duration of one typecheck operation. It may remain map-based internally; side tables are acceptable scratch state.

The typechecker operation becomes responsible for producing validated THIR:

```text
AST + symbol state
       ↓
   typechecker
       │
       ├─ private evidence
       ├─ constant finalization
       └─ THIR publication
       ↓
      THIR
```

After the operation returns, no base-typechecking result object remains on `Module`.

### THIR construction placement

The AST-to-THIR builder should move under the typechecker ownership boundary rather than require an exported evidence package. THIR's model, traversal/dispatch contracts, and validation remain in `internal/ir/thir`; typechecker owns the construction step because THIR is the publication of typechecking decisions.

This avoids replacing `typecheckresult.Result` with a large exported “typing evidence” interface that would preserve the same cross-package maintenance burden under a different name.

### Type queries

After `Typechecked`, canonical base expression types come from THIR.

During typechecking, checks that need previously computed base types or expanded-default markers query the typechecker's private evidence directly rather than `module.Module`.

`Module.BaseExprType` therefore no longer falls back to `Typechecking`; it can be removed or reduced to a THIR query if a shared helper remains useful.

`Module.EffectiveExprType` becomes:

1. use a flow-refined type from `Analysis` when present;
2. otherwise use the canonical THIR expression type.

LSP consumers continue to use this effective query without depending on typechecker scratch state.

### Generated/default and iteration evidence

Generated default arguments and checked iterator expansions remain implementation details of base typechecking until their meaning is copied into THIR. Later phases consume THIR flags/plans, not typechecker markers or generated AST bookkeeping.

This is deliberately different from the previously considered function-cache design: generation-owned AST/scope/symbol pointers are not turned into a reusable snapshot at this stage.

## Constant values become symbol-owned semantic state

`constantresult.Result` currently combines two lifetimes:

- authoritative finalized module constant values;
- provisional lazy evaluator cache entries.

They should be separated.

### Published values

Finalized values are semantic facts about declarations for the current semantic generation. Store them in generation-owned symbol state, keyed by `symbols.SymbolID`.

The existing `symbols.Index` gains narrow operations such as:

```go
PublishConstant(SymbolID, constvalue.Value)
ConstantValue(SymbolID) constvalue.Value
ClearConstants()
```

The exact names may follow existing package conventions, but callers must not gain backing-map access.

This does not put mutable state on shared `*symbols.Symbol` objects. The values remain generation-owned, matching the activity-state migration already made for used/mutable-required symbols.

### Query cache

Lazy constant-query caching belongs to the evaluator instance. It is created for an evaluation/finalization operation and discarded with that operation.

MIR and cross-module constant lookup query the owning module's generation-owned symbol state rather than a separate `Constants` artifact.

## CFG analysis publishes one durable Analysis artifact

### Conceptual boundary

Flow typing, effect extraction, definite initialization, and ownership are all analyses of canonical THIR + CFG. They remain distinct algorithms but stop pretending to be four independent module artifacts.

The durable boundary is:

```text
THIR + CFG + symbol state
          ↓
      analysis.Run
          ↓
    analysis.Module
```

### What is durable

`analysis.Module` stores only semantic information needed after the analysis operation completes:

- path-sensitive expression type refinements;
- payload/case/variant refinements needed by lowering/tooling;
- storage/value origins and aggregate provenance needed by ownership/lowering;
- source-level cleanup/drop decisions needed by MIR.

The artifact is internally function-oriented where that matches semantics, but its backing maps stay private.

Later consumers query `analysis.Module` through semantic methods instead of importing separate result packages or indexing ownership maps directly.

### What is transient

Effect extraction is a producer/consumer protocol inside analysis. Its ordered operations are consumed by definite-init and ownership during `analysis.Run`, then discarded. It does not live on `module.Module` and does not need a public `effect.Result` lifecycle.

Definite initialization is diagnostic-only. Its lattice states, worklist, and per-site states are transient and are not retained in `analysis.Module`.

Ownership's internal loan/dataflow state is transient. Only cleanup decisions needed by MIR survive in `analysis.Module`.

### Package shape

The target is one coherent package, organized with files rather than one package per algorithm/result pair:

```text
internal/semantics/analysis/
    module.go
    flow.go
    flow_expr.go
    effects.go
    effect_ops.go
    definite_init.go
    ownership.go
    ownership_expr.go
    ownership_reference.go
    cleanup.go
    validate.go
```

Exact file splitting follows code size and existing responsibilities; the important rule is one package boundary for CFG semantic analysis.

The current packages/direct result types are retired as their responsibilities move:

```text
flowresult
ownershipresult
constantresult
```

The current `effect`, `definiteinit`, and `ownership` code becomes implementation within `analysis` rather than independent persistent phase architecture. Their algorithms and exhaustive effect visitor semantics are preserved.

### Public analysis API

Prefer semantic queries over exported storage types. Representative queries include:

```go
ExprType(NodeID) typeinfo.Type
Origins(NodeID) (OriginResolution, bool)
StorageOrigins(NodeID) []place.Origin
ValueOrigins(NodeID) []place.Origin
Payload(NodeID) (PayloadAccess, bool)
CaseTest(NodeID) (CaseTest, bool)
VariantField(NodeID) (VariantFieldAccess, bool)
AggregateSlots(NodeID) ([]AggregateSlot, bool)
```

For cleanup, MIR should ask the analysis artifact what cleanup applies rather than receive an exported `ownershipresult.Result` map. Representative APIs may be event-oriented:

```go
DropsAfterSite(FunctionID, cfg.SiteID) []symbols.SymbolID
DropsBeforeReturn(FunctionID, NodeID) []symbols.SymbolID
DropsBeforeAssign(FunctionID, NodeID) bool
DiscardedValue(FunctionID, NodeID) bool
ProjectionBase(FunctionID, NodeID) bool
MatchFieldDrops(FunctionID, NodeID) []int
MatchWholePayloadDrop(FunctionID, NodeID) bool
```

Exact signatures may be simplified during implementation if MIR's actual call sites support a smaller interface. The requirement is that map layout and cleanup-plan ownership remain inside `analysis`.

## MIR lowering becomes artifact-oriented

`mir.LoweringInput` should converge from separate semantic result dependencies toward:

```go
Source      *thir.Module
CFG         *cfg.Module
Analysis    *analysis.Module
Scope       *symbols.Scope
SymbolIndex *symbols.Index
```

Constant lookup comes through generation-owned symbol state.

`exprlower` similarly depends on `analysis.Module` for flow-refined facts instead of `flowresult.Result`.

MIR remains responsible only for lowering decisions that belong to MIR. It must not recreate ownership/drop policy.

## Pipeline phases

### Target lifecycle

Once artifact migration is complete, module lifecycle states should correspond to durable milestones:

```text
Setup
Load
Parsed
Collected
Bound
Resolved
Typechecked   // THIR exists and semantic export identity is finalized
CFG           // canonical graph + CFG diagnostics exist
Analyzed      // analysis.Module exists and semantic analyses/diagnostics completed
Usage         // project-wide usage barrier completed
MIR
Backend
Finalize
```

The current lifecycle-only states:

```text
FlowTyped
Effects
DefiniteInit
Ownership
```

are removed after their artifacts no longer need independent retention.

### Internal analysis order remains explicit

`analysis.Run` keeps the real dependency order visible in ordinary code:

```text
flow
 ↓
effect extraction
 ↓
definite initialization diagnostics
 ↓
ownership / cleanup publication
 ↓
analysis validation
```

This should be straightforward control flow, not a generic pass framework.

### Diagnostics and LSP retention

The migration must preserve diagnostic availability and incremental copying semantics. Analysis diagnostics become associated with the durable `Analyzed` boundary unless a diagnostic subsystem requires a more precise internal label.

If precise diagnostic grouping remains useful, use an analysis-internal diagnostic category rather than reintroducing module lifecycle artifacts solely to name diagnostics.

LSP retained-phase logic changes from retaining through `Ownership` to retaining through `Analyzed`.

## Error recovery and partial artifacts

The architecture must preserve the compiler's current error-tolerant behavior. A source diagnostic must not force the pipeline to discard useful typed/CFG/analysis structure that language tooling or later diagnostics can still consume safely.

- Typechecking may publish THIR containing explicit invalid/recovery nodes when source typing reports diagnostics, exactly as the current THIR builder does.
- CFG construction and analysis may proceed over recoverable THIR when their structural preconditions are satisfied.
- `analysis.Run` should return the useful durable facts it can prove even when it also emits source diagnostics; it must not publish structurally invalid state.
- Internal invariant failures remain validator errors, not ordinary source recovery.
- LSP/tooling queries must tolerate `Analysis == nil` or absent per-node refinements and fall back to THIR base facts where meaningful.

The migration must not improve architectural neatness by reducing diagnostics or tooling information available from invalid/incomplete programs.

## Validation

Validation remains a core invariant, not cleanup overhead.

The target boundaries are:

- THIR validates immediately after typechecker publication;
- CFG validates immediately after construction;
- analysis validates its durable refinements and cleanup decisions before publication;
- MIR validates after lowering.

Transient effect-stream validation may remain inside `analysis.Run` while the migration is underway and may stay permanently if it catches producer bugs cheaply. It does not require the effect stream to become a durable module artifact.

## Module reset and incremental invalidation

`Module.ResetToPhase` becomes simpler because fewer artifacts exist.

Target invalidation relationships:

- before `Typechecked`: clear THIR, CFG, Analysis, MIR, LLVM and semantic export fingerprint;
- before `CFG`: clear CFG, Analysis, MIR, LLVM;
- before `Analyzed`: clear Analysis, MIR, LLVM;
- before `MIR`: clear MIR and LLVM;
- before `Backend`: clear LLVM.

Generation-owned symbol state is reset at the semantic boundary exactly as today; published constant values are reset with that state.

No migration step may preserve a later artifact whose inputs have been invalidated.

## Dependency direction

The desired dependency direction is:

```text
frontend/ast
   ↓
semantics/{collector,binder,resolver,typechecker}
   ↓
ir/thir
   ↓
ir/cfg
   ↓
semantics/analysis
   ↓
ir/mir + ir/exprlower
   ↓
backend
```

Shared semantic primitives such as `symbols`, `typeinfo`, `place`, and IDs remain lower-level utilities.

Avoid new imports from durable IR models back into high-level producer packages. A model package may expose sealed dispatch/query contracts, but producer scratch state must not become part of the model's public API.

## Migration strategy

The migration is deliberately staged so each slice is reviewable and the compiler stays working.

### Slice 1 — Typechecking publishes THIR

- introduce unexported typechecker evidence;
- move AST-to-THIR construction under typechecker ownership;
- make typechecking return/publish `*thir.Module`;
- migrate typechecker internal queries away from `Module.Typechecking`;
- remove `Module.Typechecking` and `internal/semantics/typecheckresult`;
- update base/effective expression type lookup and affected tests/docs.

No function cache is introduced.

### Slice 2 — Constant state ownership

- move finalized constant values into generation-owned symbol state;
- move lazy query cache into evaluator-local state;
- remove `Module.Constants` and `constantresult`;
- update project/MIR constant lookup.

### Slice 3 — Introduce `analysis.Module`

- move flow result representation into `analysis`;
- make flow analysis populate private analysis state;
- expose only semantic analysis queries needed by ownership, MIR, expr lowering, LSP/tooling;
- keep current effect/ownership implementation callable while consumers migrate.

### Slice 4 — Internalize effects, definite-init, and ownership

- move effect algebra/building, definite-init, and ownership implementation under `analysis`;
- make effects transient inside `analysis.Run`;
- publish cleanup decisions into `analysis.Module`;
- remove `effect.Result` and `ownershipresult.Result` from module and public lowering inputs;
- retire the old result packages and redundant package boundaries.

### Slice 5 — Collapse lifecycle phases

- add `phase.Analyzed`;
- update scheduler, readiness, diagnostics, LSP retention, reset logic, and tests;
- remove `FlowTyped`, `Effects`, `DefiniteInit`, and `Ownership` as module lifecycle phases;
- update architecture documentation.

### Slice 6 — Revisit incremental function reuse

Only after the artifact model is stable, reassess function-level reuse against the new boundary.

The reuse question becomes whether typed semantic publication for an unchanged function can be reused or reconstructed safely, rather than how to replay an exported `typecheckresult.Result` and multiple phase result objects.

Any reuse design must still obey generation ownership of AST, scopes, symbols, and mutable semantic state.

## Testing strategy

Each slice follows RED→GREEN development and includes focused unit tests plus repository regression coverage.

Required behavioral classes across the migration:

1. base type and effective flow-refined type queries used by LSP;
2. generated default arguments and expanded-default addressability/ownership behavior;
3. built-in and custom `for` iteration lowering, including generated symbols/scopes;
4. match payload bindings, variant refinement, and partial ownership behavior;
5. cross-module constants and constant-evaluation cycle diagnostics;
6. definite initialization diagnostics and deterministic ordering;
7. ownership cleanup on scope exit, return, overwrite, discard, and match payloads;
8. MIR lowering of flow-refined expressions and cleanup events;
9. incremental reset/reuse across every remaining durable phase boundary;
10. malformed THIR/CFG/analysis/MIR validation paths.

At every slice, run package-focused tests first, then the full Go suite and race tests appropriate to the touched semantic packages. Existing source fixture tests remain a required end-to-end guardrail.

## Maintenance constraints

The final design should satisfy these practical solo-maintainer rules:

- Following ordinary compilation should require understanding the artifact path, not every pass's storage object.
- Adding a new internal analysis step must not automatically require a new module field, phase enum member, reset branch, result package, lowering input, and LSP retention rule.
- A new durable artifact is exceptional and requires a clear independent semantic identity.
- Backing maps remain private; consumers ask semantic questions.
- Prefer files within one coherent package over packages whose only purpose is to hold a `Result` struct.
- Do not hide meaningful ordering behind generic pass abstractions.
- Keep closed-family compile-time dispatch where it prevents omitted semantic handling; do not visitor-ize intentionally partial/contextual syntax logic.
- If a cleanup increases the number of concepts needed to trace one source construct, reject it even if the package graph looks theoretically cleaner.

## Success criteria

The migration is complete when:

1. `module.Module` no longer stores `Constants`, `Typechecking`, `Flow`, `Effects`, or `Ownership` result fields.
2. `typecheckresult`, `constantresult`, `flowresult`, and `ownershipresult` packages are gone.
3. Effect and definite-init state are internal to CFG analysis rather than durable module artifacts.
4. Base typechecking's durable output is THIR.
5. CFG semantic analysis's durable output is one `analysis.Module`.
6. MIR lowering consumes THIR + CFG + Analysis + symbol state, not a collection of phase result types.
7. Pipeline lifecycle no longer exposes FlowTyped/Effects/DefiniteInit/Ownership as retained module states.
8. LSP effective-type behavior, diagnostics, ownership semantics, source fixtures, full tests, and relevant race tests remain correct.
9. No new generic semantic database, pass framework, or compatibility facade is introduced solely to bridge the migration.
10. Function-level caching remains deferred until these boundaries are proven stable.
