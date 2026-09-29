# Artifact-Oriented Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace phase-shaped semantic `Result` artifacts with the durable path AST → THIR → CFG → Analysis → MIR, keeping operation-local evidence private and reducing the number of module fields, phase states, packages, and cross-package contracts a solo maintainer must understand.

**Architecture:** Base typechecking becomes one operation that owns constant evaluation, private typing evidence, and THIR publication. Finalized constants move into generation-owned `symbols.Index`; CFG semantic analysis becomes one `analysis.Module` artifact, with flow facts and cleanup decisions retained while effects, definite-init state, and ownership solver state remain transient. The pipeline then exposes one `Analyzed` lifecycle state instead of FlowTyped/Effects/DefiniteInit/Ownership.

**Tech Stack:** Go 1.23+, Peeper AST/THIR/CFG/MIR, existing diagnostics/visitor infrastructure, `go test`, race detector, bundled `x_test` fixtures.

**Spec:** `docs/superpowers/specs/2026-09-28-artifact-oriented-semantics-design.md`

## Global Constraints

- Persistent module fields represent durable compiler concepts, not every operation.
- Do not add a generic semantic database, pass framework, compatibility facade, pass-through wrapper, or one-field result wrapper.
- Keep side tables private to the operation/artifact that owns them; consumers use semantic queries rather than backing maps.
- Preserve existing closed-family visitor/double-dispatch guarantees.
- Preserve source diagnostics, error recovery, THIR/CFG/analysis/MIR validation, deterministic identities, LSP fallback behavior, and incremental invalidation correctness.
- Keep `ModuleScope` and `SymbolIndex` separate in this migration.
- Do not implement function-level cache hits or persistent caches.
- Existing source semantics must remain unchanged. No new `x_test` fixture is required unless implementation reveals an intentional language behavior change; the existing `x_test` suite must still pass.
- Every task follows RED→GREEN where production behavior/API changes. Run focused tests first and full repository validation before the migration is declared complete.
- `artifact-oriented-semantics.localplan.md` remains uncommitted and is updated after every task with Rules check, validation, and resume state.
- Commits require explicit user approval under repository `RULES.md`; task commit commands below are the intended boundaries, not authorization to commit automatically.

## Review Focus

The task tests below explicitly cover these high-risk cases:

1. **Generated defaults and checked iterator expansions** — removing `typecheckresult` must not lose effective call arguments, generated binding identity, addressability, or iteration lowering. Covered in Task 1 typechecker/LSP regressions.
2. **Cross-module constants and export fingerprints** — moving constants to `SymbolIndex` must preserve authoritative owner-module reads while evaluator query caches remain generation-local and non-exported. Covered in Task 2 project/typechecker/MIR tests.
3. **Flow-refined variant/optional facts reaching lowering** — replacing `flowresult` must preserve payload tests, origins, aggregate slots, and effective type fallback used by LSP/MIR. Covered in Task 3 analysis/exprlower/LSP tests.
4. **Effect ordering, definite initialization, and ownership cleanup** — making effects transient must preserve read-before-write ordering, branch/loop convergence, scope/return/overwrite/discard/match cleanup, and validator failures. Covered in Task 4 analysis/MIR tests.
5. **Incremental/LSP retention after phase collapse** — replacing four lifecycle phases with `Analyzed` must preserve reset boundaries, copied diagnostics, semantic export invalidation, and tooling fallback on incomplete/erroring source. Covered in Task 4 pipeline/project/LSP tests.

---

### Task 1: Make base typechecking publish THIR directly

**Purpose:** Remove `typecheckresult.Result` as architecture rather than replacing it with another exported evidence container. Constant evaluation is internalized with typechecking because it is the only production consumer that currently needs pre-THIR typechecker evidence.

**Files:**
- Create: `internal/semantics/typechecker/evidence.go`
- Create: `internal/semantics/typechecker/thir_build.go`
- Create: `internal/semantics/typechecker/constant_eval.go`
- Create or move focused tests into: `internal/semantics/typechecker/constant_eval_test.go`
- Modify: `internal/semantics/typechecker/typechecker.go`
- Modify: `internal/semantics/typechecker/assignability.go`
- Modify: `internal/semantics/typechecker/check_call.go`
- Modify: `internal/semantics/typechecker/check_expr.go`
- Modify: `internal/semantics/typechecker/check_stmt.go`
- Modify: `internal/semantics/typechecker/typechecker_test.go`
- Modify: `internal/semantics/typechecker/for_in_test.go`
- Modify: `internal/semantics/typechecker/flow_test.go`
- Modify: `internal/ir/thir/model.go` only if a durable THIR query is missing; do not add typing scratch state to THIR.
- Modify: `internal/ir/thir/build_test.go` or migrate its assertions into typechecker tests.
- Modify: `internal/ir/exprlower/lower_test.go`
- Modify: `internal/semantics/ownershipresult/validate.go`
- Modify: `internal/semantics/ownershipresult/validate_test.go`
- Modify: `internal/module/module.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/project/modules_test.go`
- Modify: `internal/lsp/completion.go`
- Modify: `internal/lsp/hover.go`
- Modify: `internal/lsp/workspace_test.go`
- Delete: `internal/ir/thir/build.go`
- Delete: `internal/semantics/typecheckresult/result.go`
- Delete: `internal/semantics/typecheckresult/result_test.go`
- Delete: `internal/semantics/consteval/consteval.go`
- Delete: `internal/semantics/consteval/consteval_test.go`

**Interfaces:**
- Consumes: existing parsed/resolved `module.Module`, `symbols.Index`, AST, diagnostics, and current `constantresult.Result` storage (storage moves in Task 2).
- Produces:
  - `func Check(ctx *project.CompilerContext, module *module.Module) *thir.Module`
  - private `type evidence struct { ... }` inside package `typechecker`, containing the maps/types currently required while checking and THIR-building.
  - private constant evaluator owned by one `Check` operation; typechecker call sites use it directly instead of importing a separate `consteval` package.
  - `Module.BaseExprType` reads THIR only; `Module.EffectiveExprType` falls back to that THIR base type.
- `ownershipresult.Result.Validate` receives canonical THIR instead of `*typecheckresult.Result` for typed-node/use validation.

- [ ] **Step 1: Add failing publication tests**

Add `TestCheckPublishesTHIRWithoutPersistentTypingResult` in `internal/semantics/typechecker/typechecker_test.go` that calls:

```go
source := Check(ctx, module)
```

and asserts:
- returned THIR is non-nil for valid source;
- `source.Validate()` succeeds;
- a representative expression's `ExprType()` matches its expected base type;
- a representative call carries its effective/default arguments in THIR;
- a generated default binding is marked on the corresponding `thir.Ident`;
- a checked/custom iteration is represented by THIR `For.Checked` or `For.Iteration` rather than requiring an external result query.

Update one ownership-result validator test to express its typed/use invariant through a minimal THIR expression rather than `typecheckresult.New()`.

- [ ] **Step 2: Run RED tests**

Run:

```bash
go test ./internal/semantics/typechecker ./internal/semantics/ownershipresult -run 'TestCheckPublishesTHIRWithoutPersistentTypingResult|Test.*Validate' -count=1
```

Expected: FAIL to compile because `Check` does not return THIR and ownership validation still requires `typecheckresult.Result`.

- [ ] **Step 3: Move typechecker evidence behind the checker**

Move the semantic evidence structs/maps from `typecheckresult` into `internal/semantics/typechecker/evidence.go` and make storage package-private. Add an `evidence *evidence` field to `checker`; update all `c.module.Typechecking.*` reads/writes to `c.evidence.*`.

Replace `Module.ExpandedDefaultBinding` usage inside the checker with a checker-owned callback/method backed by private evidence. Do not add an exported evidence interface or a forwarding facade.

- [ ] **Step 4: Internalize constant evaluation with typechecking**

Move the evaluator implementation from `internal/semantics/consteval` to `internal/semantics/typechecker/constant_eval.go` so it can read private typing evidence directly for variant construction/case tests. Keep the current `constantresult.Result` storage in this task; Task 2 changes that lifetime.

Typechecker expression/statement checks call the private evaluator directly. Constant finalization and THIR constant-condition evaluation happen inside `Check` after base checking, preserving existing diagnostic anchoring and cycle behavior.

- [ ] **Step 5: Move AST→THIR construction under typechecker ownership**

Move `internal/ir/thir/build.go` logic to `internal/semantics/typechecker/thir_build.go`. The builder constructs exported THIR model nodes but retains no scratch state after `Check` returns.

Implement:

```go
func Check(ctx *project.CompilerContext, module *module.Module) *thir.Module
```

with this explicit order:
1. initialize checker + private evidence/evaluator;
2. run base checks;
3. finalize authoritative module constants using the current constant storage;
4. materialize THIR from AST + symbol state + private evidence;
5. return THIR.

Do not validate THIR inside `Check`; pipeline remains the lifecycle owner and validates the returned artifact immediately after publication.

- [ ] **Step 6: Migrate consumers away from `Module.Typechecking`**

Update pipeline to:

```go
module.THIR = typechecker.Check(phaseCtx, module)
```

and remove its direct `consteval.FinalizeValues`/`thir.Build` orchestration.

Update:
- LSP readiness guards to use `module.THIR != nil` instead of `module.Typechecking != nil`;
- workspace tests to inspect `thir.Call.Args` and `thir.Ident.IsExpandedDefaultBinding`;
- `Module.BaseExprType` to query only THIR;
- ownership validation to query THIR `ExprType`/`UseKind` facts;
- tests that inspect base semantic decisions to inspect THIR or package-private typechecker evidence only when the decision is truly scratch-state-specific.

Remove `Module.Typechecking`, its reset branches, and all imports/usages of `typecheckresult`.

- [ ] **Step 7: Verify Task 1 GREEN**

Run:

```bash
gofmt -w internal/semantics/typechecker internal/ir/thir internal/semantics/ownershipresult internal/module internal/pipeline internal/project internal/lsp internal/ir/exprlower
go test ./internal/semantics/typechecker ./internal/ir/thir ./internal/semantics/ownershipresult ./internal/ir/exprlower ./internal/project ./internal/pipeline ./internal/lsp -count=1
go test -race ./internal/semantics/typechecker ./internal/semantics/ownershipresult -count=1
go vet ./...
git diff --check
! rg -n 'typecheckresult|\.Typechecking\b' internal --glob '*.go'
```

Expected: all tests/checks pass; final `rg` produces no matches.

- [ ] **Step 8: Rules check and review stop**

Update `artifact-oriented-semantics.localplan.md` with:
- no exported typing-evidence facade/wrapper added;
- no ignored parameters or stale aliases retained;
- constant evaluator moved because pre-THIR evidence is a typechecking concern, not duplicated;
- THIR validation/diagnostics preserved;
- exact validation results.

Stop for user review/commit approval before Task 2.

**Intended commit subject after approval:** `Publish THIR from base typechecking`

---

### Task 2: Move published constants into generation-owned symbol state

**Purpose:** Eliminate `constantresult.Result` by separating authoritative generation state from evaluator scratch cache.

**Files:**
- Modify: `internal/semantics/symbols/index.go`
- Modify: `internal/semantics/symbols/index_test.go` (create if no focused index test file exists)
- Modify: `internal/semantics/typechecker/constant_eval.go`
- Modify: `internal/semantics/typechecker/constant_eval_test.go`
- Modify: `internal/project/modules.go`
- Modify: `internal/project/modules_test.go`
- Modify: `internal/project/export_fingerprint_test.go`
- Modify: `internal/ir/mir/module_lower.go`
- Modify: `internal/ir/mir/module_lower_test.go`
- Modify: `internal/module/module.go`
- Modify: `internal/pipeline/pipeline.go` only where constant storage is still wired.
- Delete: `internal/semantics/constantresult/result.go`
- Delete: `internal/semantics/constantresult/result_test.go`

**Interfaces:**
- Consumes: Task 1 private constant evaluator and generation-owned `symbols.Index`.
- Produces these `symbols.Index` operations:

```go
func (r *Index) PublishConstant(id SymbolID, value constvalue.Value)
func (r *Index) ConstantValue(id SymbolID) constvalue.Value
func (r *Index) ClearConstants()
```

- Private evaluator owns `cache map[symbols.SymbolID]constvalue.Value` and `inProgress map[symbols.SymbolID]struct{}` for one base-typecheck operation.
- `project.CompilerContext.PublishedConstant(module, sym)` resolves through the defining module's `SymbolIndex.ConstantValue`.
- `mir.LoweringInput` no longer contains a separate Constants field; local module static constant emission reads `input.SymbolIndex.ConstantValue(symbol.ID)`.

- [ ] **Step 1: Write failing symbol-ownership and cache-lifetime tests**

Add tests that assert:
- `symbols.Index.PublishConstant`/`ConstantValue` round-trip a value and `ClearConstants` removes it;
- `project.PublishedConstant` reads an imported constant from the owner module's `SymbolIndex`;
- export fingerprints change when an authoritative published constant changes;
- a typechecker constant-query cache entry is not visible through `SymbolIndex.ConstantValue` until finalization publishes it;
- MIR static data reads finalized values from `SymbolIndex` without a separate constants input.

- [ ] **Step 2: Run RED tests**

Run:

```bash
go test ./internal/semantics/symbols ./internal/semantics/typechecker ./internal/project ./internal/ir/mir -run 'Test.*Constant|Test.*Fingerprint|Test.*Static' -count=1
```

Expected: FAIL to compile because the `symbols.Index` constant API does not exist and MIR still expects `constantresult.Result`.

- [ ] **Step 3: Add generation-owned constant storage to `symbols.Index`**

Add a private `constants map[SymbolID]constvalue.Value`, initialize it in `NewIndex`, and implement only the three narrow operations above. Do not expose the map or place constant values on shared `*Symbol` objects.

- [ ] **Step 4: Make evaluator cache operation-local**

Remove `constantresult.Result` usage from `constant_eval.go`. Keep lazy cache/in-progress maps on the private evaluator; finalization writes authoritative module constants to `module.SymbolIndex.PublishConstant` after final symbol types are known.

When finalizing, call `module.SymbolIndex.ClearConstants()` before recomputing top-level constants so stale generation values cannot survive.

- [ ] **Step 5: Migrate project and MIR constant reads**

Update `CompilerContext.PublishedConstant` and semantic export fingerprint tests to use symbol-owned state.

Remove `Constants` from `mir.LoweringInput`; static data emission reads `input.SymbolIndex.ConstantValue(symbol.ID)`.

Remove `Module.Constants` and all reset/lifecycle code for it. Delete `constantresult`.

- [ ] **Step 6: Verify Task 2 GREEN**

Run:

```bash
gofmt -w internal/semantics/symbols internal/semantics/typechecker internal/project internal/ir/mir internal/module internal/pipeline
go test ./internal/semantics/symbols ./internal/semantics/typechecker ./internal/project ./internal/ir/mir ./internal/pipeline -count=1
go test -race ./internal/semantics/typechecker ./internal/project -count=1
go vet ./...
git diff --check
! rg -n 'constantresult|\.Constants\b' internal --glob '*.go'
```

Expected: all tests/checks pass; no `constantresult` or `Module.Constants` reference remains.

- [ ] **Step 7: Rules check and review stop**

Update local plan with constant ownership/cache invariants and exact validation. Stop for user review/commit approval.

**Intended commit subject after approval:** `Move constants into symbol state`

---

### Task 3: Replace `flowresult.Result` with durable `analysis.Module`

**Purpose:** Establish the one post-CFG durable semantic artifact before moving the remaining algorithms into it. This task changes the durable flow-fact owner but does not yet collapse effect/ownership lifecycle phases.

**Files:**
- Create: `internal/semantics/analysis/module.go`
- Create: `internal/semantics/analysis/flow.go`
- Create: `internal/semantics/analysis/flow_expr.go` as needed when moving existing flow implementation.
- Create: `internal/semantics/analysis/flow_test.go`
- Modify: `internal/module/module.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/project/modules_test.go`
- Modify: `internal/semantics/ownership/ownership.go`
- Modify: `internal/semantics/ownership/expr.go`
- Modify: `internal/semantics/ownership/reference.go`
- Modify: `internal/semantics/ownership/ownership_test.go`
- Modify: `internal/semantics/ownership/variant_rebind_test.go`
- Modify: `internal/ir/exprlower/lower.go`
- Modify: `internal/ir/exprlower/lower_test.go`
- Modify: `internal/ir/mir/module_lower.go`
- Modify: `internal/ir/mir/module_lower_test.go`
- Modify: `internal/lsp/server_test.go`
- Delete or move: `internal/semantics/typechecker/flow.go`
- Delete or move: `internal/semantics/typechecker/flow_thir.go`
- Delete or move: `internal/semantics/typechecker/flow_test.go`
- Delete: `internal/semantics/flowresult/result.go`
- Delete: `internal/semantics/flowresult/result_test.go`

**Interfaces:**
- Consumes: THIR, CFG, module scope, symbol state.
- Produces final durable flow-query API on `analysis.Module`:

```go
func (m *Module) ExprType(id source.NodeID) typeinfo.Type
func (m *Module) Payload(id source.NodeID) (PayloadAccess, bool)
func (m *Module) CaseTest(id source.NodeID) (CaseTest, bool)
func (m *Module) VariantField(id source.NodeID) (VariantFieldAccess, bool)
func (m *Module) Origins(id source.NodeID) (OriginResolution, bool)
func (m *Module) StorageOrigins(id source.NodeID) []place.Origin
func (m *Module) ValueOrigins(id source.NodeID) []place.Origin
func (m *Module) AggregateSlots(id source.NodeID) ([]AggregateSlot, bool)
```

- Introduce the final orchestration entry point now so later tasks extend it without another public API rename:

```go
type Input struct {
    Source      *thir.Module
    CFG         *cfg.Module
    Scope       *symbols.Scope
    SymbolIndex *symbols.Index
}

func Run(diag *diagnostics.DiagnosticBag, input Input) *Module
```

During this task `Run` populates flow facts only; effects/definite-init/ownership remain external operations until Task 4. This is a migration state, not a compatibility wrapper: the API and artifact remain final, and Task 4 extends its body/owned state.

- `Module.Flow` becomes `Module.Analysis *analysis.Module`.
- `Module.EffectiveExprType` reads `Analysis.ExprType(id)` then THIR base type.
- ownership Input and MIR/exprlower consume `*analysis.Module` instead of `*flowresult.Result`.

- [ ] **Step 1: Write failing analysis artifact tests**

Move representative flow tests to `internal/semantics/analysis/flow_test.go` and add `TestRunPublishesFlowFacts` covering:
- optional/variant refinement changes `ExprType` at a use site;
- payload/case test facts survive;
- origins and aggregate slots are returned as detached snapshots;
- no refinement falls back through `Module.EffectiveExprType` to THIR base type.

Update one exprlower and one LSP test to construct/read `Module.Analysis` rather than `Flow`.

- [ ] **Step 2: Run RED tests**

Run:

```bash
go test ./internal/semantics/analysis ./internal/ir/exprlower ./internal/lsp -run 'TestRunPublishesFlowFacts|Test.*FlowRefined|Test.*Payload' -count=1
```

Expected: FAIL because package `analysis` / `Module.Analysis` do not exist.

- [ ] **Step 3: Move flow representation and solver into `analysis`**

Move flow evidence types/maps from `flowresult` to private storage in `analysis.Module`; preserve defensive copying of origins/aggregate slices.

Move the current flow solver from typechecker into `analysis` and have `Run` populate one `Module`. Keep control-flow/dataflow algorithm behavior unchanged.

- [ ] **Step 4: Migrate consumers to `analysis.Module`**

Replace flow-result dependencies in:
- ownership input/queries;
- `exprlower.Context`;
- `mir.LoweringInput`;
- `Module.EffectiveExprType`;
- pipeline publication and reset tests.

Delete `flowresult` and old typechecker flow files. Keep the old lifecycle phase names for this task; only durable field ownership changes.

- [ ] **Step 5: Verify Task 3 GREEN**

Run:

```bash
gofmt -w internal/semantics/analysis internal/semantics/ownership internal/ir/exprlower internal/ir/mir internal/module internal/pipeline internal/project internal/lsp
go test ./internal/semantics/analysis ./internal/semantics/ownership ./internal/ir/exprlower ./internal/ir/mir ./internal/project ./internal/pipeline ./internal/lsp -count=1
go test -race ./internal/semantics/analysis ./internal/semantics/ownership -count=1
go vet ./...
git diff --check
! rg -n 'flowresult|\.Flow\b' internal --glob '*.go'
```

Expected: all tests/checks pass; `analysis.Module` is the only durable owner of flow facts.

- [ ] **Step 6: Rules check and review stop**

Record that `analysis.Module` is a durable semantic artifact, not a wrapper around `flowresult`; backing maps are private; no duplicate flow solver remains. Stop for user review/commit approval.

**Intended commit subject after approval:** `Publish flow facts through analysis`

---

### Task 4: Internalize effects and ownership, then collapse lifecycle to `Analyzed`

**Purpose:** Complete the semantic-analysis boundary. Effects, definite-init state, and ownership solver state become package-private execution details; only flow refinements and cleanup decisions survive in `analysis.Module`.

**Files:**
- Add/move into `internal/semantics/analysis/`:
  - `effects.go`
  - `effect_ops.go`
  - `effect_visitor.go`
  - `definite_init.go`
  - `ownership.go`
  - `ownership_expr.go`
  - `ownership_reference.go`
  - `cleanup.go`
  - `validate.go`
  - corresponding focused tests (`effects_test.go`, `definite_init_test.go`, `ownership_test.go`, `validate_test.go`)
- Modify: `internal/semantics/analysis/module.go`
- Modify: `internal/semantics/analysis/flow.go`
- Modify: `internal/ir/mir/module_lower.go`
- Modify: `internal/ir/mir/module_lower_test.go`
- Modify: `internal/ir/exprlower/lower.go` only where cleanup/analysis query ownership requires it.
- Modify: `internal/module/module.go`
- Modify: `internal/phase/phase.go`
- Modify: `internal/phase/phase_test.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`
- Modify: `internal/project/modules_test.go`
- Modify: `internal/lsp/state.go`
- Modify: `internal/lsp/server_test.go`
- Modify: `internal/diagnostics/bag_test.go`
- Delete directories after code/tests migrate:
  - `internal/semantics/effect/`
  - `internal/semantics/definiteinit/`
  - `internal/semantics/ownership/`
  - `internal/semantics/ownershipresult/`

**Interfaces:**
- `analysis.Input` remains the Task 3 final shape.
- `analysis.Run` becomes the complete final operation, in explicit order:
  1. flow analysis into durable `analysis.Module` facts;
  2. effect extraction into a local function/module effect stream;
  3. effect validation when source diagnostics permit invariant checking;
  4. definite-initialization diagnostics using the local effect stream;
  5. ownership analysis using flow facts + local effects, publishing only cleanup decisions into `analysis.Module`;
  6. analysis artifact validation before publication when diagnostics permit.
- Cleanup backing maps remain private. MIR uses these final queries:

```go
func (m *Module) DropsAfterSite(fn moduleid.FunctionID, site cfg.SiteID) []symbols.SymbolID
func (m *Module) DropsBeforeReturn(fn moduleid.FunctionID, id source.NodeID) []symbols.SymbolID
func (m *Module) DropsBeforeAssign(fn moduleid.FunctionID, id source.NodeID) bool
func (m *Module) DiscardedValue(fn moduleid.FunctionID, id source.NodeID) bool
func (m *Module) ProjectionBase(fn moduleid.FunctionID, id source.NodeID) bool
func (m *Module) MatchFieldDrops(fn moduleid.FunctionID, id source.NodeID) []int
func (m *Module) MatchWholePayloadDrop(fn moduleid.FunctionID, id source.NodeID) bool
```

Return slices are detached snapshots; callers never receive cleanup maps/plans.

- `mir.LoweringInput` contains `Analysis *analysis.Module` and no ownership result.
- `module.Module` contains `Analysis` but no `Effects` or `Ownership`.
- Phase enum becomes:

```text
None, Setup, Load, Parsed, Collected, Bound, Resolved,
Typechecked, CFG, Analyzed, Usage, MIR, Backend, Finalize
```

- [ ] **Step 1: Write failing end-boundary tests**

Before moving production code, add/convert tests that require the final API:

1. `analysis.TestRunChecksDefiniteInitializationAndPublishesCleanup` — one source exercises an uninitialized read diagnostic and a separate valid function publishes scope/return cleanup through `analysis.Module` queries.
2. `analysis.TestRunPreservesEffectOrdering` — assignment/initializer effect order remains read-before-write/define as required by existing tests.
3. `analysis.TestCleanupQueriesAreFunctionScoped` — same source Node/Site shape in different functions cannot leak cleanup.
4. `mir.TestGenerateMIRConsumesAnalysisCleanup` — MIR emits planned drops using `Analysis` without `ownershipresult.Result`.
5. `pipeline.TestAdvancePublishesAnalyzedArtifact` — advancing from CFG produces `module.Analysis`, sets `phase.Analyzed`, and no intermediate lifecycle phase is observable.
6. `lsp.TestRetainedAnalysisDiagnosticsRefreshAfterEdit` — retained module reuse copies analysis diagnostics through `Analyzed` and refreshes them when source changes.

- [ ] **Step 2: Run RED tests**

Run:

```bash
go test ./internal/semantics/analysis ./internal/ir/mir ./internal/pipeline ./internal/lsp -run 'TestRunChecksDefiniteInitializationAndPublishesCleanup|TestRunPreservesEffectOrdering|TestCleanupQueriesAreFunctionScoped|TestGenerateMIRConsumesAnalysisCleanup|TestAdvancePublishesAnalyzedArtifact|TestRetainedAnalysisDiagnosticsRefreshAfterEdit' -count=1
```

Expected: FAIL because cleanup queries/`phase.Analyzed` do not exist and old packages still own the algorithms.

- [ ] **Step 3: Move effect algebra/building into `analysis` without weakening exhaustiveness**

Move the existing closed `Op` set, visitor contract, THIR effect builder, and validator into package `analysis`. Preserve compile-time exhaustive visitor behavior; only visibility/package ownership changes.

Keep the effect stream local inside `Run`; do not add an `Effects` field to `analysis.Module`.

- [ ] **Step 4: Move definite initialization into `analysis`**

Move the current lattice/worklist/visitor implementation as package-private code. `Run` invokes it directly with local effects and the supplied diagnostics. No definite-init state is retained after `Run`.

- [ ] **Step 5: Move ownership and cleanup publication into `analysis`**

Move ownership solver/reference/expression logic under `analysis`, replacing `flowresult` references with direct `Module` flow queries and replacing `ownershipresult.CleanupPlan` with a private cleanup structure in `analysis.Module`.

Implement the cleanup query methods above. Move ownership validation into `analysis.Validate` and validate typed/use facts through THIR plus durable analysis facts, not removed result packages.

Delete `effect`, `definiteinit`, `ownership`, and `ownershipresult` packages after all tests are migrated.

- [ ] **Step 6: Migrate MIR to semantic cleanup queries**

Remove `Ownership` from `mir.LoweringInput`. Pass `Analysis` into function/expression lowering and replace direct cleanup-plan map indexing with semantic query calls.

Do not move drop policy into MIR; MIR only materializes cleanup decisions already published by analysis.

- [ ] **Step 7: Collapse module phases and reset/invalidation logic**

Add `phase.Analyzed`; remove `FlowTyped`, `Effects`, `DefiniteInit`, `Ownership`.

Update:
- `nextModulePhase` and retained prerequisite logic;
- scheduler target before Usage from Ownership → Analyzed;
- `advanceModulePhase`: CFG → one `analysis.Run` → Analyzed;
- project usage barrier preconditions;
- `Module.ResetToPhase`: before Analyzed clears `Analysis`, MIR, LLVM only;
- project/module reset tests;
- diagnostics phase ranges;
- LSP retained-phase/copy bounds from Ownership → Analyzed.

Keep the internal analysis order explicit in ordinary Go control flow; do not introduce generic phase/pass registration.

- [ ] **Step 8: Verify Task 4 GREEN**

Run focused checks first:

```bash
gofmt -w internal/semantics/analysis internal/ir/mir internal/ir/exprlower internal/module internal/phase internal/pipeline internal/project internal/lsp internal/diagnostics
go test ./internal/semantics/analysis ./internal/ir/mir ./internal/ir/exprlower ./internal/module ./internal/phase ./internal/pipeline ./internal/project ./internal/lsp ./internal/diagnostics -count=1
go test -race ./internal/semantics/analysis ./internal/pipeline ./internal/project -count=1
go vet ./...
git diff --check
```

Then prove the old architecture is gone:

```bash
! rg -n 'flowresult|ownershipresult|effect\.Result|\.Effects\b|\.Ownership\b|FlowTyped|DefiniteInit|phase\.Effects|phase\.Ownership' internal --glob '*.go'
```

Expected: all commands pass and no old lifecycle/result dependency remains.

- [ ] **Step 9: Rules check and review stop**

Update local plan with validation and explicit confirmation that:
- effect visitor exhaustiveness remains;
- no effect/definite-init scratch state is retained in `analysis.Module`;
- cleanup map layout remains private;
- MIR contains no new drop policy;
- no compatibility facade or generic pass abstraction was added.

Stop for user review/commit approval.

**Intended commit subject after approval:** `Collapse CFG semantic analysis`

---

### Task 5: Align current documentation and prove repository-wide behavior

**Purpose:** Remove stale architectural guidance after code migration and run the full validation matrix before declaring the migration complete.

**Files:**
- Modify current architecture docs as applicable:
  - `docs/compiler-architecture.md`
  - `docs/architecture/README.md`
  - `docs/architecture/infrastructure.md`
  - `docs/architecture/semantics-analyses.md`
  - `docs/architecture/semantics-bindings.md`
  - `docs/compiler-framework/README.md`
  - `docs/compiler-framework/change-paths.md`
  - `docs/compiler-framework/semantic-results.md`
  - `docs/compiler-framework/ownership-vocabulary.md`
- Historical migration notes such as `docs/compiler-framework/effect-stream-migration.md` may retain historical terminology if clearly marked historical; do not rewrite history merely to make `rg` clean.
- Modify no production code unless verification exposes a real defect; any defect fix returns to RED→GREEN and is recorded as a plan ruling.

**Interfaces:**
- Consumes: final Tasks 1–4 architecture.
- Produces: documentation that teaches one durable path: AST → THIR → CFG → Analysis → MIR, plus a validated branch ready for whole-branch review.

- [ ] **Step 1: Update current architecture documentation**

Document:
- typechecking publishes THIR and owns private evidence/constant evaluation;
- constants are generation-owned symbol state;
- `analysis.Run` owns flow/effects/definite-init/ownership execution while retaining only durable flow/cleanup facts;
- pipeline lifecycle includes `Analyzed`, not the four removed analysis phases;
- `Module` durable fields no longer include phase `Result` objects;
- incremental function reuse remains deferred.

Remove current-doc references that tell maintainers to use deleted result packages or lifecycle phases.

- [ ] **Step 2: Run stale-guidance scan**

Run:

```bash
rg -n 'typecheckresult|constantresult|flowresult|ownershipresult|FlowTyped|DefiniteInit|module\.Effects|module\.Ownership|module\.Typechecking|module\.Constants' docs \
  --glob '*.md' \
  --glob '!docs/superpowers/specs/2026-09-28-artifact-oriented-semantics-design.md' \
  --glob '!docs/compiler-framework/effect-stream-migration.md'
```

Expected: no matches in current documentation. Any intentional historical reference must be explicitly identified rather than silently excluded.

- [ ] **Step 3: Run full repository validation**

Run fresh, complete commands and read their output:

```bash
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
git diff --check
```

If `go test -race ./...` or LSP timing exceeds the harness timeout without a test failure, record the exact timeout and rerun affected packages separately; do not report the full command as passed unless it actually exits 0.

- [ ] **Step 4: Architectural invariant scan**

Run:

```bash
! test -d internal/semantics/typecheckresult
! test -d internal/semantics/constantresult
! test -d internal/semantics/flowresult
! test -d internal/semantics/ownershipresult
! test -d internal/semantics/effect
! test -d internal/semantics/definiteinit
! test -d internal/semantics/ownership
rg -n '^\s*(AST|THIR|CFG|Analysis|MIR|LLVMIR|ModuleScope|SymbolIndex)\b' internal/module/module.go
rg -n 'Typechecked|CFG|Analyzed|Usage|MIR|Backend|Finalize' internal/phase/phase.go
```

Expected: deleted package directories stay gone; module/phase output demonstrates the intended durable concepts without reintroduced phase-result fields.

- [ ] **Step 5: Update local plan and request whole-branch review**

Record:
- every completed task/commit;
- all validation results;
- all rulings/deviations;
- remaining risks, if any;
- Rules check for the whole migration.

Then use Superpowers `requesting-code-review` for a fresh whole-branch review against the spec and this plan before merge/integration.

**Intended commit subject after approval:** `Document artifact-oriented semantics`

---

## Plan self-review

### Spec coverage

- Base typecheck result removal + THIR publication: Task 1.
- Constant lifetime split + symbol ownership: Task 2.
- Durable analysis artifact + flow facts: Task 3.
- Transient effects/definite-init + ownership cleanup + phase collapse: Task 4.
- MIR/LSP/reset/diagnostic consumers: Tasks 1–4, with full regression coverage in Task 5.
- Incremental caching deferred: preserved as a non-goal; no task implements it.
- Documentation/solo-maintainer architecture: Task 5.

No spec requirement is intentionally deferred except Slice 6 (function-level reuse), which the spec explicitly excludes from this migration.

### Type/interface consistency

- Task 1 finalizes `typechecker.Check(...) *thir.Module` and removes base result state.
- Task 2 uses only `SymbolIndex` for authoritative constants.
- Task 3 introduces the final `analysis.Input`, `analysis.Run`, and flow query API.
- Task 4 extends `analysis.Run` and `analysis.Module`; it does not replace their Task 3 signatures.
- MIR ends with THIR + CFG + Analysis + Scope + SymbolIndex, matching the spec.

### Proportion / maintenance check

The plan deliberately avoids intermediate compatibility packages. The only staged API is `analysis.Run`: it is introduced with its final signature in Task 3 and gains the remaining owned algorithms in Task 4. No temporary result wrapper or alias is planned.
