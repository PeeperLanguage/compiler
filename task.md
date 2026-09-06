# Compiler Maintainability Migration Handoff

> Historical migration proposal, retained below without rewriting its original scope.
> Current implementation contract: [`docs/compiler-architecture.md`](docs/compiler-architecture.md)
> and [`COMPILER_FRAMEWORK_REPORT.md`](COMPILER_FRAMEWORK_REPORT.md). The old branch/workflow,
> effects-based usage target and blanket reflection ban below are not current delivery
> requirements: usage remains lexical; bounded typed-nil capability reflection is retained.
> New reference-bearing shapes still require provenance audits even when effects exist.
> Go baseline is 1.23.2. This history authorizes no branch changes, commits or shipping.

## Objective

Make Peeper compiler clean, readable, and difficult to extend incorrectly.

When contributor adds or changes syntax that uses a symbol, contributor should
declare semantic meaning once. Existing infrastructure should then handle ordinary
reads, writes, moves, copies, borrows, initialization, usage, and cleanup without
new feature-specific logic in every analysis.

Goal is not zero edits. Syntax, scope, type rules, genuinely new control flow, and
new runtime representation still need explicit owners. Goal is eliminating repeated
decisions and silent omissions.

No new Peeper language feature is part of this task. Do not add catch, error-union
syntax, or copy another language's syntax or semantics. Existing optional, enum,
loop, call, match, ownership, and default-argument behavior are validation cases,
not feature targets.

## Mandatory workflow

Follow `AGENTS.md`, `RULES.md`, and `go-style.md`. Repository rules override this
handoff.

1. Read this file and `compiler-maintainability.localplan.md` completely.
2. Work one numbered step only after explicit maintainer approval.
3. Before each patch, re-run full `AGENTS.md` pre-patch gate against live source and
   current diff.
4. After each patch, run post-patch gate and record explicit Rules check in local
   plan.
5. Keep `compiler-maintainability.localplan.md` complete. Never commit it.
6. Do not commit, push, create/update PR, merge, or change GitHub tracking without
   explicit maintainer authorization.
7. Preserve unrelated changes. Stop if unexpected work overlaps planned files.
8. Use `rg`/`rg --files` first. Use `apply_patch` for hand edits.
9. Verify behavior from current source and tests, not only docs or donor commits.
10. Do not proceed to next step when current step is merely compiling. Meet its
    validation and review stop condition.

## Branch policy

Plan was produced on `docs/compiler-framework`. That branch contains useful work
mixed with broad experimental migration. It is read-only donor, not implementation
trunk.

Before Step 1 implementation:

- run `git status --short --branch`;
- record current HEAD and `origin/main` SHA;
- create `feature/compiler-maintainability` from verified `origin/main`;
- preserve untracked `task.md` and ignored
  `compiler-maintainability.localplan.md`;
- inspect donor commits with `git show`;
- reproduce only approved behavior;
- do not merge or wholesale cherry-pick `docs/compiler-framework`;
- do not fetch, rebase, reset, stash, clean, delete, or rewrite history without
  permission.

## Architectural contract

### Each entity owns one job

| Entity | Owns | Must not own |
| --- | --- | --- |
| AST node | Parsed shape, location, canonical children | Type/ownership/backend policy |
| Module identity | Stable source/import identity | Phase-local semantic facts |
| NodeID | Syntax occurrence identity | Symbol/storage meaning |
| SymbolID | Declaration/binding identity | Flow state |
| Place/origin | Storage root and projections | Typechecking or cleanup policy |
| Type model | Source type structure and nominal identity | Per-use flow state |
| Type capability | Canonical derived copy/move/drop/etc. answer | Physical backend emission |
| Typechecker result | Resolved semantic decisions needing type knowledge | CFG topology |
| CFG | Blocks, sites, edges, reachability | Type or ownership re-derivation |
| Semantic operation | Ordered read/write/move/borrow/define/discard meaning | Analysis-specific state |
| Definite initialization | Initialized-state dataflow and diagnostics | Ownership/drop policy |
| Ownership | Move/loan/liveness state and cleanup decisions | Syntax parsing, physical layout |
| Usage | Used/unused accounting and warnings | Ownership state |
| Cleanup plan | Exact source-level destruction sites | Backend layout traversal |
| HIR/MIR | Lowering established evidence | Rediscovering source semantics |
| Backend layout | Physical representation | Source semantic acceptance |
| Artifact validator | Shape and cross-reference invariants | Duplicate analysis algorithm |

### Desired flow

```text
source
  -> AST
  -> binding/resolution artifacts
  -> typechecker artifact + published semantic meaning
  -> CFG artifact
  -> flow-refined artifact
  -> definite initialization / ownership / usage over CFG + semantic meaning
  -> cleanup plan
  -> HIR
  -> MIR
  -> backend
```

No lower phase reaches backward to recompute an upstream decision when explicit
evidence can cross boundary.

### Realistic feature-extension target

For future syntax node that evaluates value then stores it in symbol:

```text
required feature-specific work:
  parser shape
  resolver scope/binding
  typechecker rule and semantic publication
  CFG only if control flow differs
  HIR only if existing lowering shapes cannot represent it

automatic common work:
  symbol reads and writes
  definite initialization
  copy/move selection already published by typechecker
  borrow conflicts
  used/unused accounting
  cleanup planning
  MIR/backend when existing operations suffice
```

If future construct maps to existing semantic operations but still needs custom
ownership, definite-init, usage, or backend cases, migration is incomplete.

## Hard design constraints

- No pass-through wrappers or old names forwarding to new names.
- No ignored parameters retained to avoid caller updates.
- No duplicate maps during migration after one slice completes.
- No `map[type]any`, `Publish[T]`, service locator, global registry, reflection, or
  generic phase engine.
- No visitor method on every AST node for each compiler phase.
- No phase behavior stored on syntax nodes.
- No new helper/package/file unless it removes current duplication, protects
  invariant, or represents real phase/lifetime boundary.
- No one-field organizational struct.
- No backend source-policy decision.
- No validator that reimplements ownership or dataflow.
- No refactor mixed with newly discovered behavior change. Report bug and request
  separate approval.
- No Go 1.27 upgrade in this task. Existing Go generics may be used only when same
  implementation truly works across current types and improves clarity.
- No language syntax or semantics copied from external languages.

## Donor map

Inspect these commits only when their step is active:

| Commit | Useful evidence | Known caution |
| --- | --- | --- |
| `0d1f4ee` | explicit binding/typechecking result ownership | broad 48-file migration; split smaller |
| `03b4004` | canonical module identity and constant result | do not combine with Step 1 |
| `de8185a` | AST dispatch contract | test-tier, not compile-tier |
| `18b4800` | child traversal enforcement | structural check can be fooled; mutation-test it |
| `26600b0` | attribute/substructure traversal hardening | keep recovery semantics |
| `8ad69ae` | ownership capability vocabulary | final code still composes three walkers |
| `c8eca73` | published value-use kinds | incomplete coverage; validator only checks some uses |
| `c687b50` | cleanup-plan consolidation | verify every removed channel behavior |
| `52ff007` | ownership artifact validator | does not prove exactly-once drop paths |
| `7ec06e9` | removal of unused result channel | confirm no consumer before deletion |
| `7b11a33` | CFG topology validation | validator distinct from CFG analysis |
| `6fa713a` | closed iteration evidence | useful valid-state model; avoid ceremonial interfaces |
| `2a5fcf0` | sealed MIR node families | does not itself enforce backend dispatch completeness |

## Validation baseline

Use isolated cache because normal ccache/cache paths can be read-only:

```bash
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go vet ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go build ./...
git diff --check
```

When packaging or source behavior can be affected:

```bash
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./x_test
```

Run focused packages first, then full suite. Record command, exit code, and meaningful
output. Environment failures stay separate from source failures.

## Step 1 - Extract typechecker-owned result from mixed semantic storage

### Goal

Create one explicit typechecker generation artifact on fresh branch from
`origin/main`. This is first narrow slice of phase-result migration, not full donor
commit replay.

### Why first

Current `origin/main` `project.SemanticInfo` mixes binding, resolution,
typechecking, constant, method-index, and operation-catalog state. Common semantic
publication cannot be reliable until its owner and reset lifetime are clear.

### Pre-change inspection

Answer in local plan before editing:

1. Every `SemanticInfo` field and all writers/consumers.
2. Exact phase that first makes each field valid.
3. Whether field is base typechecking, flow-refined, binding/resolution,
   constant-evaluation, or shared catalog state.
4. Which fields donor `typecheckresult.Result` moved.
5. Which donor moves were later corrected; inspect subsequent commits touching
   `typecheckresult`, `bindingresult`, and `project.Module`.
6. Reset behavior in `Module.resetToPhase`, `ResetSemanticData`, and
   `CompilerContext.ResetModule`.
7. LSP/invalid-source paths that read partial typechecking evidence.
8. Eager const-evaluation paths that run before typechecking completes.

### Approved Step 1 implementation boundary

Move only facts produced as base typechecker decisions and consumed downstream.
Expected candidates, subject to live proof:

```text
ExprTypes
CaseTests
Matches
ImplicitConversions
ImplicitCallArguments
CompilerCalls
StringConcatenations
VariantConstructions
ForIterations
InterfaceImplementations
```

Do not move merely because donor moved it. Keep outside this slice unless live
writer/consumer/lifecycle evidence proves typechecker ownership:

```text
BlockScopes
ResolvedSymbols
ExpandedDefaultBindings
ConstValues
MethodSets
MethodSymbol
OperationFunctions
```

### Required implementation behavior

- Add dedicated typechecker result only if it owns several coherent facts.
- Constructor initializes all required maps and protects valid empty state.
- Typechecker owns creation/population.
- Consumers read new owner directly; no old-field fallback.
- Remove migrated fields from old mixed structure in same slice.
- Move associated data models with their owner; no type aliases.
- Keep base type evidence distinct from flow-refined evidence.
- Preserve nil/partial behavior before typecheck and during invalid source.
- Preserve default-argument expansion NodeID provenance.
- Preserve eager consteval behavior when typechecker result is absent/incomplete.
- Preserve semantic export fingerprint and LSP hover/completion behavior.
- Update reset lifecycle atomically: retain through its producing phase, clear on
  reset before that phase.
- Add lifecycle tests for nil, initialized-empty, populated, retained, and cleared
  states.
- Do not introduce generic result container.
- Do not move binding/resolution fields as incidental cleanup.

### Required validation

At minimum:

```bash
gofmt -w <touched-go-files>
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./internal/semantics/typechecker ./internal/semantics/consteval ./internal/project ./internal/pipeline ./internal/lsp
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -race -count=1 ./internal/project ./internal/pipeline ./internal/lsp
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./x_test
git diff --check
git status --short --branch
```

### Step 1 stop condition

Stop for Codex review. Report:

- branch and base SHA;
- field producer/consumer/lifecycle table;
- exact fields moved and explicitly deferred;
- all old reads/writes removed;
- reset and partial-source behavior;
- diff stat;
- exact validation results;
- mandatory Rules check.

Do not begin Step 2. Do not commit.

## Step 2 - Finish explicit phase artifacts and canonical identity

### Goal

Give remaining semantic facts one honest owner and stable lifetime, then replace
duplicated module identity with canonical identity.

### Work slices

This step is too broad for one patch. Claude must propose ordered sub-slices from
live inventory and wait for approval before each:

1. binding/collection/resolution generation artifact;
2. constant result versus query/cache state;
3. remaining shared catalogs whose multi-writer lifecycle is genuine;
4. canonical module identity and indexes;
5. reset/invalidation convergence.

### Requirements

- One fact stored once.
- Producer, consumers, valid-from phase, reset-before phase documented.
- Reuse `NodeID`, `SymbolID`, module identity, and existing binding indexes.
- Keep generated/default provenance paired with binding identity.
- No name/path/pointer reconstruction when stable ID exists.
- No compatibility field, shadow map, or stale alias.
- No false phase split: if collector/binder/resolver truly build one staged symbol
  graph with same lifetime, keep coherent result and document multi-writer contract.
- Directly update all consumers.
- Preserve diagnostics discard alongside artifact reset.
- Preserve incremental module reuse and dependency invalidation.

### Stop condition

After each approved sub-slice, show artifact ownership matrix and reset tests. Do not
continue automatically.

## Step 3 - Canonical traversal and exhaustive phase decisions

### Goal

Adding AST statement/expression/type kind or child must create immediate named
failures at every phase requiring a decision.

### Requirements

- Reuse existing `ast.Inspect`, `ir.InspectExpr`, `ir.InspectPlace`, and
  `hir.InspectStmt`; do not add parallel visitors.
- AST node owns canonical children only.
- Do not add resolver/typechecker/ownership methods to AST nodes.
- Add or transplant structural child-field tests only after understanding recovery
  nodes, substructures, attributes, and generated/default AST.
- Add phase-dispatch contracts for real switch sites.
- Missing node must be handled or explicitly classified:

```text
traverse
ignore
reject
contextual
```

- Every omission classification needs concrete reason.
- Test must fail when stale classification remains after real handler added.
- Mutation-prove each contract: remove one real case/child locally, capture expected
  failure, restore probe before stop.
- Do not claim compile-time exhaustiveness from source-inspection tests.
- Evaluate compile-tier sealed interfaces only if boilerplate and phase-state costs
  are lower than current checked-table approach. Do not implement visitor spike by
  default.

### Stop condition

Produce matrix of node families versus phase sites, with guard strength:
automatic, visible test, loud runtime validation, or manual gap.

## Step 4 - Canonical type capabilities

### Goal

Types receive one canonical derived answer for each shared semantic capability.
Consumers stop independently walking type structure.

### Inventory first

Audit current predicates and every caller, including:

```text
copy / explicit-copy / no-copy
needs-drop
sized / lowerable
equatable / orderable
contains-reference / contains-stored-reference
pointer/reference target
collection shape
target-width representability
backend structural drop/layout traversal
```

### Requirements

- Centralize only semantic questions shared by multiple callers.
- One recursive walker may return several tightly coupled ownership properties when
  they share traversal and invariants.
- Do not create giant capability object for unrelated properties.
- Preserve aliases, nominal types, generic instantiation, recursive types, cycle
  guards, aggregates, enums/optionals, interfaces, functions, arrays/slices,
  ownership carriers, and invalid types.
- Update consumers directly and delete replaced walkers.
- Do not leave `IsX` wrappers around canonical function solely for old callers.
- Backend layout traversal may remain when it answers physical emission rather than
  source policy. Rename/comment/test boundary if unclear.
- Type capability does not own per-expression use decision.
- Add exhaustive current-type table tests and recursion tests.
- If consolidation reveals inconsistent existing behavior, report it. Do not choose
  new language semantics inside refactor.

### Stop condition

Show old/new behavior table, removed duplicate walkers, consumers, and remaining
backend-only traversal with justification.

## Step 5 - Publish semantic operations once

### Goal

Represent common effect of existing syntax using small stable vocabulary so later
analyses do not each inspect AST shape.

### Inventory

Trace existing behavior for:

- declarations with and without initializers;
- assignment and replacement;
- return values;
- expression statements/discarded values;
- conditions;
- calls, implicit receivers, piped calls, default arguments, intrinsics;
- address/shared/mutable borrow;
- selector/index/deref places;
- variant construction, case tests, match payload bindings;
- loops, break, continue, scope exits;
- explicit free/drop operations;
- string/array/collection operations.

Record exact evaluation order and which phase currently decides read/copy/move.

### Vocabulary constraints

Candidate concepts—not mandated names:

```text
UseValue(Read | Copy | Move)
DefinePlace
ReadPlace
WritePlace
BorrowPlace(shared | mutable)
UnpackPayload
DiscardValue
```

- Add operation only when current behavior needs it.
- Operation stores stable `NodeID`/`SymbolID`/place identity and diagnostic location,
  not raw syntax pointer when avoidable.
- Typechecker publishes type-dependent decisions.
- Purely structural decisions may be normalized after CFG if that avoids redundant
  evidence without losing completeness.
- Preserve ordered evaluation.
- Result owner and reset phase explicit.
- Missing required operation for valid accepted source fails validator.
- Invalid source uses deliberate recovery, not false compiler bug.
- Avoid one struct with unrelated optional fields; use valid closed shapes where
  they clarify real alternatives.
- Do not create a framework package until boundary has multiple real producers or
  consumers.

### Pilot requirement

Choose one existing construct whose symbol effects are currently repeated across at
least two analyses. Migrate producer plus one consumer only. Prove parity, stop, and
request review before expanding.

### Stop condition

Show old repeated decisions, new canonical publication, operation ordering tests,
validator behavior, and exact remaining consumers.

## Step 6 - Convert dataflow consumers

### Goal

Definite initialization, ownership, and usage become separate state machines over
shared CFG sites and semantic operations.

### Requirements

- Migrate one construct family and one consumer per approved slice.
- Delete old AST switch/re-derivation only after parity tests pass.
- Definite initialization owns initialized-state lattice and diagnostics.
- Ownership owns liveness, moves, loans, conflicts, and cleanup decisions.
- Usage owns used/unused accounting and warnings.
- Share operation stream, not analysis state or diagnostics.
- Preserve CFG reachability, joins, loops/fixed points, and branch-local state.
- Preserve stable-place overlap and alias invalidation.
- Preserve unreachable-source semantics when a warning is syntax/typecheck-owned.
- Preserve exact source locations and diagnostic codes/messages unless separately
  approved.
- New existing operation kind must force every consumer to handle, reject, or
  explicitly ignore it.
- No default switch branch that silently does nothing.

### Completion test

Use an existing symbol-bearing construct as extension simulation. Modify only its
upstream normalization to emit already-known operations; confirm migrated consumers
need no construct-specific code.

Do not add source syntax.

## Step 7 - Cleanup and lowering convergence

### Goal

One cleanup plan owns source destruction decisions. HIR, MIR, and backend consume
published facts without rebuilding source semantics.

### Requirements

- Inventory every drop channel, flag, temporary cleanup path, and explicit free.
- Distinguish programmer-requested free, source cleanup plan, and MIR temporary
  destruction.
- Remove write-only/dead/duplicate cleanup channels after consumer audit.
- Preserve evaluation-before-unwind and reverse lexical cleanup order.
- Preserve return, assignment replacement, discarded value, projection base,
  match/payload, loop exit, and scope exit behavior.
- Ownership publishes decisions at stable NodeID/SiteID.
- HIR/MIR lower exact evidence.
- Backend only emits physical destruction for supplied MIR/layout.
- Backend may recursively traverse layout to emit nested destruction; it cannot
  decide source liveness or invent missing cleanup.
- Avoid moving HIR earlier in pipeline if ownership still needs typed AST/CFG and
  established phase order.
- Add exactly-once runtime regressions for owned nested values and every changed
  cleanup path.

### Stop condition

Provide decision/emission ownership map and prove no duplicate policy remains in
touched paths.

## Step 8 - Artifact validators and omission proof

### Goal

Normal `go test ./...` and pipeline execution catch missing contributor work.

### Requirements

- Each phase artifact documents producer, consumers, valid-from phase, reset rule,
  and validator.
- Validate keys reference current AST nodes, symbols, CFG sites, or types.
- Validate legal operation/use/capability combinations.
- Validate CFG topology separately from user-facing CFG analysis.
- Validate cleanup keys and result generation; do not duplicate ownership dataflow
  to “prove” exactly-once behavior.
- Seal IR/MIR node families where useful.
- Add dispatch contracts where Go cannot provide exhaustiveness.
- Add result reset/invalidation tests.
- Mutation-prove guards with temporary probes, then remove probes.
- Create no test-only production API.
- Simulate addition of a node, type, and semantic operation in tests/build probes.
  Record which failure guides contributor at each missing step.
- Explicitly list any remaining unguarded work.

### Required output

```text
change kind | required decision | owner | guard | failure message | manual risk
```

## Step 9 - Final maintainability convergence

### Goal

Prove architecture is easier to understand and requires fewer repeated edits.

### Audit

- Every touched function still matches name, parameters, return, and behavior.
- No pass-through wrappers, stale aliases, ignored params, duplicate maps, shadow
  registries, or temporary bridges.
- No files/modules split only for appearance.
- No unrelated refactor.
- No source behavior change hidden inside architecture work.
- AST traversal is canonical.
- Semantic facts published once.
- Identity stable across phases.
- Analyses consume operations, not source syntax, when semantics are common.
- Cleanup has one source decision owner.
- HIR/MIR/backend do not re-derive upstream facts.
- Invalid source/LSP recovery remains safe.
- Docs match live source and name honest enforcement gaps.

### Comparison report

Choose at least three existing change shapes:

1. symbol-bearing statement/expression;
2. composite/owned type;
3. control-flow construct.

For each, show before/after required files and decisions. Do not claim fewer touches
when same work merely moved behind wrappers or generated tables.

### Full validation

```bash
gofmt -w <all-touched-go-files>
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -race -count=1 ./internal/project ./internal/pipeline ./internal/lsp ./internal/semantics/...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go vet ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go build ./...
GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1 go test -count=1 ./x_test
git diff --check
git status --short --branch
```

### Final stop condition

Return phase-owner map, omission matrix, before/after change paths, validation
evidence, Rules check, and honest remaining risks. Wait for explicit commit and
publication approval.

## Codex review contract

Codex will review Claude output after every approved step against:

- live base SHA and exact diff;
- approved step boundary;
- producer/consumer/reset ownership;
- all touched functions, fields, aliases, parameters, and behavior;
- wrapper/duplication/helper rules;
- diagnostics and invalid-source/LSP recovery;
- stable identity and absence of re-derivation;
- evaluation order, CFG topology, flow joins, moves, loans, and cleanup;
- HIR/MIR/backend phase discipline;
- focused/full/race/vet/build/bundle/source-fixture results;
- unrelated work and Git publication state.

## Initial authorization

Only Step 1 is authorized. Complete Step 1, update
`compiler-maintainability.localplan.md`, and stop for Codex review. Do not start
Step 2. Do not commit.
