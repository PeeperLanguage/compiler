# Compiler Framework

Framework migration is now implemented as a set of canonical compiler kernels,
not a universal pass/visitor framework.

Read [`../compiler-architecture.md`](../compiler-architecture.md) first. It is the
current architecture contract and extension guide. This directory retains deeper
migration notes, artifact ownership analysis, validation design, and historical
change-path evidence.

## Final architecture decision

Peeper uses four complementary mechanisms:

1. **Canonical structure** — `ast.Inspect`, `typeinfo.ForEachChild`,
   `place.Project`/`Decompose`, and `graph.Directed` ensure structural knowledge is
   written once.
2. **Canonical semantic evidence** — typechecker results, typed CFG edges/sites,
   flow evidence, and ordered semantic effects prevent downstream rediscovery;
   `effect.Visitor` makes the semantic operation set exhaustive for consumers.
3. **Generic mechanics** — `graph.Worklist` and shared graph topology remove
   repeated scheduling/adjacency code without hiding phase-specific lattices.
4. **Explicit true extension points** — resolver/typechecker/CFG/effect/HIR and
   semantic type representation decisions remain exhaustive where behavior really
   differs.

Goal is not "every phase visits every AST node". Goal is stronger:

> phases that only care what syntax **does** should consume canonical semantic
> evidence and never need to know that syntax kind exists.

A new ordinary expression can therefore reuse `Use`/`Borrow`/`Write`/`Define` and
inherit definite-init, ownership, liveness, and cleanup behavior. A new composite
semantic type declares child structure and ownership composition once and nested
copy/drop behavior follows automatically.

## Current canonical owners

| Concern | API |
| --- | --- |
| AST recursion | `ast.Inspect` / node `forEachChild` |
| semantic type structure | `typeinfo.ForEachChild` / `TypeChildRelation` |
| type ownership composition | sealed `typeinfo.Type.ownershipShape` |
| place/projection grammar | `place.Project`, `place.Decompose` |
| graph adjacency | `graph.Directed` |
| fixed-point scheduling | `graph.Worklist` |
| control topology | `cfg.Graph` + typed `cfg.Edge` |
| value/storage behavior | `effect.Result` + exhaustive `effect.Visitor` |
| cleanup evidence | `ownershipresult.Result` |

## Contract philosophy

Old framework experiments tried to make every phase acknowledge every AST kind.
That catches omissions but preserves distributed work. Final architecture narrows
those contracts to actual semantic boundaries.

- Structural consumers reuse canonical traversal.
- Generic analyses consume semantic operations.
- A new semantic type is sealed until it declares structure + ownership policy.
- Remaining source-inspection contracts guard only closed sets that Go cannot make
  exhaustive directly.
- Artifact validators reject malformed evidence at producer boundaries.

See [`change-paths.md`](change-paths.md) for historical evidence that motivated
migration. Its old change counts are baseline measurements, not the current
recommended extension path.
