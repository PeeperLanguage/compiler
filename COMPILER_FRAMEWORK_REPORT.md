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

## Shipped architecture changes

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

Result: no separate CFG successor/predecessor store can drift from reverse edges.

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

### Contract cleanup

Contracts that only verified duplicated downstream AST switches were removed.
Remaining contracts guard real closed extension points such as semantic type
identity/lowering/fingerprinting. Artifact validators remain primary guards for
cross-phase evidence.

### Go baseline

Repository now targets Go 1.23.2. Post-1.23 convenience APIs used by tests/runtime
were replaced with equivalent 1.23 code; no compiler semantics required Go 1.26.

## Extension result

For a new syntax construct expressed using existing semantic actions, expected
work is concentrated in syntax-aware owners:

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

plus source fixtures and repository-specific build validation. Passing tests alone
is not enough; architecture audit must also confirm canonical kernels have not
been bypassed.
