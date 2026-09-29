# Change paths through the compiler

This is the concrete companion to [`../compiler-architecture.md`](../compiler-architecture.md).
Read that document first: the goal is **not** to make every phase acknowledge every
syntax node. The goal is to edit the few owners of unique semantics and let canonical
structure/evidence drive the rest.

Mandatory engineering requirements live in [`RULES.md`](../../RULES.md); compiler design-review guidance lives in [`COMPILER_GUIDELINES.md`](../../COMPILER_GUIDELINES.md). Current architecture claims in this guide must be verified against source.

## The rule for every change

Before adding a switch or recursive walk, ask what fact you need and who already owns it.

| Need | Canonical owner/API |
| --- | --- |
| AST children | node `forEachChild` + `ast.Inspect` |
| semantic type children | `typeinfo.ForEachChild` |
| copy/drop composition | sealed `typeinfo.Type.ownership` |
| storage projection | `place.Project` / `place.Decompose` |
| graph adjacency | `graph.Directed` |
| fixed-point scheduling | `graph.Worklist` |
| name identity | `symbols.Index` and lexical scopes |
| type/use/adaptation decisions | THIR published by `typechecker.Check` |
| control topology | `cfg.Module` / typed `cfg.Edge` |
| evaluation/storage actions | transient effect stream inside `analysis.Run` |
| flow and cleanup evidence | query-only `analysis.Module` |

If a later phase needs a durable fact from an earlier owner, extend THIR, CFG, Analysis, or symbol state as appropriate.
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
6. **Analysis effect builder** — map evaluation to existing define/write/use/borrow/iterate/discard/call-boundary operations.
7. **THIR/MIR** — publish typed source evidence and lower the construct when no existing MIR shape covers it.

Do **not** add a corresponding AST case to definite initialization, ordinary ownership
state transitions, liveness, or cleanup. If one of those needs syntax to understand the
new feature, the semantic boundary is probably missing evidence.

### Syntax with a genuinely new semantic action

Only add a new private analysis effect operation when the existing operations cannot express the behavior. Then it is a true closed extension point:

1. add the sealed operation in `internal/semantics/analysis`;
2. derive it in evaluation order;
3. validate its required identity/evidence;
4. extend the private effect visitor; every exhaustive consumer then fails compilation until it explicitly decides what the operation means;
5. add focused Go tests and, for language behavior, `x_test` source fixtures.

A new effect is therefore a compile-time introduction to semantic consumers, not a
search-and-remember exercise. It must never fall through as an accidental no-op.

## Path 2 — add a semantic type

A semantic type is not complete until it satisfies the sealed `typeinfo.Type` contract.
The first edits are therefore local to `internal/semantics/typeinfo`:

1. add human-facing `Text` behavior;
2. declare semantic attributes and ordered child slots in `structure`, using correct
   `TypeChildRelation` values;
3. implement required `isSameType`, `isSized`, `isLowerable`, and `ownership` behavior.

`ForEachChild` and semantic fingerprinting derive from the same structure. Each
intrinsic query owns its cycle policy and reuses canonical children where needed.

Then add decisions owned outside the type model, for example:

- compatibility and conversions;
- MIR/backend lowering;
- source-type conversion if new syntax is involved.

Sealed type methods and focused type/IR tests guard the remaining closed type-kind sites.
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

- Value is read/copied/moved? Extend THIR use classification or the transient use operation, not an ownership expression switch.
- Place is borrowed? Derive a borrow operation with exact operand/place identity.
- Storage is introduced/replaced? Use the existing define/write operations.
- A long-lived sequence iteration access is needed? Use the iteration operation and CFG loop identity.
- A branch/case fact is needed? Publish it through `analysis.Module`.
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
| new effect operation | private visitor compile failure + transient evidence validator |
| malformed CFG/analysis/MIR | artifact validator |
| changed Peeper behavior | focused tests + `x_test` fixture |

The ideal result is that a new ordinary syntax node causes **fewer** downstream edit
requirements than before, while genuinely new semantics become **more** explicit.

## Pre-review audit

Before considering a compiler architecture change complete, search for accidental
parallel machinery:

```bash
# private fixed-point schedulers in semantic analyses
rg -n 'queue|queued' internal/semantics

# syntax knowledge leaking into generic analysis consumers
rg -n 'case \*ast\.' internal/semantics/analysis

# selector/index projection reimplementation
rg -n 'SelectorExpr|IndexExpr' internal/semantics/analysis
```

Interpret results semantically rather than mechanically. A return-specific ownership
policy or the syntax-aware effect publisher is valid; another generic child/projection
walk is not.

Then run the verification commands documented in
[`../compiler-architecture.md`](../compiler-architecture.md).
