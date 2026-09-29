# Semantics: typechecking and program analyses

This map records the current semantic pipeline observed in source. Verify mutable details against linked implementations and tests.

## Durable artifact path

```text
AST → THIR → CFG → Analysis → MIR
```

`internal/pipeline/pipeline.go` schedules the durable lifecycle:

```text
Parsed → Collected → Bound → Resolved → Typechecked → CFG → Analyzed → Usage → MIR → Backend
```

Operations inside typechecking and analysis are not independent lifecycle artifacts.

## Base typechecking

`internal/semantics/typechecker.Check` consumes resolved AST and generation-owned symbol state. One checker operation owns:

- base type rules and diagnostics;
- call adaptation, conversions, match and iteration decisions;
- lazy constant queries and final constant publication;
- private AST-to-THIR construction evidence.

Its durable output is `*thir.Module`. Private evidence and evaluator caches die when the operation returns. THIR carries effective/default call arguments, generated binding identity, checked iteration plans, expression types, use kinds, and other decisions required downstream.

Canonical base expression types come from THIR. `module.Module.EffectiveExprType` first checks flow-refined evidence in `Analysis`, then falls back to the THIR type.

## Constants

Finalized module constants are generation-owned facts in `symbols.Index`, keyed by `symbols.SymbolID`. `PublishConstant`, `ConstantValue`, and `ClearConstants` provide the narrow storage API. The typechecker's evaluator cache and cycle-detection state remain operation-local.

Cross-module queries resolve the defining module and read its symbol index. MIR static-data lowering reads the same authoritative state; there is no separate constant artifact.

## CFG

`internal/ir/cfg` builds typed blocks, ordered sites, terminators, and explicit edge meaning from THIR. `cfg.Module.Validate` checks topology before semantic analysis. Consumers use `cfg.SiteID` only relative to the owning `moduleid.FunctionID`.

## Unified analysis operation

`internal/semantics/analysis.Run` consumes:

- canonical THIR;
- CFG topology;
- module scope;
- generation-owned symbol index.

It executes, in order:

1. flow refinement and provenance analysis;
2. transient semantic-effect extraction and validation;
3. definite-initialization checking;
4. ownership, borrow/liveness analysis, and cleanup planning;
5. durable analysis-fact and cleanup-evidence validation when source diagnostics permit it.

These remain distinct algorithms, but they share one ownership boundary and one pipeline phase.

### Durable `analysis.Module` facts

Only facts needed after `Run` returns are retained:

- path-sensitive expression type refinements;
- payload/case and named-variant field evidence;
- storage and value origins;
- aggregate slot decomposition;
- source-level cleanup decisions used by MIR.

Backing maps and producer methods are private. Consumers use semantic queries such as `ExprType`, `Payload`, `CaseTest`, `VariantField`, `Origins`, `AggregateSlots`, `DropsAfterSite`, `DropsBeforeReturn`, `DropsBeforeAssign`, `DiscardedValue`, `ProjectionBase`, and match-drop queries. Slice-valued queries return detached storage.

### Transient analysis state

The ordered effect stream, definite-initialization lattices, ownership solver states, worklists, loan sets, and liveness maps exist only during `Run`. They are not stored on `module.Module` and are not pipeline checkpoints.

Effect operations form a private closed family: define, write, use, borrow, iterate, discard, and call boundaries. Their visitor contract keeps consumers exhaustive without publishing another durable artifact.

## Ownership and cleanup

The ownership solver consumes THIR use kinds, type capabilities, flow provenance, transient effects, and CFG paths. It diagnoses move/borrow violations and records source-level cleanup obligations in `analysis.Module`.

Cleanup channels remain distinct because their event ordering differs:

- scope-site drops occur while MIR processes CFG sites;
- return drops occur after the returned value is evaluated;
- overwrite, discarded-value, projection-base, and match-payload drops attach to their exact source event.

MIR does not rediscover cleanup policy. It queries `analysis.Module`. MIR-created temporary drops remain MIR's responsibility because those temporaries have no source symbol.

## Usage

`internal/semantics/usage` runs after `Analyzed` as a project barrier. It reads usage and mutable-required facts from each generation-owned `symbols.Index` and emits diagnostics. It does not add another module artifact.

## Validation boundaries

- Typechecking publishes THIR; pipeline validates THIR.
- CFG construction publishes topology; pipeline validates CFG.
- `analysis.Run` validates transient effects plus durable refinement, provenance, aggregate, and cleanup evidence when source errors do not explain incomplete evidence.
- MIR construction publishes MIR; pipeline validates MIR before backend emission.

Invalid source produces source diagnostics. Broken representation contracts produce internal-evidence diagnostics rather than silent fallback.

## Incremental ownership

`module.Module` owns current-generation AST, THIR, CFG, Analysis, MIR, LLVM text, scope, and symbol index. Reset gates follow those durable artifacts. Function-level cross-generation reuse remains deferred; stable identities alone are not cache validity.
