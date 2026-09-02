# Ownership Vocabulary Design

Status: **proposal — needs maintainer approval on vocabulary before any code**.

This document defines the ownership decision vocabulary that makes lifetime
handling automatic for future features. Design goal, stated by the maintainer:

> A new construct (e.g. a Zig-style `catch` block) should auto-handle its
> payload's lifetime — consumed, copied, dropped — without writing ownership
> code, unless it invents a genuinely new lifetime relationship.

This is the design record for framework workstream 6 and the ownership slice
of `COMPILER_FRAMEWORK_REPORT.md`. Facts below come from inspected code
(paths relative to `compiler/`); baseline findings are recorded for
traceability.

## 1. Current state

### What exists and works

The type level already has a three-axis capability model in
`internal/semantics/typeinfo/capabilities.go`:

| Query | Meaning |
| --- | --- |
| `IsImplicitCopyType` | value copies implicitly on read use |
| `IsNoCopyType` | contains ownership; copy forbidden |
| `NeedsDrop` | scope cleanup must destroy runtime state |

Ownership is the enforcement authority: every consumption flows through
`useKind` (`useRead`/`useCopy`/`useConsume`, `ownership/expr.go:17`) and
cleanup lands in `ownershipresult.CleanupPlan` keyed to exact CFG sites.
MIR consumes the plan; the backend emits drops. For-in is the model citizen:
the typechecker publishes `ForIteration.Carrier` and ownership consumes it
without re-deriving.

### What is broken: decisions are scattered, not published

The typechecker publishes *shape* evidence; **all use-kind decisions are
re-derived in ownership from AST shapes plus hardcoded per-node rules**:

| Decision | Where it actually lives today |
| --- | --- |
| Ordinary call argument consume-vs-copy | re-derived in `ownership/expr.go:344-351` from `fn.Params` + `IsImplicitCopyType`; typechecker already matched params in `checkCall` |
| Binding/assignment/return consumption | hardcoded `useConsume` (`ownership.go:501,530,688`); typechecker publishes nothing |
| String-concat operand roles | shape published (`StringConcatenations`), use kinds re-derived (`ownership/expr.go:111-115`) |
| Match-arm carrier move | re-derived from binding types (`ownership.go:664-671`); typechecker had all inputs |
| `alloc` argument consumption | hardcoded (`ownership/expr.go:266-274`), absent from `CompilerCalls` evidence |
| Variant/array-literal/as-cast consumption | hardcoded per-node-type (`ownership/expr.go:96-122`) |
| Drop obligations | `NeedsDrop` checked at six separate ownership sites |

Capability model gaps (each is a place where "auto" silently breaks):

| # | Gap |
| --- | --- |
| G1 | `IsNoCopyType` is dead in production; move-only is re-derived as `!IsImplicitCopyType` everywhere — but structs with only scalar fields are neither implicit-copy nor no-copy (explicit-copy middle class), so the negation is not equivalent |
| G2 | `NeedsDrop` implemented twice: `typeinfo/capabilities.go:356` and backend `drop_emit.go:303-373` over the IR type table — they agree today, drift tomorrow |
| G3 | Owned interface values: source says no-drop, backend raw-frees through a `TypeOwnedPtr`-to-interface special case — drop policy lives only in the backend |
| G4 | `FuncType` has no ownership character (closure move-only? nothing answers) |
| G5 | `TypeParameterType` treated move-only even when instantiated with a copyable argument — generics over `T` cannot copy |
| G6 | `NoneType` not implicit-copyable — `none` is move-on-use |
| G7 | Enum-payload copyability is non-compositional: same struct copyable as variant payload, move-only standalone (intentional, but must be stated as vocabulary, not accident) |
| G8 | Cycle-guard inconsistency in `IsNoCopyType` (`seen` never deleted on exit) |
| G9 | "Dynamic array owns" encoded three times (implicit-copy, no-copy, needs-drop, plus backend `Length == ""`) |

Cleanup machinery fragilities found along the way:

- **Three drop channels**: planned drops (`CleanupPlan`), embedded flags
  (`DropTarget`/`DropRoot`/`DropBase`), and `temporaryDrops` flushes.
- **Return asymmetry**: returns emit no CFG scope-exit sites
  (`cfg/build.go:133-139`); `cleanupBeforeReturn` re-walks the scope chain,
  duplicating `applyBlockExit` logic.
- **`hir.Return.Cleanup` is vestigial** — never populated by lowering.
- **`MatchCarrierMoves` is published but never consumed.**

## 2. Proposed vocabulary

Two closed sets. Everything else is derived.

### 2.1 Type capability (per `typeinfo.Type`, total — every type answers)

```go
type OwnershipCapability uint8

const (
    CapabilityTrivial OwnershipCapability = iota // copy free, no drop
    CapabilityCopy                               // implicit copy, no drop
    CapabilityExplicitCopy                       // copy via explicit op, no drop
    CapabilityMove                               // move-on-use, needs drop
)

// One canonical walker; every other query derives from it.
func OwnershipCapabilityOf(typ Type) OwnershipCapability
```

Mapping from today's model:

| Type | Capability |
| --- | --- |
| scalars, `RawPtr`, `CStr`, `Allocator` | `Copy` |
| immutable `Ref` | `Copy` (borrow is a use-kind concern, not type concern) |
| mutable `Ref` | `ExplicitCopy`? — **decision D1** |
| `String`, `OwnedPtr`, owner arrays | `Move` |
| `Optional`, `Array`, `Struct`, `Enum` | compose from inner/fields/cases |
| `Interface` | **decision D2** (source-level `Move` + drop, or backend-owned) |
| `FuncType` | **decision D3** (proposed: `Move`, closures own captures) |
| `TypeParameter` | **decision D4** (proposed: conservative `Move` + copy when instantiated copyable — requires instantiation-aware query) |
| `None` | `Copy` (**decision D5**, fixes G6) |

Rules:

- `IsImplicitCopyType`, `IsNoCopyType`, `NeedsDrop` become **derivations** of
  `OwnershipCapabilityOf` (or are deleted — **decision D6** on `IsNoCopyType`,
  which is dead today).
- The backend's `typeNeedsDrop` is **deleted**; MIR carries drop obligations
  published by ownership, and the backend trusts them. One implementation of
  "what needs drop" (fixes G2/G3/G9).
- Cycle guards are uniform (fixes G8).

### 2.2 Use kind (per value use, published once by the typechecker)

The `UseKind` vocabulary lives in `typeinfo` beside `OwnershipCapability`
(the per-use counterpart of the per-type capability); the published map
lives in the typecheck result:

```go
type UseKind uint8

const (
    UseRead  UseKind = iota // borrow-ish observation, value unchanged
    UseCopy                 // value copied; source unchanged
    UseMove                 // value consumed; source dead
)

// Published in typecheckresult.Result:
ValueUses map[ast.NodeID]UseKind
```

Plus two published facts that remove hardcoded per-node rules:

```go
// consumption of binding/assign/return initializers is derivable from
// UseKind on the initializer expression — no separate map needed.
// Match-arm carrier moves:
MatchArmMoves map[ast.NodeID]UseKind // per arm body: UseMove or UseCopy
// String-concat operand roles:
//   left UseMove, right UseRead — publish alongside StringConcatenations
```

What becomes deletable in ownership: `checkCallArgument`'s param re-derivation
(typechecker publishes argument `UseKind` during `checkCall`), hardcoded
`useConsume` at binding/assign/return, concat operand re-derivation,
`matchArmMovesCarrier`, `alloc` special case, and the per-node consumption
switch in `ownership/expr.go:96-122`.

What ownership **keeps**: flow-sensitive enforcement — use-after-move, use
while borrowed, loan conflicts, destroy-while-borrowed. Those are *state
machines over published classifications*, which is ownership's real job.

### 2.3 The rule new features follow

```text
new syntax node
  → children use standard field types (Expr, *BlockStmt, …) → traversal free
  → typechecker classifies each value use with UseKind during normal checking
  → ownership consumes ValueUses + type capabilities → cleanup plan automatic
  → MIR lowers plan; backend trusts it
  → zero ownership code, unless the construct invents a new lifetime relation
```

For `catch err { body }`: payload binding is an ordinary `UseMove` (or
`UseCopy` per capability) into the arm scope; cleanup at scope exit is already
universal. Only the variant construction is catch-specific.

## 3. Validator contract

One canonical validator at the ownership boundary:

```go
// ownershipresult.Validate checks published evidence and plan shape only.
func (r Result) Validate(types *typecheckresult.Result, cfg *cfg.Module) error
```

Invariants:

1. Every ownership-relevant expression (capability ≠ Trivial) has a `UseKind`
   entry — missing entry is an internal error, not silent fallback.
2. Every `UseKind` is legal for the expression type's capability (no `UseCopy`
   of a `Move` type without explicit-copy context).
3. Every drop-needing symbol appears in exactly one cleanup site per path;
   no symbol in both `AfterScope` and `BeforeReturn` for the same path.
4. Every cleanup site key references an existing CFG site / node.
5. Plan maps contain no stale entries after regeneration (existing `delete`
   convention becomes validator-checked).

Invalid source remains diagnostics; validator failure is a compiler bug.

**As implemented (slice 4).** `Validate(types, bindings, graphs)` runs in the
pipeline after every ownership pass, skipped when the module already has errors,
and reports failure as `ICE0002` rather than a source diagnostic. Invariants 2,
4 and 5 are implemented as written; 5 collapses into 4, since a stale entry is
exactly a key that no longer names a program point.

Invariant 1 is implemented over the sites that actually need publication: a use
kind is published where the decision requires type information the typechecker
holds — call arguments, intrinsic operands, match carriers — and the validator
requires an entry for every argument in `EffectiveCallArguments`. The remaining
uses are structural: a binding moves, a condition reads, an index reads. Those
follow from syntactic position alone, so publishing them would add ceremony
without adding knowledge, and the validator does not demand entries for them.

Invariant 3 is **deferred**. Proving that a symbol is dropped exactly once per
path is a CFG dataflow walk — the analysis ownership already performs. Repeating
it inside the validator would make the validator a second implementation of the
thing it checks, which §6 rules out. Double-drop stays covered behaviorally by
the ownership suite and the `x_test` drop fixtures.

`publishedUse`'s capability fallback is retained, not removed. It is reachable
only where the typechecker exited before publishing, which means the program
already has diagnostics; raising a compiler-bug error there would blame the
compiler for source the user was already told is invalid. The validator is the
single place that treats a missing classification as a bug.

## 4. Migration slices

Each slice keeps the full suite green and is independently reviewable.

1. **Capability consolidation** — `OwnershipCapabilityOf` as single walker;
   re-point `IsImplicitCopyType`/`NeedsDrop` at it; resolve D1–D5; delete
   backend `typeNeedsDrop` (MIR already carries obligations); fix G6/G8.
   No behavior change intended; existing suite + fixtures prove it.
2. **Publish use kinds** — typechecker publishes `ValueUses` for call
   arguments, bindings, assignments, returns, concat operands, match arms.
   Ownership consumes; delete the re-derivations listed in §2.2. Largest
   slice; behavior-preserving with focused regressions per decision site.
3. **Cleanup unification** — fold return-cleanup into CFG scope-exit sites
   (removing the asymmetry) or explicitly document the split; delete
   vestigial `hir.Return.Cleanup`; resolve the three-drop-channels question
   (**decision D7**: keep embedded flags as MIR-lowering detail but single
   source in plan).
4. **Validator** — `ownershipresult.Validate` at the phase boundary + tests.

## 5. Decisions (resolved by maintainer)

| # | Decision | Resolution |
| --- | --- | --- |
| D1 | Mutable ref | **`ExplicitCopy`, with the single-active-`&mut` invariant**: conceptually only one active `&mut` may exist. A use of a mutable ref either moves it, or is an error if it would create a second active `&mut`. Reservations (temporary, scope-bound reborrows) remain the only sanctioned exception, enforced by the loan machinery. "Copy a `&mut`" is not a second owner — it is an error. |
| D2 | Owned interface drop | **Source-level `Move` + needs-drop.** Structural rule: owned interface allocates → move. Backend raw-free special case becomes the lowering of a source-published drop obligation, not an independent policy. |
| D3 | Function values / closures | **Structural rule applied to the capture set**: closure capability composes from captured state like a struct — captures only Copy state → Copy; captures owned/borrowed state → Move. No special policy. |
| D4 | Generic `T` | **Instantiation-aware.** The structural rule needs a concrete type; at declaration `T` is conservative `Move`, at instantiation the query checks the bound type and applies the rule. |
| D5 | `NoneType` | **`Copy`** — contains nothing; structural rule says trivially copyable. Fixes the current move-on-use oddity. |
| D6 | `IsNoCopyType` | **Delete.** One capability walker; every query derives from it. |
| D7 | Drop channels | **Single source: `CleanupPlan` is authoritative.** Embedded flags become MIR-lowering details filled from the plan. |

The governing rule stated by the maintainer:

> Ownership capability is baked into the type itself. Scalar → copyable →
> copy. Contains a reference, pointer, or allocation inside → move. Check
> the type, apply the rule. No per-type policy tables.

Consequence: capability is a **pure structural function of the type** —
compositional over fields/cases/inner, instantiation-aware for generics,
with exactly one implementation that the whole compiler consumes.

## 6. Non-goals

- No borrow-checker redesign; loan machinery stays.
- No change to `place` semantics or `Local` provenance rules.
- No new language surface; this is evidence plumbing with behavior preserved.
- Validators never re-derive semantic decisions; they check published shape.
