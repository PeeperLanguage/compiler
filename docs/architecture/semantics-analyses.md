# Semantics: typechecking and program analyses

This map records semantic-analysis implementation observed in source. Verify mutable details against linked symbols. It describes current behavior and handoffs without prescribing future sequencing or representation boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Scope and phase order

Current module sequencing is:

```text
typechecker.Check
  -> CFG construction and CFG diagnostics
  -> typechecker.CheckFlow
  -> effect.Build
  -> definiteinit.Check
  -> ownership.Check
  -> usage.Analyze
  -> HIR/MIR lowering
```

`internal/pipeline/pipeline.go` advances one phase at a time. Typechecking creates
base semantic evidence. CFG construction then creates immutable control topology.
Flow typing consumes both. Effects consume base typechecking evidence, flow-refined
expression types, bindings, and CFG sites. Definite initialization consumes only
published effects and CFG. Ownership consumes effects, flow origins, typechecking
match evidence, bindings, and CFG. Usage runs after ownership and reads symbol flags
and scopes.
The pipeline validates published effects immediately after building them. It validates
ownership output after ownership runs, but only when that phase has no source errors;
incomplete evidence on invalid source is not reported as an internal evidence error.
HIR and MIR are not semantic analyses: they consume the analysis artifacts. MIR uses
`ownershipresult.CleanupPlan` for source-level drops.

## Source inventory

Key implementation files: `internal/semantics/typechecker/{typechecker.go,check_fn.go,check_stmt.go,check_expr.go,check_call.go,assignability.go,errors.go,flow.go}`; `internal/semantics/typecheckresult/result.go`; `internal/semantics/flowresult/result.go`; `internal/semantics/effect/{model.go,build.go,validate.go,visitor.go}`; `internal/semantics/definiteinit/initialization.go`; `internal/semantics/ownership/{ownership.go,effects.go,expr.go,reference.go}`; `internal/semantics/ownershipresult/{result.go,validate.go}`; and `internal/semantics/usage/usage.go`.
Supporting non-test files: `internal/ir/cfg/{model.go,build.go,analyze.go}`,
`internal/pipeline/pipeline.go`, and `internal/ir/mir/module_lower.go`. Tests are not
listed; they assert contracts described below.

## Identity and evidence model

### AST `NodeID`

AST nodes have stable `ast.NodeID` values. Typechecking results use them as keys for
expression types, calls, conversions, match evidence, iteration evidence, variant
construction, value-use classification, and reference-argument classification.
Flow results use them for expression resolutions, payload access, case tests, variant
fields, and per-function site-fact containers. Cleanup entries keyed by an AST/HIR
program event use `ir.NodeID`, whose source identities are derived from AST IDs.
`NodeID` identifies source meaning or a source event. It does not identify an ordered
execution point. A return statement, assignment statement, match body, or discarded
expression can therefore be a `NodeID` key in a cleanup plan.

### CFG `SiteID`

`internal/ir/cfg/model.go` defines:
```go
type SiteID struct {
    Block int
    Index int
}
```
A `SiteID` identifies one ordered semantic program point within one CFG block. The
same pair is meaningful only inside its function graph. `cfg.Site` carries the site
kind, source `NodeID`, lexical `ScopeID`, and source location.
`cfg/build.go` assigns site IDs during `finalizeSites`. Statements, scope exits,
terminators, and join sites are ordered within blocks. Site edges connect adjacent
sites and then connect the final site to successor blocks. Edge kinds preserve normal,
true, false, return, and variant-case meaning. Consumers must use the immutable
published topology; a new topology generation must rebuild the CFG.
Analysis artifacts key function-local data by function `ir.NodeID` first, then by
`cfg.SiteID`. This outer function key is required because a site ID has no module-wide
meaning.

### Places and origins

Effects use `effect.Place`. A place has exactly one root: a resolved symbol or a
temporary expression ID, plus ordered `place.OriginProjection` values. The place
package owns selector/index decomposition. A place describes storage identity; it does
not replace evaluation of bases, indexes, or bounds.
Flow uses `place.Origin` values for storage and value provenance. Origins retain roots
and projections, including variant-payload paths. Ownership compares origins with
`place.OriginsOverlap`, and flow/ownership read resolved origins rather than
reconstructing selector/index meaning independently.

## Base typechecking

### Entry and checker state

`typechecker.Check` in `typechecker.go` creates a fresh `typecheckresult.Result`, then
runs `checker.checkModule`. `checker` holds compiler context, module, optional flow
state, a site-only flag, payload/optional-test context, whole-carrier context, loop
depth, and a reused generated-loop call.
`checkModule` performs three declaration walks:
1. Check function-type contracts, attributes, interface declarations, enum
   declarations, and reference-storage/type restrictions.
2. Check module-level `let` and `const` bindings.
3. Check each function or method, including receiver validation and its body.
The checker intentionally owns source-sensitive rules: type syntax, conversions,
call adaptation, default arguments, match case resolution, loop iteration plans,
reference-return contracts, and expression evaluation rules.

### Function and declaration checks

`check_fn.go` contains the function boundary checks:
- `checkFunction` binds parameter types, checks defaults, and checks the body.
- `checkDefaultParameters` enforces ordering, receiver restrictions, explicit default
  types, assignability, and owned-parameter reuse rules.
- `checkFunctionShape` checks extern representation, parameter shape, references,
  lowerability, and return shape.
- `checkCallableReturn` validates direct reference returns and their `from` sources,
  including source slots, duplicates, borrowed status, mutability, and lowerability.
- `checkFunctionTypeContracts` checks function-type syntax wherever it appears.
- `checkTypeDeclReferenceStorage` rejects unsupported stored-reference shapes in
  structs, aliases, and array/heap-owned type declarations.
- `checkInterfaceDecl`, `checkEnumDecl`, and `checkEnumPayloadType` validate declared
  semantic type forms and payload restrictions.
- `checkReceiverFunction` validates concrete local receiver targets.
- `checkDeclAttributes` validates attribute targets, arguments, conflicts, and extern
  body requirements.

### Statement checks

`check_stmt.go` dispatches statements. `checkBlock` resolves the block scope and
checks statements in source order. `checkStmt` handles blocks, bindings, returns,
conditionals, loops, breaks/continues, matches, expression statements, assignments,
and recovery/declaration cases.
Important statement evidence and rules:
- Returns type and validate returned values before ownership later checks provenance.
- `if` and loop conditions require condition types; CFG later records branch
  condition identity.
- `checkMatchStmt` resolves named enum cases, detects duplicates and missing cases,
  validates payload patterns, records `typecheckresult.Match`, and records each
  arm's `CarrierUse` as read or move.
- `checkAssign` checks target type, value type, temporary-borrow escape, mutability,
  const reassignment, reference assignment, and indexed sequence assignment.
- `checkBinding` handles declaration type and initializer requirements and publishes
  binding-related type evidence through the ordinary expression checker.

### Expression checks

`check_expr.go` contains the expression dispatcher and expression-specific typing.
`typeExpr` is the shared entry point. It calls `typeExprBase`, records the base type
through `Typechecking.RecordExprType` during the base pass, then calls
`effectiveExpressionType` when flow state exists. Recursive typing remains in
`typeExprBase`, so the two passes share one syntax dispatch and do not implement two
independent expression type systems.
The base dispatcher handles literals, identifiers, scope resolution, selectors,
indexes, struct/variant/array literals, unary/binary operators, `is`, calls, `free`,
`print`, `as`, and recovery expressions. Unsupported internal cases panic rather than
silently producing an omitted semantic case.
`assignability.go` owns compatibility recording and interface adaptation:
- `assignable` records non-identity implicit conversions and resolves implicit
  interface implementations where permitted.
- `resolveInterfaceImplementations` proves every required method and receiver shape.
- `storeInterfaceImplementations` publishes the selected implementations by node.
- `mutableAddressableExpr` delegates place/addressability decisions to `place`.
`check_call.go` owns call typing and call-specific evidence:
- `typeCallExpr` resolves effective arguments, method receivers, intrinsics, argument
  types, call checking, and return type.
- Intrinsic helpers type allocation, collections, dynamic array owners, and byte
  conversion calls.
- `publishValueUse` classifies call arguments as read, copy, or move from the
  parameter type and separately records reference-argument mutability.
- Default arguments are expanded once into `EffectiveCallArguments`.
The result is not only a type map. It is the source of downstream semantic decisions.

## `typecheckresult.Result`: base contracts

`internal/semantics/typecheckresult/result.go` defines the base evidence model.
Important types:
- `InterfaceImplementation` proves a declared method fills an interface slot.
- `CaseTest` records a subject, case, truth polarity, case count, and variant family.
- `Match` records subject, enum type, case count, and `MatchArm` values.
- `MatchBinding` records whole-payload or payload-field projection, field index,
  type, binding symbol, and discard state.
- `MatchArm.CarrierUse` tells ownership whether selecting the arm moves the carrier.
- `ForIteration` records guaranteed entry, generated symbols, element type, and a
  closed `IterationPlan`.
- `RangeIteration` and `SequenceIteration` hold plan-specific generated state.
- `VariantConstruction` records enum type, case, payload type, and source value.
- `StructFieldAccess` records an ordinary selector's field slot, physical field
  type, and any type crossed by implicit pointer/reference dereference. Physical
  field type remains distinct from flow-refined per-use expression type.
- `CompilerCall` records intrinsic operation and function kind.
`Result` maps `ast.NodeID` to:
- expanded default bindings and effective call arguments;
- interface implementations, implicit conversions, and implicit call argument types;
- compiler calls, string-concatenation decisions, ordinary struct fields, variant
  constructions, and case tests;
- matches, iteration plans, checked iteration rewrites, and expression types;
- `ValueUses`, classifying ownership-relevant value uses;
- `ReferenceArguments`, where presence means the call parameter borrows and the bool
  records mutability.
Queries such as `MatchCases`, `StringConcatenation`, `ValueUse`, `ReferenceArgument`,
`ArmBindings`, `ForLoopGuaranteedEntry`, `SequenceCarrier`, and
`CallArgumentsOrSource` expose narrow facts to later owners. They avoid making CFG or
effect code depend on the whole typechecker result shape.

## Base/flow two-pass typechecking

### Why two passes exist

Base typing resolves source expression types without path-sensitive variant facts.
CFG construction needs base typechecking evidence first: match case indexes, checked
iteration structure, loop entry facts, and source-to-CFG identities.
After CFG exists, `CheckFlow` reuses the checker to type expressions at each CFG site
with path state. It refines optional and variant payload types only when the current
path proves the required case. The second pass is not a separate AST walker: it uses
the same `typeExprBase` dispatch and adds `effectiveExpressionType` around it.

### Flow state and worklist

`flow.go` defines:
- `variantStateFact`: origin path, possible cases, case count, and symbol dependencies;
- `originStateFact`: storage origins and value origins;
- `flowState`: reachability, variant facts, reference origins, raw-pointer origins;
- `flowCheck`: result, current state, analyzer, and expression event log;
- `flowExpressionEvents`: ordered case tests and calls within one site;
- `flowAnalyzer`: function graph, function scope, site index, input states, and result.
`CheckFlow` initializes every `flowresult.Result` map, then analyzes each body-backed
function graph. Function parameters that are reference values seed present optional
layers and value origins. `flowAnalyzer.run` indexes all sites, seeds entry and
otherwise disconnected sites, and runs a worklist to a fixed point.
At each site, flow snapshots the incoming facts under the function node and `SiteID`,
then applies the site. Terminator edges refine the outgoing state using true/false
condition facts or variant case facts. States merge by intersection-like proof:
variant cases are merged conservatively, and reference/raw-pointer origin facts are
retained only where both paths provide matching storage facts. Calls invalidate facts
when mutable references or raw pointers can mutate relevant storage; mutable module
bindings also invalidate variant proofs.

### Flow expression typing

`effectiveExpressionType` resolves a place through `place.Resolve`, using current
reference origins, raw-pointer origins, call return-origin sources, constant indexes,
and payload cases. It records storage and value origins for every typed expression.
For optional values, it calculates required payload depth from the expected type and
unwraps only proven layers. An optional test preserves carrier type while recording a
case test. A use needing an unproven payload emits either an unstable-narrowing or
missing-presence-proof diagnostic. Stable place identity is required because a value
that can change between test and use cannot carry the proof.
`recordCaseTest`, `recordPayloadAccess`, and `recordFlowResolution` publish exact
source-node evidence. `resolveFlowPlace` delegates storage grammar to `place.Resolve`;
flow supplies state-dependent origin callbacks rather than peeling AST selectors and
indexes itself.
`applySite` uses `siteOnly` checking to replay typing without duplicating diagnostics
or source-wide traversal. It applies declaration and assignment origin transitions.
`applyVariantCaseEdge` restricts the subject to the selected case and installs payload
binding origins. `applyConditionEdge` reads the CFG terminator's condition identity
and applies logical `!`, `&&`, and `||` implications, invalidating proofs if ordered
calls intervene.

### `flowresult.Result`

`internal/semantics/flowresult/result.go` defines the published flow evidence:
- `VariantFact` stores carrier origins, possible cases, case count, and dependencies.
- `OriginFact` stores storage-to-value origin mappings.
- `Facts` groups variants, reference origins, and raw-pointer origins.
- `PayloadAccess` records carrier origins, payload case path, and direct-carrier status.
- `CaseTest` extends base case evidence with a payload path.
- `VariantFieldAccess` records carrier node, case, payload struct, field, and type.
- `Result.SiteFacts` maps function node then CFG site to snapshots.
- `ExprTypes` stores flow-refined expression types.
- `Payloads`, `CaseTests`, and `VariantFields` store path evidence.
- `ResolvedStorageOrigins` and `ResolvedValueOrigins` are the direct origin queries
  consumed mainly by ownership and later lowering/tooling.

## CFG contract

`cfg.BuildModule` in `internal/ir/cfg/build.go` creates one immutable graph per body-backed
function. It translates blocks, returns, branches, loops, breaks/continues, and valid
matches into blocks and terminators. Match topology is created only from typechecker
case evidence. Invalid match evidence receives a recovery site rather than invented
case tags.
`cfg.BuildQueries` accepts `MatchCases`, `LoopGuaranteedEntry`, and checked iteration
blocks. `finalizeGraph` marks reachability, rebuilds block topology, appends
terminator/join sites, assigns `SiteID`, and builds site edges.
`cfg.Analyze` in `analyze.go` reports unreachable statements, constant conditions, and
missing returns. It does not mutate finalized topology. Its output is diagnostic, not
an input lattice for flow or ownership.

## Semantic effects

### Effect model

`internal/semantics/effect/model.go` defines the closed `effect.Op` set:
- `Define`: binding enters existence; `Initialized`, `Value`, and `OnEntry` distinguish
  initializer storage from parameters and match payload bindings.
- `Write`: existing place is replaced or mutated; `Node`, `Owner`, and `Value` retain
  assignment identities.
- `Use`: a place is read, copied, or moved with a `UseKind`.
- `Borrow`: a place is borrowed shared, mutable, or raw; `Argument` marks call loans.
- `Iterate`: a sequence iterable receives a loop-lifetime shared access and carrier.
- `Discard`: a produced value is thrown away.
- `CallBegin` and `CallEnd`: bracket nested call evaluation and temporary lifetime.
`effect.Result` maps function `ir.NodeID` to `SiteOps`; `SiteOps` maps `cfg.SiteID` to
an ordered `[]Op`. Slice order is evaluation order and is semantic. `effect.Visitor`
is exhaustive. Adding an operation requires every consumer to implement its visitor
method.

### Effect production

`effect.Build` in `build.go` receives narrow `BuildQueries`: resolved symbols, scopes,
effective call arguments, match arm bindings, string-concatenation decisions, value
use kinds, expression types, reference arguments, and sequence carriers.
`builder.buildFunction` defines parameters at entry, defines match payload bindings at
the first arm-body site, and publishes every reachable site. `publishStmt` maps source
statements to operations:
- declarations publish initializer value effects then `Define`;
- assignments publish moved RHS effects then `Write`;
- expression statements publish a read then `Discard`;
- returns publish a moved return value;
- matches publish subject read;
- loops publish iterable read and, for sequence loops, `Iterate`;
- branch conditions are published from CFG terminators.
`value` is the single syntax-to-value-effect dispatcher. It handles identifiers,
address expressions, selectors, indexes, ranges, literals, calls, `free`, `print`,
unary/binary expressions, `is`, and `as`. It uses `place.Project`/`Decompose` for
place structure. `placeOperands` separately publishes evaluation of bases, indexes,
and bounds so a place operation does not hide evaluated operands.
Calls emit `CallBegin`, receiver/argument operations, then `CallEnd`. Reference
arguments emit `Borrow` instead of an additional `Use`; ordinary argument use kind
comes from typechecker evidence. String concatenation moves its left operand and reads
its borrowed right operand. A sequence loop's `Iterate` comes from the published
sequence-carrier query; ownership does not inspect loop syntax to rediscover it.

### Effect validation contract

`effect.Validate` checks artifact shape, not semantic meaning. It requires one outer
result entry per non-nil CFG function, then verifies site membership, operation node
categories, place roots, locations, call-bracket nesting, and operation identities.
An empty per-function operation map is valid. It deliberately does not decide whether
a source construct should have emitted an operation; that remains `build.go`'s
responsibility.

## Definite initialization

`internal/semantics/definiteinit/initialization.go` contains one analysis entry point:
`Check`. It iterates function CFGs and invokes `analyzeFunction` with that function's
`effect.SiteOps`.
The lattice is `state`, a set of initialized `symbols.SymbolID`. `transfer` replays
one site's operations in order. `intersectState` joins predecessor states, so a value
is initialized at a join only if every reachable predecessor initialized it. The
worklist terminates because transfer only adds initialized symbols and joins intersect.
`trackedSymbols` defines the diagnosable universe from `Define` operations. This
excludes enclosing bindings that the function does not define. `checkReads` replays a
site in deterministic declaration order, allowing an earlier same-site initialized
definition or whole-root write to cover a later read.
`initializationVisitor` is exhaustive over effects:
- `Define` records locally tracked symbols and initializes initialized defines;
- a whole-root `Write` initializes its root;
- a projected `Write` requires an already initialized root and never initializes it;
- `Use` and `Borrow` report reads of tracked symbols absent from state;
- `Iterate`, `Discard`, and call brackets do not change initialization.
Diagnostics identify the symbol and source location carried by the effect. This phase
never inspects AST syntax, so constructs that reuse `Define`, `Write`, `Use`, or
`Borrow` inherit the same initialization behavior.

## Ownership analysis

### Entry and state

`ownership.Check` requires AST, module scope, bindings, effects, and CFG. It creates an
empty `ownershipresult.Result` and one initialized `CleanupPlan` per function. It
rejects ownership-tracked module bindings, then checks each body-backed function with
its graph and effect map.
`ownership.go` defines analyzer state:
- `moved`: symbol to move site;
- `live`: ownership-tracked symbols currently owning a value;
- `pointers`: raw-pointer origins;
- `references`: reference loans held by symbols;
- CFG site indexes, worklist states, liveness sets, and cleanup plan.
`analyzer.run` first computes symbol liveness backwards, then plans dead match-carrier
cleanup, seeds parameter ownership and reference loans, and performs forward CFG
analysis. At joins, live sets must agree; mismatched ownership state reports an error.
Moved and raw-pointer facts merge conservatively. Reference loans merge by holder,
loan identity, path, mutability, and origins.

### Effects as ownership input

`ownership/effects.go` applies one site's ordered operations through
`ownershipEffectVisitor`. It captures reference provenance for values stored by
`Define` or `Write` before replaying effects, because evaluating a move can otherwise
erase the source state before the destination receives it.
- `applyDefineEffect` installs pointer/reference provenance and marks initialized or on-entry ownership values live.
- `applyWriteEffect` handles whole-root reinitialization versus projected mutation,
  checks storage access, and schedules replacement drops.
- `applyUse` detects use-after-move, then applies whole or projected use behavior.
- `applyBorrow` checks moved state, shared/mutable/raw behavior, and call reservations.
- `applyIterateEffect` installs the long-lived shared loan on a sequence carrier.
- `CallBegin`/`CallEnd` track nested call frames, reservations, and argument temporaries.
- `Discard` is consumed by cleanup planning rather than changing ownership state.
`UseRead` preserves a live owned value. `UseMove` records the move and removes it from
live state. `UseCopy` reports an invalid copy for tracked non-implicit-copy values.
Projected moves are rejected for move-only partial variant payloads and indexed
move-only elements; such values must be bound or borrowed.

### Loans, liveness, and provenance

`reference.go` defines `referenceLoan`, loan identity, holder/path, origin set,
mutability, loop lifetime, and `loanContext` partitions: persistent, temporary, and
reserved loans. Storage access kinds distinguish read, shared borrow, mutable
reservation, mutable borrow, mutation, consumption, and destruction.
`checkStorageAccess` obtains expression origins from flow and calls
`reportLoanConflict`. Conflicts compare overlapping origins and exclusive-access
requirements, then attach source labels for the conflicting loan and its keeping-alive
use or call. Mutable call arguments remain reservations until `CallBegin` activation.
Shared argument temporaries end at `CallEnd`. Sequence iteration loans end at loop exit;
returns release all iteration loans.
`computeSymbolLiveness` is a backwards CFG fixed point over published effects. It uses
`symbolUsesAndDefinitions` and `symbolUseSequence`, both effect visitors. A whole-root
write defines a symbol but also uses it when the old value may need dropping. On-entry
defines do not kill liveness because the incoming edge established them.
`referenceValueForExpr` interprets only accepted reference-bearing value shapes using
existing holder loans, flow origins, reference types, struct payload syntax, and
variant-construction evidence. `replaceReferenceField` updates one exact projected
reference slot while preserving sibling loans. `validateReferenceReturn` checks
returned reference origins against the function's declared `from` parameter sources.
`expr.go` owns small ownership-specific expression policies. `planProjectionBaseDrop`
plans cleanup for a dropping projection of a temporary, or reports that a dropping
projection must be bound. `pointerOrigin`, `updatePointerSymbol`, and
`checkPointerEscape` track raw pointers and reject returns that point to local storage.
These are intentionally ownership policies that cannot be represented by generic
value effects alone.

### Cleanup planning

`ownershipresult.CleanupPlan` is the only source-level drop obligation consumed by
lowering. Its channels are:
- `AfterScope map[cfg.SiteID][]SymbolID`: symbols dropped when one lexical scope exits;
- `BeforeReturn map[ir.NodeID][]SymbolID`: symbols from all unwound scopes, after return
  value evaluation;
- `BeforeAssign map[ir.NodeID]struct{}`: old dropping value before replacement;
- `DiscardedValue map[ir.NodeID]struct{}`: dropping temporary expression statements;
- `ProjectionBase map[ir.NodeID]struct{}`: dropping temporary projection bases;
- `MatchFieldDrops map[ir.NodeID][]int`: dropped unselected/discarded payload fields;
- `MatchWholePayloadDrops map[ir.NodeID]struct{}`: dropped discarded whole payload.
`applyBlockExit` checks destruction against live loans, computes reverse declaration-order
scope drops, records them by `SiteID`, and clears scope ownership. `cleanupBeforeReturn`
walks every enclosing scope and records drops by return `NodeID`. Scope exit and return
remain separate: a return evaluates its value before unwinding, while a scope-exit
site represents one ordinary scope event.
`ownershipresult/result.go` documents that source `free` is programmer-directed and
MIR temporary drops concern only temporaries materialized by MIR; these are not
competing source cleanup channels.

### Ownership result validation

`ownershipresult.Validate` checks published plan shape against typechecking, bindings,
and CFG evidence. It requires one cleanup plan per non-nil CFG function, while an
empty per-function plan remains valid. It checks function/CFG existence, scope-exit
`SiteID` keys, return and assignment site node keys, typed discarded/projection nodes,
match-body scopes, nonzero symbol IDs, and value-use capabilities. It does not re-prove
move/drop correctness; that is `ownership.Check`'s analysis and would duplicate policy.

## Usage analysis

`usage.Analyze` runs after ownership. It does not walk expression effects. It checks:
- unused imports in module scope;
- unused private module-level functions, types, constants, and bindings, excluding
  prelude symbols, `main`, public names, imports, and `_`;
- unused local variables, constants, parameters, and receivers from binding scopes;
- mutable bindings marked mutable but never modified, using `RequiresMutable` and
  `MutableLocation`.
The resolver/typechecker set symbol `Used`, `RequiresMutable`, and related symbol
state. Usage converts those flags into warnings with locations, replacement edits, and
help text. It is deliberately later than semantic validity and does not participate in
flow, initialization, ownership, or cleanup.

## End-to-end data flow

```text
AST + bindings + symbols
        |
        v
 typechecker.Check
        |
        +--> typecheckresult.Result
        |      types, calls, conversions, matches, loops,
        |      value uses, reference arguments, constructions
        |
        v
 CFG build/finalize
        |
        +--> immutable blocks, terminators, SiteID edges
        |
        v
 typechecker.CheckFlow
        |
        +--> flowresult.Result
        |      path cases, payload paths, storage/value origins,
        |      flow-refined expression types
        |
        v
 effect.Build
        |
        +--> effect.Result[function NodeID][SiteID][]Op
        |
        +--> definiteinit.Check: initialized-symbol lattice + diagnostics
        |
        +--> ownership.Check: move/borrow/liveness state + CleanupPlan
                                      |
                                      v
                         HIR/MIR lowering consumes planned drops
```
The stable identity rule is strict: `NodeID` carries source semantic identity and
source-event cleanup keys; `SiteID` carries ordered CFG execution identity. Effects,
flow facts, initialization states, ownership states, and cleanup plans must preserve
that distinction. Later phases consume published evidence instead of rediscovering
facts from syntax, as required by [`docs/compiler-architecture.md`](../compiler-architecture.md).
