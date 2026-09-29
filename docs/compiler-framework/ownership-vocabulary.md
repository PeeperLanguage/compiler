# Ownership Vocabulary

Status: **current implementation**.

This document describes the vocabulary used by typechecking, unified semantic analysis, MIR, and the backend.

## Type capability

`internal/semantics/typeinfo` owns compositional ownership capability. Each semantic type answers whether values are implicitly copied, explicitly copied, moved, and/or require destruction. Containers derive behavior from their children according to language rules.

The type capability describes a value class. It does not decide what one source occurrence does.

## Per-use classification

`typeinfo.UseKind` classifies an individual typed expression use:

- `UseRead`: observe or borrow without consuming the source value;
- `UseCopy`: copy while leaving the source live;
- `UseMove`: consume the source value.

The typechecker publishes this classification into THIR. Ownership analysis consumes THIR use kinds instead of re-deriving ordinary call, binding, assignment, return, literal, or conversion consumption from AST shape.

## Place and provenance

`internal/semantics/place` owns storage roots and ordered projections. Flow analysis publishes storage/value origins and variant payload paths in `analysis.Module`. Ownership uses those facts to track loans, aliases, moves, and reference escape contracts.

A new reference-bearing value shape requires a provenance audit even when its use kind is already expressible.

## Transient semantic operations

Inside `analysis.Run`, a private ordered effect stream represents:

- storage definition;
- writes/replacements;
- reads/copies/moves;
- borrows;
- sequence iteration access;
- discarded values;
- call lifetime boundaries.

Definite initialization and ownership consume the same operation order. The stream is scratch state, not a module artifact.

## Durable cleanup decisions

Ownership analysis publishes source-level cleanup decisions through `analysis.Module` queries. MIR consumes those decisions and does not originate source cleanup policy.

The channels remain distinct:

- drops after a CFG scope-exit site;
- drops before a return, after its value is evaluated;
- drops before replacement assignment;
- discarded owned values;
- owned projection bases;
- omitted match fields or whole payloads.

MIR may additionally destroy temporaries it creates itself. Programmer-written `free` and MIR temporary destruction are not competing ownership decisions.

## Extension rule

A new syntax construct should require no ownership-specific code when it can be expressed using existing type capability, use kind, place/provenance, and semantic operations.

Add ownership logic only when the construct introduces a genuinely new lifetime relationship. In that case:

1. define the semantic relationship at its canonical producer;
2. preserve exact source and stable identity;
3. make transient consumers classify it exhaustively;
4. publish only durable facts required after `analysis.Run`;
5. add path-sensitive diagnostics and MIR cleanup regressions.

## Validation

The analysis boundary validates that typed use evidence and cleanup plans refer to real THIR/CFG identities and legal type operations. Validation failures are compiler bugs; invalid user programs remain source diagnostics.
