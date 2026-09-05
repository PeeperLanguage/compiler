# Compiler Engineering Guidelines

This document defines durable principles for implementing Peeper compiler code.
It does not define language syntax, language semantics, package layout, or phase
order.

Repository authority is split deliberately:

- [RULES.md](RULES.md) defines mandatory code quality, architecture, testing,
  branch, and commit rules.
- [AGENTS.md](AGENTS.md) defines agent workflow and review gates.
- [docs/language-spec.md](docs/language-spec.md) and focused design documents
  define language behavior.
- Current source, verified dependencies, and approved design plans determine
  pipeline order.

When this document conflicts with verified compiler correctness or an approved
design, do not follow it silently. Report conflict and evidence so maintainer can
decide whether guideline or design must change.

Current ownership, pointer, copy, and optional design lives in
[docs/ownership-pointer-model.md](docs/ownership-pointer-model.md). The implemented
compiler architecture, canonical structural mechanisms, semantic evidence boundaries,
and contributor extension rules live in
[docs/compiler-architecture.md](docs/compiler-architecture.md). Migration history and
supporting analysis live under
[docs/compiler-framework/](docs/compiler-framework/README.md).

## 1. Priorities

Use this order when trade-offs are real:

1. correctness
2. clear semantics and invariants
3. maintainability
4. diagnostic quality
5. performance

Do not copy architecture from another compiler without a Peeper-specific need.
Small scope is acceptable. Fake boundaries, placeholder artifacts, and designs
known to require replacement are not.

## 2. Establish Ownership Before Implementation

Every compiler responsibility needs one canonical owner. Before changing code,
identify:

- input artifact;
- output artifact or observable effect;
- invariants established;
- diagnostics emitted;
- consumers;
- invalidation and lifetime rules.

A package, phase, result, or helper is justified by owned behavior, not by a
possible future use. Do not create generic `common`, `results`, `flow`, or
`analysis` containers without multiple concrete consumers and a stable shared
contract.

Package names should describe current responsibility. Exact directory layout may
change as ownership becomes clearer; this document does not freeze it.

## 3. Preserve Representation Boundaries

Each representation should contain facts appropriate to its layer:

- lexer produces tokens;
- parser and AST preserve source syntax and locations;
- semantic analysis resolves names, types, and language rules;
- control-flow representation preserves topology and semantic edge meaning;
- lowering preserves established facts while changing representation;
- backend layout and code generation own physical representation.

Do not put semantic conclusions into syntax nodes for convenience. Prefer
explicit semantic artifacts or side tables keyed by stable identity.

Later stages must consume evidence already established by an earlier owner.
They must not rescan source to rediscover method selection, symbol identity,
type decisions, ownership facts, or other resolved semantics.

Semantic identity and physical layout are separate. Source-visible field order,
symbol identity, and diagnostic mapping remain stable even when backend layout
uses different slots or offsets. Layout must provide explicit mapping rather
than mutating semantic order.

## 4. Derive Phase Order From Dependencies

Do not treat a phase list in documentation as proof of correct order. For each
analysis or transform, determine required inputs and guarantees from inspected
code and tests.

Useful dependency principles:

- name-dependent work requires resolved identities;
- type-dependent work requires necessary semantic types;
- path-sensitive work requires control-flow topology that preserves relevant
  source paths and edge meaning;
- lowering consumes semantic evidence instead of recreating it;
- optimization runs only after every mandatory analysis that needs preserved
  source structure.

An optimization must not remove source-reachable structure solely because a
condition is constant before mandatory semantic checks finish. Typed expression
folding and control-flow simplification are distinct operations with distinct
scheduling requirements.

If two analyses depend on each other, make cooperation explicit through a
well-defined query, staged artifact, or fixed-point contract. Do not invent an
arbitrary total order to hide dependency.

## 5. Make Control Flow Explicit

Control-flow-sensitive rules belong on a representation that understands
reachability and predecessors. Examples include return completeness, definite
initialization, ownership state, and tagged-variant narrowing.

Control-flow edges must carry semantic kinds when analyses depend on branch
meaning. Consumers must not infer true/false, return, unwind, or cleanup meaning
from successor order.

Terminating paths must not contribute facts to continuation joins. Unreachable
paths must not corrupt facts for reachable paths.

CFG topology should remain a control-flow artifact. Analysis outputs such as
cleanup plans or narrowing facts belong to their analyses unless they are part
of graph topology itself.

Tagged-variant narrowing produces `Module.Flow` after CFG construction and
before definite initialization and ownership. Downstream phases query effective
per-use types and consume recorded case-test, payload, field, match, and origin
evidence. They must not re-detect `none`, `is`, variant constructors, or match
patterns from AST shape or backend text. Optional syntax and named enums share
case-set flow without losing distinct source semantics.

## 6. Centralize Structural Traversal

Do not duplicate exhaustive AST, HIR, MIR, expression, place, type, or member
walks for analyses that only need traversal.

Node-owning packages should expose canonical traversal so adding a node has one
structural update point. Prefer designs that make missing child coverage fail
compilation or a focused completeness test. Unknown sealed node kinds should
fail clearly rather than be skipped silently.

Semantic switches remain appropriate when each node kind requires distinct
behavior. Centralize structural recursion, not semantic decisions.

Current canonical mechanisms are `ast.Inspect` for AST recursion,
`typeinfo.ForEachChild` for semantic-type structure, `place.Project`/`Decompose`
for storage projections, `graph.Directed` for topology, and `graph.Worklist` for
fixed-point scheduling. Analyses should extend these owners instead of creating a
parallel walk, adjacency store, or queue/queued-set implementation.

Before adding a walker or lookup:

1. search all existing implementations;
2. identify canonical owner;
3. reuse it directly when contract matches;
4. extend canonical implementation when behavior is shared;
5. keep local logic only when semantics genuinely differ.

Do not introduce a generic dataflow framework from similar-looking worklists
alone. Extract one only after several analyses demonstrate stable shared solver
mechanics without hiding their state, join, direction, edge, or diagnostic rules.

## 7. Preserve Behavior During Refactors

Before replacing or moving code, audit everything old path owns:

- diagnostics and spans;
- validation and invariant checks;
- mutation and cached state;
- normalization and fallback behavior;
- target or backend differences;
- incremental invalidation;
- debug or dump output relied on by tests.

Moving code is incomplete until old owner and stale access path are removed.
Do not preserve obsolete names through pass-through wrappers or aliases. Follow
replacement and helper rules in `RULES.md`.

## 8. Define Incremental Artifacts Honestly

Every reusable artifact needs explicit production, consumption, invalidation,
and reset rules. A scheduler state is not automatically a safe cache boundary.

Support only checkpoints compiler can reconstruct correctly. If one object mixes
facts from multiple stages, either split ownership or document which checkpoints
can safely retain it.

Fingerprint equality must imply equality of interface visible to dependent
compilation. Syntax fingerprints may support early conservative invalidation;
semantic fingerprints must use canonical semantic identity for exported API.
LSP is one consumer of incremental results, not architectural owner of compiler
invalidation.

Cache behavior must remain understandable through focused tests. Avoid caches
whose keys, retained facts, or invalidation propagation cannot be stated plainly.

## 9. Diagnostics And Failures

User errors produce diagnostics with concrete source spans and stable codes where
exposed. Prefer root-cause diagnostics over cascades.

Broken compiler invariants must fail clearly. Do not turn internal corruption or
an unhandled sealed node kind into a misleading user diagnostic or silent skip.

When moving a check between stages, preserve diagnostic text, source identity,
ordering where observable, and deduplication unless change is intentional and
tested.

## 10. Verification

Use `RULES.md` for mandatory commands and fixture requirements. Compiler changes
also need evidence proportional to boundary crossed:

- bug fixes begin with focused regression reproducing old failure;
- language behavior uses positive and applicable negative `x_test/` fixtures;
- traversal changes test completeness and newly reachable child nodes;
- flow changes test branches, joins, loops, termination, and unreachable paths;
- incremental changes test unchanged reuse and dependent invalidation;
- lowering changes prove accepted semantics remain lowerable through affected
  backends;
- representation changes verify dumps, diagnostics, and source identity where
  relevant.

Passing unit tests do not prove phase ownership is correct. Review actual inputs,
outputs, consumers, and invalidation paths after change.

## 11. Decision Checklist

Before implementation:

1. What compiler concept changes?
2. Which existing owner already implements part of it?
3. What evidence does change require from earlier stages?
4. Which later stages consume its result?
5. Does change repeat traversal, lookup, formatting, or semantic discovery?
6. Can unsupported cases fail early and clearly?
7. What cache, diagnostic, source-map, or backend behavior can regress?
8. Which focused test proves boundary and failure mode?

If guideline suggests one design but inspected evidence supports another, record
conflict and present options. Maintainer decides policy; implementation must not
silently encode disputed assumption.
