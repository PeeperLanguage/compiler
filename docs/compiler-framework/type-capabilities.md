# Type capabilities — what got consolidated, and what deliberately did not

Step 4 of the maintainability plan asks that a type receive **one** canonical
answer per shared semantic question, and that consumers stop walking type
structure themselves. It also draws a line: *centralize only semantic questions
shared by multiple callers, and do not create a giant capability object for
unrelated properties.*

This document is the inventory that line was drawn against. It exists so the
decisions below are not re-litigated by the next person who notices that two
functions in `typeinfo` look similar.

## The one consolidation that happened

Copy class and drop obligation were three separate recursive walkers:

| Removed | What it answered |
| --- | --- |
| `IsImplicitCopyType` | does this duplicate on use without an explicit operation |
| `noCopyType` | does this refuse duplication entirely |
| `NeedsDrop` | does this carry a destructor obligation |

All three walked the same structure with the same cycle guard, and every
consumer asked at least two of them about the same type. They are now one
traversal, `ownershipCapability` in `capability_walk.go`, returning
`OwnershipCapability{Copy, Drop}`. `OwnershipCapabilityOf` is the only public
spelling; there are no `IsX` wrappers left behind for old callers.

The quantifiers differ inside that one walk and could not be collapsed further:
implicit copy is *for all* members, while no-copy and drop are *there exists*.
A `enumPayload` flag carries the one context the answer depends on — a struct
copies implicitly as an enum payload but never as top-level bulk storage.

Equivalence with the three predicates was proven by a differential test over 283
constructed types, then frozen as the `capabilityGolden` table so the answers
cannot drift silently.

**Consumers:** 20 non-test call sites, in `typechecker`, `ownership`, and
`hir/lower`.

## Already consolidated before this step

`ContainsReference`, `ContainsStoredReference`, `ContainsAbstractSelf`,
`ContainsTypeParameter`, `ContainsInvalid` and `ContainsNamedEnum` are six
five-line wrappers over one shared `containsType` walker in `relations.go`, each
supplying a traversal mode and a predicate. That is the target shape, reached
already. Nothing to do.

## Deliberately not consolidated

### Sized and Lowerable

These two are the tempting merge: both recursive, both over the same structure,
both with a cycle guard. They must stay apart, and the reason is not style.

| | `IsSizedType` | `IsLowerableType` |
| --- | --- | --- |
| Guard key | `*DefinedType` | underlying `Type` |
| Answer on a cycle | `false` — a type containing itself inline has no size | depends on how the cycle was reached; through a pointer it is representable |
| Context parameter | none | `throughIndirection` |
| Interface | not sized | lowerable |
| Type parameter | sized | not lowerable |

A linked list is lowerable and not sized. One traversal cannot hold both answers
without carrying two guards and two cycle rules, at which point it is two
walkers sharing a function body. Copy and drop merged because they genuinely
share a walk and a cycle rule; these do not.

This is recorded as a comment above `IsSizedType` so the argument travels with
the code.

**Consumers:** `IsSizedType` 1, `IsLowerableType` 6.

### The shallow predicates

`IsIntegral`, `IsArithmetic`, `IsOrderable`, `IsEquatable` and `IsCondition` are
4–10 lines each, non-recursive, one switch over primitive kinds. They answer
unrelated questions — operator admissibility, not ownership — and merging them
is precisely the "giant capability object for unrelated properties" the step
forbids. They stay as they are.

**Consumers:** 13, 4, 1, 1, 3 respectively.

### Reference target, collection shape, target width

- `ReferenceTarget` is already a single accessor with 37 consumers. No duplicate
  exists to remove.
- Collection shape is not a derived question at all: `ArrayShape` is a field on
  `ArrayType`, decided when the type is built.
- Target-width representability is `LiteralFitsType`, one function, 4 consumers,
  no second implementation.

## Remaining structural traversal in the backend, and why it stays

`internal/backend/llvm/drop_emit.go` has `typeHasRuntimeProperty`, reached
through `typeNeedsDrop`, `typeCarriesAllocatorID` and `typeNeedsRawFreeID`. It
recurses over types and asks about drops, which reads at a glance like a second
implementation of the source-level obligation. It is not, and the difference is
structural rather than a matter of trust:

- It reads `ir.TypeTable`, the **lowered** type universe, not `typeinfo.Type`.
  It can see representation choices no source type mentions, and it cannot see
  source policy such as the explicit-copy class.
- It never decides *whether* a value is dropped. That decision arrives already
  made, as a `mir.Drop` instruction. `emitDrop` is entered from exactly one
  place — `emitter.go:311`, handling a `mir.Drop` — and every other call to
  `emitDropValue` in the file is this traversal recursing into a drop that was
  already ordered upstream.
- Its one non-drop caller decides an ABI shim: a declared-only function whose
  signature carries owned storage needs one. Also physical.

So ownership decides policy and the backend expands representation. A type can
be reachable in the backend walk without carrying a source-level drop
obligation, and neither side is wrong. The boundary is now stated in a comment
above `typeNeedsDrop`, including what to do if you ever find yourself wanting it
to answer the policy question: you are in the wrong phase.

This closes gap **G2** in `ownership-vocabulary.md`, which recorded the two as
"implemented twice … they agree today, drift tomorrow". They are not two
implementations of one question; they are one policy decision and one structural
expansion, and only the policy side can originate a drop.

## Behavior changes

None. The consolidation was proven equivalent against the predicates it
replaced before they were deleted, and no consumer's answer changed.
