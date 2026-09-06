# Compiler Framework Final Report

## Problem

Peeper previously repeated the same structural and semantic work in several
phases. A feature could compile while one resolver/type/flow/ownership/cleanup
path was forgotten, then fail much later. Fixing one missed path often created a
new mismatch because phases independently walked the same syntax or type shape.

`ast.Inspect` already demonstrated the preferred solution: one package owns
structure; many consumers reuse it.

## Final decision

Framework is **not** a compiler-wide visitor pattern and **not** an effect stream
used for everything. Final design combines:

- canonical structural kernels;
- phase-owned semantic evidence;
- generic graph/worklist mechanics;
- explicit exhaustive decisions only at genuine semantic extension points.

Detailed contract: [`docs/compiler-architecture.md`](docs/compiler-architecture.md).

## Implemented architecture

This describes current source, including local correctness follow-up; it does not
assert that uncommitted work is released or that roadmap milestones are complete.

### Semantic type structure

`typeinfo.Type` is sealed by canonical child and ownership-composition methods.
`typeinfo.ForEachChild` exposes immediate semantic children with relation metadata.
Recursive containment and copy/drop capability queries reuse this structure.

Result: adding a container around an existing subtype declares its relation once;
nested reference/ownership/drop behavior is derived instead of added to several
walks.

### Shared graph topology

`graph.Directed` owns ordered outgoing/incoming indexes. Existing graph users and
CFG use this kernel while CFG retains typed branch/case semantics.

Result: adjacency/reverse-adjacency mechanics have one owner. CFG terminators and
ordered sites remain canonical topology; block/site indexes are derived, frozen by
consumer convention, and rebuilt together for topology changes. Validators inspect
all stored edges, including foreign components, and exact site-edge metadata.

### Shared fixed-point scheduling

`graph.Worklist` owns queueing, pending deduplication, and rescheduling. Flow,
definite-init, ownership, and liveness keep their own lattices and transfers.

Result: repeated scheduler mechanics are centralized without creating a generic
pass framework that hides semantics.

### Canonical places

`place.Project` and `place.Decompose` own selector/index place grammar. Effect and
ownership code no longer maintain private selector/index peeling logic.

### Semantic effects as downstream boundary

Effects now carry enough identity for generic consumers:

- definitions carry initializer identity;
- writes carry source owner/value identity;
- borrows carry exact borrowed operand identity;
- sequence loops publish `Iterate` with loop/place/carrier identity.

Ownership consumes `Define` and `Write` directly instead of separate `let`,
`const`, and assignment statement handlers. `effect.Visitor` is the compile-time
consumer contract: introducing a genuinely new semantic operation breaks every
exhaustive consumer until it explicitly implements that operation. Sequence-loop borrow lifetime is
published as an effect instead of rediscovered from `ForStmt` and typechecker plan
shape. Definite initialization and ownership/liveness consume the same ordered
effect stream.

### Bounded ownership provenance and lexical usage

Ownership still captures accepted reference-bearing value shapes before effects
can move them, using existing loans, flow origins, reference types and semantic
variant constructions. Holder-relative loan paths distinguish stored slots from
borrowed origins and loan IDs. Exact projected direct/optional enum reference-field
replacement preserves siblings and copied holders; carrier-level liveness remains
conservative. This is not a general aggregate provenance framework, nor support for
nested stored-reference aggregates. New reference-bearing shapes require an audit.

Usage warnings remain lexical: `semantics/usage` consumes symbol usage/mutability
flags from resolution, type/import lookup and typechecking, not reachable runtime
effects. Migrating them would require a separate warning-policy decision.

### Contract cleanup

Contracts that only verified duplicated downstream AST switches were removed.
Remaining contracts guard real closed extension points such as semantic type
identity/lowering/fingerprinting. Artifact validators remain primary guards for
cross-phase evidence shape/identity. They do not prove that a handled syntax case
published every required operation: producer ordering tests and executable source
fixtures cover semantic publication. Sealed type methods enforce presence, not
correct child enumeration. Empty effect artifacts can be valid.

Leaf type traversal methods return directly; exported fingerprints use their
existing `Type` parameter without a redundant assertion. Bounded pointer reflection
in `isNilType` remains to preserve typed-nil capability answers. No nil-only
interface or exhaustive replacement dispatcher is introduced.

### Go baseline

Repository now targets Go 1.23.2. Post-1.23 convenience APIs used by tests/runtime
were replaced with equivalent 1.23 code; no compiler semantics required Go 1.26.

## Extension result

For a new syntax construct expressed using existing semantic actions, expected
work is concentrated in syntax-aware owners, provided existing provenance shapes
also suffice:

```text
AST/parser -> resolver/typechecker as needed -> CFG/effects as needed -> HIR
                                      |
                                      v
                     existing definite-init/ownership/liveness/cleanup
```

For a new composite semantic type:

```text
declare type -> child relations + ownership shape
                    |
                    v
       generic containment/copy/drop propagation
```

Representation-specific decisions such as equality, ABI lowering, exported
fingerprint, or genuinely special sizing remain explicit by design.

## Verification standard

Final handoff requires:

```text
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -race <concurrency-sensitive compiler packages>
```

plus a fresh `go run ./scripts/bundle.go` followed by full `x_test` with explicit
absolute `PEEPER_BIN`. Run full tests, bundle and executable fixtures sequentially:
they share build artifacts. Without `PEEPER_BIN`, fixture execution is skipped.
See architecture verification commands for focused graph/project/pipeline races.

Passing tests alone
is not enough; architecture audit must also confirm canonical kernels have not
been bypassed.
