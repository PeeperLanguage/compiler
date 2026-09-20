# Peeper Compiler Architecture

This document records compiler architecture observed in source. Verify mutable details against linked symbols and current tests. It explains current implementation and rationale; it does not make current phase order, package boundaries, or representations permanent.

Language intent is recorded in [`language-spec.md`](language-spec.md) and [`ownership-pointer-model.md`](ownership-pointer-model.md). Conflicts among documents, source, tests, and explicit requirements require review rather than automatic precedence.

## Current design rationale

Peeper's compiler is organized around one rule:

> Define structure once. Define unique semantics once. Derive everything else.

The architecture is intended to prevent the failure mode where a feature looks
complete in parsing or typechecking but one forgotten ownership, flow, cleanup, or
lowering walk survives until production.

Three patterns currently drive the design:

1. **Shared mechanisms for repeated concerns.** Tree structure, semantic type
   structure, place projections, graph adjacency, worklist scheduling, and value
   effects currently each have one implementation owner.
2. **Compositional behavior.** A new container/type/syntax construct that is built
   from existing semantic operations and provenance evidence can reuse ownership,
   definite-init, liveness, and cleanup behavior. New reference-bearing value shapes
   still need an explicit provenance audit; operation coverage alone is insufficient.
3. **Loud true extension points.** If a new construct introduces genuinely new
   semantics, syntax-aware owners must make an explicit decision. Unknown sealed
   node/effect kinds panic or fail validation instead of being silently skipped.

This is not a "visitor everywhere" architecture and not an "effects replace every
phase" architecture. Visitors/switches are appropriate at syntax interpretation
boundaries. Generic downstream analyses consume canonical semantic evidence.

## Pipeline

`internal/pipeline/pipeline.go` owns scheduling. Current per-module order is:

```text
parse
  -> collect
  -> bind
  -> resolve
  -> constants
  -> typecheck
  -> CFG
  -> flow typing
  -> semantic effects
  -> definite initialization
  -> ownership + cleanup
  -> usage
  -> HIR
  -> MIR
  -> backend
```

Project/module readiness and incremental checkpoints are explicit because current
stages have different dependency, barrier, and invalidation rules. A replacement would need to represent those differences rather than hide them behind a uniform interface.

## Representation boundary

Syntax remains source shape. Semantic facts remain side tables/results keyed by
stable identity.

```text
                         source
                           |
                        AST / parser
                           |
        +------------------+------------------+
        | syntax-aware semantic owners        |
        | resolver, typechecker, CFG builder, |
        | effect publisher, HIR lowering      |
        +------------------+------------------+
                           |
             canonical semantic evidence
        symbols / types / places / CFG / effects
                           |
        +------------------+------------------+
        | syntax-agnostic generic analyses    |
        | definite-init, most ownership value |
        | flow/liveness/worklist mechanics    |
        +------------------+------------------+
                           |
                      cleanup / IR
```

Current later stages consume decisions published by earlier work. For example, typechecking records borrowed call arguments and effects publish `Borrow`; ownership consumes that evidence rather than inferring it again from call syntax. This is current design, not proof that same stage boundaries must remain.

## Current structural mechanisms

### AST: `ast.Inspect`

AST nodes own child shape through `forEachChild`. Generic consumers use
`ast.Inspect` rather than maintaining private recursive switches.

Under current AST contract, each node declares children once. Semantic stages may still dispatch on node kind when behavior genuinely differs.

### Semantic types: `typeinfo.ForEachChild`

`typeinfo.Type` is sealed inside `typeinfo` and requires explicit intrinsic
operations:

- `structure` — local semantic identity plus ordered child slots and their
  `TypeChildRelation`;
- `isSameType`, `isSized`, `isLowerable`, and `ownership` — operation-specific
  behavior that new types must implement.

`typeinfo.ForEachChild` projects present children from `structure`; semantic
fingerprinting consumes the same structure, including absent slots.
`ForEachChild` is the semantic-type equivalent of `ast.Inspect`.
Containment and ownership capability queries consume this structure. Sizing and
lowerability intentionally keep separate recursion policies because recursive
cycles mean different things to those queries.

Consequence: adding a semantic type cannot satisfy `typeinfo.Type` until it
declares structure, equality, representation, and ownership behavior. Required
methods enforce presence; behavioral tests still prove each implementation's policy.

Typed-nil ownership inputs retain explicit-copy/no-drop answers. The ownership
query checks `typednil.IsNil` before dispatch, while structural traversal separately
skips typed-nil children. This is not a compiler-wide typed-nil validation guarantee.

### Places: `place.Project` / `place.Decompose`

`internal/semantics/place` currently handles selector/index place grammar. Consumers use its evidence instead of independently peeling `SelectorExpr`/`IndexExpr` to answer which storage is touched.

Current place facts carry:

- root symbol when storage is named;
- ordered field/index projections;
- temporary base identity when expression projects from a temporary.

Addressability, mutability, effect publication, ownership, and flow origin logic
build on this contract.

### Graphs: `graph.Directed`

`internal/graph.Directed` owns directed adjacency and reverse adjacency once.
Domain graphs keep semantic edge data on top:

- import/type dependency graphs use `graph.Graph`;
- CFG owns `cfg.Edge` kinds/case metadata while storing site/block topology in
  `graph.Directed`.

A second successors/predecessors store would duplicate current topology state. Reuse or deliberately replace the shared kernel when contracts differ.

CFG terminators and ordered block sites define control flow. Block/site edge
indexes are derived at construction and immutable by consumer convention after
publication. Rebuild the CFG for a new topology generation; do not independently
mutate terminators or indexes while downstream evidence still names its sites.
Validators inspect all stored edges, including disconnected foreign endpoints,
and compare site targets, kinds, and case labels against the block topology.

### Fixed-point scheduling: `graph.Worklist`

`graph.Worklist` owns FIFO scheduling, pending-node deduplication, and
rescheduling. Flow, definite-init, ownership, and liveness retain their own state,
join, direction, edge transfer, diagnostics, and convergence rules.

This is currently smaller than a generic dataflow framework. Shared mechanics
are centralized; semantic lattices remain visible in their owning packages.

## Current semantic effects

`internal/semantics/effect` publishes value/storage behavior in source evaluation
order. Current operations are:

- `Define` — storage becomes a binding, optionally initialized from a value;
- `Write` — existing place is replaced/mutated;
- `Use` — read/copy/move of a place;
- `Borrow` — shared/mutable/raw borrow with exact operand identity;
- `Iterate` — long-lived sequence-loop access and hidden carrier identity;
- `Discard` — produced value is thrown away;
- `CallBegin` / `CallEnd` — call-lifetime brackets for argument loans.

`effect.Visitor` is the exhaustive consumer boundary. An `effect.Op` is sealed and
must dispatch through that visitor; adding a new semantic operation therefore makes
every exhaustive consumer fail compilation until it implements the new visitor method.
This is where Peeper deliberately uses the visitor pattern: new **semantics** are
introduced to every consumer, while new syntax that reuses existing effects is not.

The effect publisher is a syntax-aware boundary because evaluation order and use
kind are language semantics. Downstream analyses do not repeat that AST walk.

Examples of behavior now derived from effects:

- definite initialization consumes `Define`, `Write`, `Use`, `Borrow`;
- ownership consumes `Define`/`Write` generically rather than special-casing
  `let`, `const`, and assignment statements;
- ownership/liveness use the same published `Use`/`Borrow` sequence;
- sequence-loop borrow lifetime arrives as `Iterate`; ownership no longer asks
  `ForStmt` or `SequenceIteration` what kind of loop it is.

A new syntax construct that reuses existing operations and reference-provenance
shapes normally requires no changes to these downstream analyses. The publisher
must still evaluate every base/index/bound exactly once in semantic order; a place
path describes storage, not all evaluated operands.

A new **semantic operation** is different. It is a real extension point: add the
operation, validate it, and make each consumer explicitly decide what it means.
Unknown effects must not be silently ignored.

## Current responsibility map

| Concern | Current owner | Published evidence / artifact |
| --- | --- | --- |
| Source structure | parser / `frontend/ast` | AST + stable node IDs |
| Declaration catalog | collector | module symbols |
| Type binding | binder | symbol type state / binding result |
| Lexical/import resolution | resolver | `Bindings` symbol/scope identity |
| Type rules and adaptation | typechecker | `typecheckresult.Result` |
| Control topology | `ir/cfg` | typed blocks/sites/edges |
| Variant/optional path facts | flow typechecker | `flowresult.Result` |
| Evaluation/storage actions | `semantics/effect` | ordered `effect.Result` |
| Definite initialization | `semantics/definiteinit` | diagnostics |
| Move/borrow/drop analysis | `semantics/ownership` | `ownershipresult.Result` |
| Lexical usage warnings | `semantics/usage` | diagnostics from symbol usage/mutability state exposed by `IsUsed` / `RequiresMutable` |
| High-level lowering | `ir/hir/lower` | HIR |
| Mid-level lowering | `ir/mir` | MIR |
| Physical layout/codegen | backend | backend IR |

Current stages usually obtain earlier facts through results or queries instead of re-detecting equivalent facts from AST shape. Audit whether that remains simplest boundary before extending it.

## Current syntax-aware responsibilities

These phases currently inspect syntax to establish distinct semantics:

- resolver: names/scopes/import/variant paths;
- typechecker: type rules, conversions, calls, loop/match semantics;
- CFG builder: source control constructs -> topology;
- effect publisher: evaluation order and value/storage action;
- HIR lowering: source construct -> executable high-level IR;
- ownership reference capture: bounded value-shape interpretation plus published
  type/flow evidence, preserving live loans before effects can move source values.

Those switches are not architectural duplication by themselves. The smell is two
phases independently deriving the **same fact**.

Ownership still has return-specific policy because return-origin/pointer-escape
checks straddle returned-value evaluation: provenance must be checked before a
move can erase it, while cleanup runs after evaluation. This is a deliberate
language policy, not a generic child walk. An equivalent future control transfer
would need either shared published evidence or another explicitly justified
implementation of that policy.

### Reference provenance and holder-relative loans

`ownership.referenceValueForExpr` is not a generic aggregate interpreter. It uses
existing holder loans, `Flow.ValueOrigins`, reference types, struct payload
syntax and `Typechecking.VariantConstruction` evidence for currently accepted carriers.
Pre-evaluation capture preserves loan identity before a move clears source state.
Flow origin sets describe referents; they do not replace ownership's dynamic loan
IDs, mutability, reservations/activation, liveness, joins or cleanup policy.

Each `referenceLoan.path` locates a slot relative to its holder, independently of
`origins` (borrowed storage) and `id` (loan identity). Copies clone paths; equality
and joins distinguish the same loan in different slots. Projected writes consume
an exact `Flow.StorageOrigins` destination and captured RHS loans to replace
one direct/optional enum reference field, including clearing it, while retaining
sibling and copied-holder loans. Partial writes keep the carrier live. This is not
full field-sensitive last-use analysis or support for nested stored-reference
aggregates/arrays. Those storage restrictions remain typechecker-owned.

Flow typing must retain recorded assignment-operand types and variant payload
proofs for HIR; retyping an already checked operand can erase that evidence. HIR
consumes published payload/projection facts; backend typed-store invariants remain
strict. Single-case enum selectors and optional-array index assignment have known
separate typing limitations, not resolved by this reference-field repair.

### Lexical usage, not runtime liveness

`semantics/usage.Analyze` emits unused/import/private/local/parameter and unnecessary
`mut` warnings from symbol-owned usage state. Resolver and project type/import lookup call
`MarkUsed`; typechecking calls `RequireMutable`. The backing bits are private to `symbols.Symbol`. Type-only/import references and source
uses outside reachable runtime paths are not equivalent to effect-stream uses.
Ownership liveness remains a separate CFG/effect analysis. Moving usage to reachable
CFG effects would change warning policy and needs explicit design/approval; it is
not an unfinished mechanical migration required by this architecture.

## Current expression and statement extension path

Current implementation is easiest to assess by classifying feature before editing downstream code.

### A. Pure syntax / sugar over existing semantics

Current edit points:

1. AST + parser;
2. AST child declaration (`forEachChild`);
3. translate at the syntax-aware semantic boundary into existing decisions/effects.

For already supported value/provenance shapes, ownership, definite-init, liveness,
graph scheduling, and cleanup should require no new node case.

### B. New typechecking or control semantics, existing value effects

Current edit points:

1. AST + parser;
2. resolver only if name/scope behavior differs;
3. typechecker decision/evidence;
4. CFG only if topology differs;
5. effect publisher maps construct to existing operations;
6. HIR lowering.

Generic mechanics stay unchanged; audit reference capture if the feature introduces
a new accepted reference-bearing value shape.

### C. New semantic action

Only when existing effects cannot represent behavior:

1. add sealed `effect.Op` and its `effect.Visitor` method;
2. add producer + validator;
3. implement the new visitor method in every semantic consumer; compile failures
   provide the checklist;
4. add focused tests proving evaluation order and analysis behavior;
5. add/update Peeper fixtures when language behavior changes.

Under current design, new syntax does not require new effect kind when existing operations represent its behavior.

## Current semantic-type extension path

Under current sealed interface, new type first satisfies `typeinfo.Type`:

1. `Text` for human-facing rendering;
2. `structure` with semantic attributes and correct `TypeChildRelation` for every
   contained type slot;
3. `isSameType`, `isSized`, `isLowerable`, and `ownership` for intrinsic behavior.

Then make explicit decisions only where another owner genuinely differs:

- compatibility and conversions;
- HIR/backend type lowering;
- syntax conversion if source has new type syntax.

Focused typeinfo/IR tests guard the remaining type-kind extension
points rather than generic containment or ownership traversal.

## Current graph-backed analysis path

Current implementations generally reuse `graph.Directed` for topology and `graph.Worklist` for rescheduling, keep semantic edge metadata in domain edge types, and keep state/join/transfer in analysis packages. They use explicit `cfg.Edge` and CFG metadata rather than inferring true/false/case/loop meaning from successor position. These are observed choices to evaluate, not permanent requirements.

## Validation model

Different mistakes are caught at different boundaries:

| Mistake | Guard |
| --- | --- |
| AST child omitted | AST traversal tests + source fixtures |
| semantic type child/ownership method omitted | sealed `typeinfo.Type` compile-time contract |
| incorrect child relation or capability composition | structural tests + capability golden/cycle tests |
| semantic type missing representation decision | sealed type methods + focused representation tests |
| malformed graph topology | CFG/graph validators and tests |
| malformed effect evidence | `effect.Result.Validate` |
| new effect ignored by an exhaustive consumer | `effect.Visitor` compile-time contract |
| malformed cleanup evidence | `ownershipresult.Validate` |
| malformed HIR/MIR | IR validators |
| wrong language behavior | package tests + `x_test` source fixtures |

Effect validation checks node membership and expression categories, not whether
an existing syntax case emitted every required operation. Definition, write-owner,
and iteration-owner IDs remain generic source identities; consumers do not need
a particular declaration syntax. Rejecting dispatch boundaries catch unsupported kinds during execution; producer ordering tests and source fixtures catch missing or reordered operations.
Each non-nil CFG function requires an outer effect and cleanup-plan entry; those
per-function artifacts may be empty when no operations or cleanup are needed.

Compiler correctness does not depend on parsing its own Go source. Closed families use sealed interfaces and required methods where that improves clarity; remaining dispatch sites reject unknown members loudly and are covered by behavior tests.

## Architecture review risks

Potential regressions under current design include:

- a second recursive AST walk for a structural query already served by
  `ast.Inspect`;
- a second semantic-type child enumeration for generic recursive behavior;
- private selector/index peeling when `place.Project`/`Decompose` answers it;
- private graph adjacency stores;
- private queue + queued-set fixed-point schedulers;
- downstream syntax checks for a fact already published by typechecking/CFG/effects;
- default/no-op handling that makes an unknown sealed node/effect silently succeed;
- pass-through compatibility wrappers around canonical APIs.

## Verification commands

Required Go version is declared in [`go.mod`](../go.mod).

```bash
go test -count=1 ./internal/semantics/typeinfo ./internal/project ./internal/ir/hir ./internal/ir/mir
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./internal/graph ./internal/project ./internal/pipeline
go run ./scripts/bundle.go
PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
git diff --check
```

Run commands sequentially: full Go tests and executable fixtures can touch shared
`build/` artifacts. Without `PEEPER_BIN`, fixture tests skip compiler execution;
manifest-only success is not language validation. Race coverage above is focused,
not a claim that every package or target was race-tested.

For language behavior, use `x_test` and the bundled compiler according to
[`RULES.md`](../RULES.md). Useful architecture-audit searches include private
adjacency stores, queue/queued loops, repeated type-child enumeration, and AST
switches appearing in previously syntax-agnostic analyses.
