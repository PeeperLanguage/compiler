# Semantic Artifact Ownership

Status: **current architecture**.

This document records the durable semantic ownership model after the artifact-oriented migration. Historical migration details remain in Git history and the explicitly historical framework notes.

## Rule

Persistent objects represent durable compiler concepts. Compiler passes are operations over those concepts and keep temporary state private.

```text
AST → THIR → CFG → Analysis → MIR
```

## Durable module artifacts

`module.Module` owns current-generation artifacts:

- `AST`: parsed source syntax;
- `THIR`: canonical typed source representation;
- `CFG`: control topology and semantic edges;
- `Analysis`: durable path-sensitive facts and source cleanup decisions;
- `MIR`: lowered executable representation;
- `LLVMIR`: emitted backend text;
- `ModuleScope` and `SymbolIndex`: generation-owned semantic environment.

Pipeline phases correspond to these durable checkpoints. Internal flow, effect, initialization, and ownership steps execute together under `Analyzed`.

## Typechecking ownership

`typechecker.Check` owns private checking evidence, constant evaluator cache state, and THIR construction for one operation. It publishes THIR directly. Later phases do not access checker scratch maps or reconstruct call, match, conversion, use-kind, or iteration decisions from AST.

## Constant ownership

Authoritative constant values are semantic facts about symbols. They live in the defining module's `symbols.Index` under `symbols.SymbolID`. Lazy evaluation caches and cycle tracking live only on one evaluator instance.

This separation ensures fingerprints, imports, and MIR read finalized generation state without turning evaluator scratch entries into public artifacts.

## Analysis ownership

`analysis.Run` consumes THIR, CFG, scope, and symbol state. It performs flow refinement, transient effect extraction, definite-initialization checking, and ownership analysis.

`analysis.Module` retains only facts needed by later consumers:

- refined expression types and variant evidence;
- storage/value origins and aggregate slots;
- cleanup decisions queried by MIR.

The effect stream, worklists, initialization states, ownership states, and loan/liveness internals are temporary. Backing maps and mutation operations remain private.

## Reset and invalidation

`Module.ResetToPhase` clears artifacts below the retained durable checkpoint:

- below `Typechecked`: THIR and later artifacts;
- below `CFG`: CFG and later artifacts;
- below `Analyzed`: analysis and later artifacts;
- below `MIR`: MIR and backend output;
- below `Backend`: LLVM output.

Semantic generation reset replaces module scope and symbol index, including published constants. Reusable artifacts must have explicit production, consumption, invalidation, and reset rules; a scheduler phase alone is not a cache key.

## Consumer rule

Consumers depend on the narrow artifact they need:

- tooling uses THIR plus `Module.EffectiveExprType`;
- lowering consumes THIR, CFG, Analysis, scope, and symbol state;
- project fingerprints read canonical symbol/type/constant state;
- no consumer receives a generic semantic database or mutable backing map.

When a later operation needs a new durable fact, first decide whether it belongs in THIR, CFG, Analysis, or symbol state. Do not create a new lifecycle object merely because a pass computed it.
