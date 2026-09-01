# Semantic Result Ownership Inventory

Status: **design approved; migration active**.

This document records baseline `project.SemanticInfo` ownership before framework migration. It is the Step 1 design record for the [compiler framework roadmap](README.md). Facts below came from inspected producers, consumers, reset paths, scheduler prerequisites, and incremental LSP reuse. Approved migration progress is recorded below; baseline tables remain for rationale and traceability.

## Approved migration progress

Completed slices:

1. Parsed AST remains immutable during default expansion. `typecheckresult.Result.EffectiveCallArguments` stores source-plus-default argument evidence, and `Module.RebuildTypedASTIndex` indexes source and generated expression trees.
2. `Module.Typechecking` now owns one `typecheckresult.Result` per base-typecheck generation. It contains `ExpandedDefaultBindings`, `EffectiveCallArguments`, `InterfaceImplementations`, `ImplicitConversions`, and `ImplicitCallArguments`. These fields and `InterfaceImplementation` were deleted from `project.SemanticInfo`; no compatibility maps or aliases remain.
3. `typechecker.Check` publishes a fresh result, `resetToPhase` discards it below `Typechecked`, partial semantic consumers use its canonical effective-argument fallback, and HIR consumes its evidence strictly.
4. Intrinsic dispatch, string concatenation classification, and variant construction evidence moved into the same result. `CompilerCall` and `VariantConstruction` moved with their maps; eager constant evaluation treats a missing pre-typecheck result exactly like the previous empty proof map.
5. Base `CaseTests` and `Matches` moved into `typecheckresult.Result`, along with `CaseTest`, `Match`, `MatchArm`, `MatchBinding`, explicit match projections, and canonical `MatchCases` validation. `flowresult.Result.CaseTests` uses `flowresult.CaseTest`, which embeds base case evidence and owns flow-only payload paths.
6. Base `ExprTypes` moved into `typecheckresult.Result`. `Module.BaseExprType` is canonical base lookup; `Module.EffectiveExprType` gives flow evidence precedence and falls back to base evidence. `flowresult.Result.ExprTypes` remains distinct flow-refined evidence.
7. Staged collection, binding, resolution, and type-dependent symbol evidence moved into `bindingresult.Result`: `BlockScopes`, `NodeSymbols`, `MethodsByReceiver`, `MethodsByDecl`, and `OperationFunctions`. Generated defaults and selectors write the same canonical node-symbol table; no precedence accessor or duplicate map exists.
8. `SemanticInfo` was deleted. Constant storage moved directly to `Module.ConstValues` without changing behavior or lifetime. Dedicated constant migration remains: finalized module constants and mutable query cache still require separate ownership.
9. Typechecker evidence cleanup removed redundant interface method name/owner keys, replaced copied match case descriptors with `CaseCount`, moved `PayloadPath` to flow-owned evidence, and made match field/whole-payload projection explicit with an invalid sentinel consumed exhaustively.

All dedicated typechecker and staged binding fields/models have explicit owners. Constant-result separation remains unmigrated.

## Current lifecycle

`collector.collectModule` calls `Module.ResetSemanticData`, publishing fresh
`Module.Bindings` and `Module.ConstValues` at start of one semantic generation.
Collector, binder, resolver, and typechecker stage one shared binding/scope graph;
constant evaluation and later CFG/flow/HIR queries mutate the separate constant map.

`Module.resetToPhase` follows approved production contract:

```text
retained <= Parsed  -> clear ModuleScope, Bindings, ConstValues, and later results
exact later reuse   -> retain completed artifacts without phase re-entry
```

Intermediate semantic re-entry remains unsupported because shared symbol objects and
constant cache cannot be rewound independently. Production invalidation uses exact
artifact reuse or resets to `Parsed`.

## Baseline field ownership matrix

This table records pre-migration storage and problems; current ownership is tracked in Approved migration progress above.

| Field | Baseline writers / complete phase | Main consumers | Baseline contract problem |
| --- | --- | --- | --- |
| `BlockScopes` | resolver / `Resolved` | typechecker, CFG constant evaluation, flow, definite-init, ownership, usage, HIR, LSP | Scope topology is resolver-owned, but contained symbols later gain types, `Used`, and `RequiresMutable` state consumed by usage/HIR. |
| `ResolvedSymbols` | collector, resolver, typechecker / `Typechecked` | typechecker, semantic fingerprint, flow, definite-init, ownership, HIR, LSP | Name suggests resolver result, but enum declarations, selectors, and expanded defaults have different writers. |
| `ExpandedDefaultBindings` | typechecker / `Typechecked` | typechecker, ownership, HIR | Marker requires paired symbol provenance, with copied type/lowering evidence where available. Parsed reset can delete marker while retaining expanded AST. |
| `ExprTypes` | typechecker / `Typechecked` | typechecker, const evaluation, flow, ownership, HIR, LSP | Base type is distinct from `Flow.ExprTypes`; direct base-map reads coexist with `EffectiveExprType` precedence. |
| `CaseTests` | typechecker / `Typechecked` | const evaluation and flow transfer | Uses `flowresult` type but is stored in base semantic aggregate; flow creates second case-test map. |
| `Matches` | typechecker / `Typechecked` | CFG, flow, definite-init, ownership, HIR | Uses `flowresult` type despite being base typechecker evidence. Presence can coexist with some diagnostics. |
| `ConstValues` | constant evaluation, post-typecheck finalization, later constant queries / no global completion phase | const evaluator cache, semantic fingerprint, CFG and HIR expression evaluation, MIR | One map mixes finalized module constants with working-cache entries that can still appear during CFG/HIR. |
| `MethodSets` | collector membership; binder/resolver/typechecker mutate symbols / `Typechecked` symbol state | typechecker, semantic fingerprint, LSP | Catalog ownership differs from mutable `Type`, `Used`, `Initializing`, and `RequiresMutable` state of symbols inside it. |
| `MethodSymbol` | collector mapping; binder/resolver/typechecker mutate symbol / `Typechecked` symbol state | binder, resolver, typechecker, flow, ownership, HIR, LSP | Stable declaration identity is collection output; pointed-to mutable symbol state advances later. |
| `InterfaceImplementations` | typechecker / `Typechecked` | HIR | Clear typechecker proof; strongest first extraction candidate. |
| `ImplicitConversions` | typechecker / `Typechecked` | HIR | Clear typechecker proof. |
| `ImplicitCallArguments` | typechecker / `Typechecked` | typechecker borrow checks, HIR | Clear typechecker call-adaptation proof. |
| `CompilerCalls` | typechecker / `Typechecked` | HIR | Clear typechecker dispatch proof; entry may coexist with later call diagnostics. |
| `StringConcatenations` | typechecker / `Typechecked` | ownership, HIR | Clear typechecker operation-classification proof. |
| `VariantConstructions` | typechecker / `Typechecked` | const evaluation, flow, ownership, HIR | Clear typechecker construction proof. |
| `OperationFunctions` | binder append and sort / `Bound` | LSP completion | Binder-owned catalog derived from collected top-level function symbols. |

## Exact producer groups

### Collection

Baseline collection output formerly inside `SemanticInfo`:

- `MethodSets`
- `MethodSymbol`
- enum variant declaration entries in `ResolvedSymbols`

Collection creates symbol shells. Binder and typechecker later complete or mutate
those symbol objects. Moving maps does not make pointed-to symbols immutable.

### Binding

Binder produces:

- bound types on collected symbols;
- sorted `OperationFunctions` catalog.

`OperationFunctions` is only used by LSP completion. Compiler semantic phases use
normal scopes and symbols instead.

### Resolution

Resolver produces:

- `BlockScopes`
- most `ResolvedSymbols` entries.

Typechecker later extends `ResolvedSymbols` for type-dependent selector resolution
and cloned default expressions. Therefore `ResolvedSymbols` cannot honestly move to
a resolver-only result unchanged.

### Constant evaluation

Constant evaluation owns `ConstValues`, but map mixes two lifetimes:

1. finalized module-scope constants after typechecking calls `FinalizeValues`;
2. lazy working-cache entries created by later constant queries during CFG and HIR.

`FinalizeValues` deletes and recomputes module constants but intentionally retains
local cache entries. `EvaluateExpr` can add entries after `Typechecked`, so map has no
global completion phase. Fingerprinting and MIR need authoritative module constants;
constant queries need mutable cache. Migration must distinguish these contracts even
if implementation retains one staged artifact.

### Typechecking

Clear typechecker-owned base types and lowering proofs:

- `ExprTypes`
- `CaseTests`
- `Matches`
- `InterfaceImplementations`
- `ImplicitConversions`
- `ImplicitCallArguments`
- `CompilerCalls`
- `StringConcatenations`
- `VariantConstructions`
- `ExpandedDefaultBindings`
- type-dependent and cloned-default entries added to `ResolvedSymbols`.

Flow-refined facts remain in separate `flowresult.Result`. Ownership cleanup remains
in separate `ownershipresult.Result`.

## Consumer boundaries

### Direct evidence consumption

Current downstream phases mostly consume recorded evidence correctly:

- HIR consumes conversions, compiler-call dispatch, interface slots, string
  concatenation, variant construction, expanded defaults, match evidence, and
  resolved symbols.
- ownership consumes resolved symbols, expanded-default provenance, operation
  classification, variant construction, effective types, and match evidence.
- CFG consumes resolved match-case indexes through `typecheckresult.Result.MatchCases`.
- MIR consumes finalized constant values and separate ownership cleanup plans.

Migration must preserve these direct evidence paths. It must not make downstream
phases resolve methods, conversions, variants, or call adaptation again.

### Existing fallbacks

Some consumers intentionally or defensively fall back when evidence is missing:

- HIR and ownership may perform lexical symbol lookup for identifiers.
- flow assignment lookup may fall back to scope lookup.
- LSP symbol/type queries reconstruct import, field, or lexical context when exact
  semantic evidence is unavailable.

Each fallback requires review during migration. Compiler fallbacks may hide broken
phase contracts; LSP fallbacks may be necessary for incomplete source. Do not remove
both categories mechanically.

### Partial results after diagnostics

Pipeline advances modules through CFG, flow, definite initialization, and ownership
before project error gate. HIR alone is suppressed when diagnostics already contain
errors. Therefore `Module.Phase == Typechecked` does not mean every typechecker proof
exists or whole result is valid.

Missing evidence may mean:

- construct does not require that evidence;
- invalid source prevented publication;
- producer skipped after an earlier diagnostic;
- compiler violated internal contract.

Extracted results need explicit per-entry presence and failure semantics. Validators
must not convert expected diagnostic-driven absence into internal panic, while valid
source missing mandatory proof must fail clearly.

### Shared mutable symbol and scope graph

Maps are not only ownership concern. Many results point to same `*symbols.Symbol`
and `*symbols.Scope` graph. State mutates across phases:

- binder sets symbol types;
- resolver sets `Initializing`, `Used`, and scope contents;
- typechecker may infer types and set `RequiresMutable`;
- usage and HIR consume later state.

LSP shallow reuse preserves these pointers. Splitting maps into result structs does
not make symbols immutable or reset-safe. Migration needs explicit stable identity,
mutation owner, and snapshot contract for shared symbol graph.

## Reset and incremental findings

### Finding 1: parsed reset can retain expanded AST without provenance

Severity: **high correctness risk**.

`typechecker.expandCallDefaults` mutates caller AST by appending cloned default
expressions to `CallExpr.Args`. It also records:

- cloned `ResolvedSymbols` entries;
- `ExpandedDefaultBindings` markers;
- copied expression types, conversions, and interface evidence.

Incremental invalidation can reset module to `Parsed`. That reset retains AST but
clears complete `SemanticInfo`. Re-typechecking sees argument count already expanded,
so expansion does not run again and deleted provenance is not reconstructed.

Potential impact: imported defaults referencing declaration-module bindings may be
resolved as caller syntax, remain unresolved, or lose lowering/ownership evidence.
Current tests cover initial imported-default compilation, not parsed reset and
recompile. This failure path is source-derived; no failing reset/recompile case was
executed during inventory.

Relevant code:

- `internal/semantics/typechecker/check_call.go`: `expandCallDefaults`,
  `copyExpressionEvidence`
- `internal/frontend/ast/clone.go`: `SubstituteExpr`
- `internal/project/modules.go`: `resetToPhase`
- `internal/lsp/state.go`: `seedReusableModules`
- `internal/pipeline/pipeline.go`: `invalidateSemanticDependents`

### Finding 2: intermediate semantic reset is non-idempotent

Severity: **high correctness and contract risk**.

Reset to any phase after `Parsed` retains complete `SemanticInfo`, scopes, and symbol
state. Re-entering later phases can corrupt results:

- reset to `Collected`, then binder appends duplicate `OperationFunctions`;
- reset to `Bound`, then resolver redeclares parameters in retained scopes;
- reset to `Resolved`, then const evaluation can return stale cached values;
- reset to `ConstEval`, then typechecker runs with retained proof maps and symbol
  mutations.

Current production downgrade paths appear limited to exact-phase reuse or `Parsed`,
so no production intermediate re-entry was established. `ResetModule` still claims
general retained-phase behavior. Existing tests cover `Parsed` and later retained
artifacts, but no reset/re-entry tests cover unsafe `Collected`, `Bound`, `Resolved`,
or `ConstEval` checkpoints. Those intermediate checkpoints are presently
non-reconstructible. Result separation must give each artifact exact reset gate or
reduce supported reset contract to safe checkpoints.

### Finding 3: LSP snapshots alias retained compiler state

Severity: **medium architecture risk**.

LSP reuse stores module pointers or shallow module copies. Retained maps and pointers
can still alias:

- AST
- imports
- module scope
- semantic maps
- typed AST index
- CFG/HIR/MIR artifacts

LSP compilation is mostly serialized, so no concrete race was reproduced. Still,
old context does not mean immutable snapshot: new compilation can mutate AST and
artifacts reachable from older context. Snapshot-generation correctness is unproven.
Result migration must state whether artifacts are immutable, uniquely owned, cloned,
or transferred.

### Finding 4: synthetic clone IDs are unique but schedule-dependent

Synthetic AST IDs use atomic allocation, preventing concurrent duplicate allocation
under practical node counts before counter wrap. Exact values depend on goroutine
scheduling; no wrap or parser-namespace bound is enforced. Current semantic
fingerprints do not include them. Future serialized artifacts, tests, or caches must
not assume stable or unconditionally collision-free synthetic IDs.

## Scheduler and concurrency contract

`advanceModulesThrough` advances ready modules concurrently, one phase per batch,
then waits before invalidation. Import prerequisites ensure imported module semantic
facts are complete before caller typechecking reads imported defaults.

Current safe assumptions:

- independent modules mutate separate semantic maps;
- imported semantic reads happen after required import phase;
- context module/type indexes use `CompilerContext.mu`;
- diagnostics and metrics are synchronized;
- semantic invalidation runs after worker join.

Every extracted result must preserve per-module ownership and import readiness. A
result must not introduce shared mutable maps across independently scheduled modules.
Focused race tests are required for each migration slice.

## Minimal proposed boundaries

These are design candidates, not approved types.

### Candidate A: declaration catalog

Cohesive current facts:

- method membership by receiver;
- declaration-to-method symbol identity;
- operation-function catalog after binding.

Open question: one staged callable catalog from `Collected` through `Bound`, or
separate collection and binding outputs? Source proves distinct production points;
review must weigh that boundary against added navigation/type cost and whether either
output has independent consumers, reset, or reuse.

### Candidate B: binding table

Cohesive current fact: AST identity to semantic symbol identity.

A strict resolver result is inaccurate because collector and typechecker also add
entries. Options:

1. one staged binding table with explicit writer phases;
2. separate declaration, resolution, and type-selection maps plus one canonical
   query operation;
3. change selector/default representation so final binding table has one producer.

Option 2 adds maps and query policy. It is acceptable only if it removes ambiguity
rather than spreading lookups. Option 3 may produce cleanest contract but has largest
behavioral scope.

### Candidate C: constant result

Distinguish published module constants from evaluator working cache. One staged
artifact remains possible, but API must expose which entries are authoritative and
which may appear after typecheck. Downstream fingerprint and MIR consumers need
finalized module values; CFG/HIR queries need mutable cache.

### Candidate D: typechecker result

Strong cohesive output:

- base expression types;
- conversion and call-adaptation proof;
- intrinsic/compiler-call dispatch;
- string operation classification;
- interface implementation slots;
- variant construction and match evidence;
- base case-test evidence;
- default-expansion provenance if AST expansion remains.

Possible extraction sizes range from one proof map to cohesive typechecker result.
Choice must compare migration blast radius, import-cycle constraints, partial-result
semantics, and actual reset lifetime. Exact package location must avoid import cycles
without creating wrapper accessors.

## Recommended migration order after review

1. **Fix or redesign default expansion lifetime first.** Do not move provenance maps
   while AST/reset contract is inconsistent.
2. **Define supported reset checkpoints.** Either implement field/result-specific
   reset or reject unsafe intermediate retention.
3. **Extract approved typechecker proof group at smallest safe migration size.** Move
   fields and all consumers together; delete old maps immediately.
4. **Make authoritative module constants distinct from post-typecheck query cache.**
5. **Resolve binding-table ownership.** Do not label current multi-writer map as
   resolver-only result.
6. **Move callable catalog only if resulting boundary reduces coupling and does not
   create decorative result types.**
7. **Add result validators and concurrency/determinism tests after each owner is
   explicit.**

## Required validation per migration slice

- focused producer and consumer package tests;
- diagnostic codes, text, spans, ordering, and deduplication preservation;
- `ResetModule` tests at every supported retained checkpoint;
- unchanged semantic fingerprint tests;
- imported default expansion followed by parsed reset and recompile;
- LSP unchanged-module reuse and semantic-dependent invalidation;
- repeated-output determinism where generated names or dumps are affected;
- `go test -race` for project, pipeline, LSP, and touched semantic packages;
- full `go test ./...` before review completion.

## Approved manual decisions

Maintainer approved these directions before product migration:

1. **Parsed versus transformed AST:** Must parsed AST remain immutable, with expanded
   defaults stored in a separate typed artifact, or may typechecker mutate it if reset
   restores pristine syntax and provenance?
2. **Reset contract:** Keep every intermediate semantic checkpoint, or restrict reuse
   to checkpoints proven reconstructible given current non-idempotent re-entry?
3. **Binding ownership:** Prefer one staged binding table or separate phase maps with
   canonical query policy?
4. **Migration granularity:** Move one proof group or one cohesive typechecker result?
   Choose smallest slice that preserves consumers without duplicate maps/wrappers.
5. **LSP snapshots:** Are retained artifacts immutable, uniquely owned by latest
   context, or copied on reuse? What generation may mutate shared AST/symbol graph?
6. **Partial results:** After semantic diagnostics, which evidence remains published
   and which downstream analyses may consume it?
7. **Missing evidence:** Which absence means prior user diagnostic, not-applicable,
   recoverable LSP state, or internal invariant violation?
8. **Symbol graph:** Which phase owns mutable symbol/scope state, and must stable
   pointer/`SymbolID` identity survive result boundaries and resets?
9. **Constants:** Separate authoritative module values from query cache physically, or
   retain one artifact with explicit entry-kind/finalization contract?
10. **Synthetic IDs:** Are practical atomic uniqueness and schedule dependence enough,
    or should namespace/wrap guarantees become enforced invariants?

Approved answers: parsed AST immutable; exact artifact reuse or reset to `Parsed`; one staged binding table; smallest coherent typechecker slices; old LSP generations immutable; partial semantic evidence may continue through semantic analyses but not HIR/backend; missing mandatory evidence on valid input is invariant failure; symbol identity is stable per generation and read-only after typecheck; finalized module constants differ from mutable query cache; synthetic IDs require namespace/wrap enforcement before persistence.
