# Change paths through the compiler

This is the concrete companion to [`../compiler-architecture.md`](../compiler-architecture.md).
Read that document first: the goal is **not** to make every phase acknowledge every
syntax node. The goal is to edit the few owners of unique semantics and let canonical
structure/evidence drive the rest.

Mandatory repository policy remains [`RULES.md`](../../RULES.md); durable compiler
principles remain [`COMPILER_GUIDELINES.md`](../../COMPILER_GUIDELINES.md).

## The rule for every change

Before adding a switch or recursive walk, ask what fact you need and who already owns it.

| Need | Canonical owner/API |
| --- | --- |
| AST children | node `forEachChild` + `ast.Inspect` |
| semantic type children | `typeinfo.ForEachChild` |
| copy/drop composition | sealed `typeinfo.Type.ownershipShape` |
| storage projection | `place.Project` / `place.Decompose` |
| graph adjacency | `graph.Directed` |
| fixed-point scheduling | `graph.Worklist` |
| name identity | resolver/binding results |
| type/use/adaptation decisions | `typecheckresult.Result` |
| control topology | `cfg.Module` / typed `cfg.Edge` |
| evaluation/storage actions | `effect.Result` |
| cleanup/drop evidence | `ownershipresult.Result` |

If a later phase needs a fact from an earlier owner, extend the owner's published result.
Do not reconstruct the fact from AST shape downstream.

## Path 1 — add an expression or statement

First classify the feature.

### Syntax using existing semantics

Expected edits:

1. **AST** — define the node and its stable identity/location.
2. **AST children** — implement `forEachChild` once. Generic `ast.Inspect` users then
   see the new children automatically.
3. **Parser** — parse/recover the source syntax.
4. **Resolver/typechecker** — only where name, scope, type, call, or adaptation semantics
   differ.
5. **CFG** — only when control topology differs.
6. **Effect publisher** — map evaluation to existing `Define`/`Write`/`Use`/`Borrow`/
   `Iterate`/`Discard`/call-boundary operations.
7. **HIR** — lower the source construct when no existing source lowering covers it.

Do **not** add a corresponding AST case to definite initialization, ordinary ownership
state transitions, liveness, or cleanup. If one of those needs syntax to understand the
new feature, the semantic boundary is probably missing evidence.

### Syntax with a genuinely new semantic action

Only add a new `effect.Op` when the existing operations cannot express the behavior.
Then the effect is a true closed extension point:

1. add the sealed operation in `internal/semantics/effect`;
2. publish it in evaluation order;
3. validate its required identity/evidence;
4. extend `effect.Visitor`; every exhaustive consumer then fails compilation until it
   explicitly decides what the operation means;
5. add focused Go tests and, for language behavior, `x_test` source fixtures.

A new effect is therefore a compile-time introduction to semantic consumers, not a
search-and-remember exercise. It must never fall through as an accidental no-op.

## Path 2 — add a semantic type

A semantic type is not complete until it satisfies the sealed `typeinfo.Type` contract.
The first edits are therefore local to `internal/semantics/typeinfo`:

1. add the type and its `TypeNode`/`Text` behavior;
2. enumerate immediate contained types in `forEachChild` with correct
   `TypeChildRelation` values;
3. declare `ownershipShape` — whether it is a leaf/container and how copy/drop composes.

That is the structural extension point. After it is correct, recursive containment and
copy/drop propagation use the canonical traversal automatically.

Then add only representation-specific decisions that truly differ, for example:

- equality/compatibility;
- sizing or lowerability with special cycle/ABI rules;
- exported semantic fingerprinting;
- HIR/backend lowering;
- source-type conversion if new syntax is involved.

`internal/contracts/type_dispatch_test.go` guards the remaining closed type-kind sites.
Do not add new private recursive type-child walkers to satisfy one query.

## Path 3 — add a graph-backed analysis

1. Store topology in `graph.Directed`; keep domain semantics in typed node/edge metadata.
2. Reuse `graph.Worklist` if the analysis is a rescheduling fixed point.
3. Keep the analysis's state, join, transfer, direction, diagnostics, and edge semantics
   in its own package.
4. On CFG, use edge kinds/case metadata. Never infer true/false/case/loop meaning from
   successor position.

A domain graph may wrap the graph kernel; it should not own a second adjacency index.

## Path 4 — add an ownership/flow rule

Start from semantic evidence, not syntax.

- Value is read/copied/moved? Extend the producer of `effect.Use` or its typechecker
  decision, not an ownership expression switch.
- Place is borrowed? Publish `effect.Borrow` with exact operand/place identity.
- Storage is introduced/replaced? Use `effect.Define` / `effect.Write`.
- A long-lived sequence iteration access is needed? Use `effect.Iterate` and CFG loop
  identity.
- A branch/case fact is needed? Put it on CFG/flow evidence.
- A type recursively contains ownership/reference behavior? Put the relationship on the
  semantic type structure.

Return pointer/reference provenance is currently a deliberate ownership policy that
straddles returned-value evaluation, so ownership retains a return-specific control
hook. If another construct needs the same control semantic, publish a shared control
operation rather than adding parallel syntax reconstruction.

## What should break when a new thing is added

The architecture intentionally distinguishes automatic composition from true extension
points:

| Change | Expected guard |
| --- | --- |
| new AST field/node child | AST traversal completeness test |
| new syntax kind at a syntax-aware closed site | dispatch contract / compiler failure |
| new semantic type | sealed `typeinfo.Type` compile failure until structure/ownership declared |
| new semantic type missing representation decision | type dispatch contract |
| new effect operation | `effect.Visitor` compile failure + artifact validator |
| malformed CFG/effects/ownership/IR | artifact validator |
| changed Peeper behavior | focused tests + `x_test` fixture |

The ideal result is that a new ordinary syntax node causes **fewer** downstream edit
requirements than before, while genuinely new semantics become **more** explicit.

## Pre-review audit

Before considering a compiler architecture change complete, search for accidental
parallel machinery:

```bash
# private fixed-point schedulers in semantic analyses
rg -n 'queue|queued' internal/semantics

# syntax knowledge leaking back into generic consumers
rg -n 'case \*ast\.' internal/semantics/definiteinit internal/semantics/ownership

# selector/index projection reimplementation
rg -n 'SelectorExpr|IndexExpr' internal/semantics/ownership internal/semantics/effect
```

Interpret results semantically rather than mechanically. A return-specific ownership
policy or the syntax-aware effect publisher is valid; another generic child/projection
walk is not.

Then run the verification commands documented in
[`../compiler-architecture.md`](../compiler-architecture.md).
