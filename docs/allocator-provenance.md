# Allocator Provenance Design

## Status

Design and implementation tracker for issue #26. Allocator provenance through
owned interfaces is implemented; FFI closure and bridge retirement remain the
next boundary.

## Goal

Every runtime value that owns allocator-backed storage must carry enough
provenance to release that storage through allocator instance that created it.
Moves, aggregate storage, optional wrapping, function calls, and interface
erasure must preserve provenance without a compiler side table.

Safe code must not be able to select a different allocator while destroying an
owner. Raw allocation and deallocation remain separate FFI operations and never
create or consume safe owners.

## Current Boundary

Current source model already has correct ownership timing:

- `typeinfo.NeedsDrop` classifies values requiring cleanup.
- ownership analysis decides when moves invalidate values and where `Drop`
  actions occur.
- HIR and MIR carry explicit `Drop` operations.
- LLVM recursively destroys payloads, then releases through owner-carried
  allocator descriptors.
- dynamic-array allocation, growth, and release use the originating allocator;
  the default descriptor currently bridges to `malloc` and `free`.
- `*Interface` carries `{data, vtable, allocator}`. Its drop slot destroys the
  erased payload, then its release slot deallocates concrete storage through the
  carried allocator.

`*T` lowers to `{T*, allocator}`, dynamic arrays to `{T*, len, cap, allocator}`,
strings to `{byte*, len, allocator}`, and owned interfaces to
`{data, vtable, allocator}`. Borrowed interfaces retain `{data, vtable}`.

## Decisions

### Provenance is runtime value data

Allocator identity is not part of source type identity. Values allocated by
different allocators remain compatible `*T` values and may meet at assignments,
branches, parameters, returns, optionals, and aggregate fields.

Each allocation-owning runtime carrier stores allocator handle beside owned
address. This avoids:

- provenance type parameters on every function and aggregate
- whole-program allocator inference
- hidden maps keyed by allocation address
- losing provenance across module or interface boundaries

Ownership analysis continues to track liveness only. It must not grow a second
allocator dataflow analysis.

### Allocator handle is opaque and process-lived

Source type `Allocator` is a builtin, implicitly copyable, non-null handle. It
lowers to opaque pointer to immutable allocator descriptor. It has no literal,
field access, raw-pointer cast, or safe constructor.

Initial allocator descriptors and their contexts live for process lifetime.
This deliberately excludes stack-scoped allocator contexts and arena handles
whose lifetime could end before their owners. Supporting scoped allocators later
requires a lifetime/region design and is not hidden inside this issue.

Descriptor ABI owns three operations:

```text
context
allocate(context, size, alignment) -> rawptr
deallocate(context, address, size, alignment)
```

Size and alignment are passed to both operations. This supports allocators that
require layout on deallocation and prevents libc assumptions from entering
source semantics.

Each LLVM module emits a private default descriptor with internal bridge thunks.
The thunks may call current `malloc` and `free`; portable runtime work may
replace them with `peeper_rt_*` calls without changing source types, owner
layouts, HIR, or MIR. Internal linkage prevents duplicate bridge definitions
when imported Peeper modules are linked together.

### Destruction selects allocator from owner

`free(owner)` remains only safe explicit early-destruction operation. It consumes
`*T`, recursively destroys `T`, then deallocates storage through allocator handle
carried by owner.

No `free(allocator, owner)` or `allocator.free(owner)` variant is added. Such an
API duplicates allocator information and creates mismatch state only to reject
it. Safe code instead makes wrong-allocator destruction impossible by
construction.

Low-level `Malloc`/`Free`-style functions continue to operate on `rawptr`. An
owned pointer does not implicitly convert to `rawptr`, so raw deallocation cannot
consume a safe owner. Future raw-to-owner adoption must be explicit, unsafe, and
must attach allocator handle at adoption boundary.

### Allocation uses inferred value type

First source construction operation is:

```peep
let node: *Node = alloc(Node{value: 1})
let arena_node: *Node = alloc(Node{value: 2}, arena)
```

Target declaration surface is:

```peep
fn alloc<T>(
    value: T,
    allocator: Allocator = allocator::Default(),
) -> *T
```

Default parameters are implemented first under
`docs/default-parameters.md`. Generic function execution and monomorphization do
not exist yet, so initial `alloc` remains shadowable compiler operation, like
current dynamic-array owner operations. It exposes same one-or-two-argument
surface, infers `T` from `value`, and uses canonical default-argument expansion
instead of a separate arity special case.

Evaluation follows normal left-to-right call order: value, then explicitly
provided or default allocator. Allocation happens after both operands evaluate.
Null allocation result traps; panic/abort has no unwind cleanup in current
language model. `value` moves into new storage only after allocation succeeds.

Zero-sized targets request one byte while retaining target alignment, ensuring
non-null `*T`. Same normalized size and alignment are used on deallocation.

`alloc` rejects values whose runtime layout is unavailable and values forbidden
at heap-storage boundary, including stored safe references under current rules.

Fallible `try_alloc`, unsafe adoption, custom descriptor construction, and
scoped allocator contexts remain later work.

## Runtime Layouts

`allocator` below means opaque allocator descriptor pointer.

| Source value | Runtime layout |
| --- | --- |
| `Allocator` | `allocator` |
| `*T` | `{T* data, allocator}` |
| `?*T` | same as `*T`; `data == null` means `none` |
| `[]T` | `{T* data, i64 len, i64 cap, allocator}` |
| `string` | `{byte* data, i64 len, allocator}` |
| `*Iface` | `{rawptr data, rawptr vtable, allocator}` |
| `&T`, `&mut T`, slice views, borrowed interfaces | unchanged non-owning layouts |
| `rawptr`, `cstr` | unchanged; no provenance |

`none` for `?*T` zeroes full carrier. Optional presence checks inspect only
`data`. Valid owners always have non-null data and allocator.

Empty dynamic arrays carry chosen allocator even while `data`, length, and
capacity are zero. This lets later `append` or `reserve` allocate through same
instance. Plain `[]T{}` carries default allocator without allocating storage.

Dynamic-array operations preserve handle. Growth allocates replacement through
same handle, moves elements, then releases old buffer through same handle.
`shrink` retains allocation and handle. Empty-result normalization must not erase
handle.

Strings follow dynamic storage rule. `cstr` remains borrowed foreign data and
must never be deallocated by owner cleanup.

## Interface Erasure

Converting `*Concrete` to `*Iface` moves both concrete data address and allocator
handle into interface carrier. It never reallocates payload.

Owned-interface vtables reserve two housekeeping slots before method slots:

1. `drop_value(data)` recursively destroys concrete payload.
2. `release_storage(allocator, data)` deallocates using concrete size and
   alignment known by thunk.

Normal drop and `free(*Iface)` call both slots in order. Consuming interface
method calls method, then only `release_storage`, because payload ownership moved
through method. Borrowed interfaces never call either slot.

This keeps concrete layout out of erased carrier while preserving originating
allocator. Nested owners inside payload retain and use their own handles.

## Compiler Pipeline

### Syntax and semantic types

- Predeclare `Allocator` as builtin scalar-like handle type.
- Add shadowable `alloc` compiler operation with inferred result `*T`.
- Keep `OwnedPtrType{Target}` unchanged; allocator is runtime metadata, not type
  compatibility.
- Keep `free(owner)` AST and semantic ownership behavior unchanged.
- Reject allocator construction/casts outside approved core/runtime entrypoints.
- Reject extern signatures that produce or consume allocation-owning carriers
  until explicit foreign-ownership ABI exists. Imported Peeper functions remain
  valid because they use Peeper carrier ABI.

### HIR

Add explicit allocation expression containing allocator, value, result type, and
source location. Do not lower `alloc` as ordinary call or backend-recognized
symbol.

Keep `Drop` unchanged. Extend dynamic-array and interface construction values to
carry allocator source when required. Default allocator selection becomes
explicit HIR data rather than hidden LLVM choice.

### MIR

Add explicit allocation value with lowered allocator and initializer refs.
Dynamic-array allocation carries allocator ref. `InterfaceMake` preserves
allocator when source is owned.

Keep ownership-planned `Drop` unchanged. MIR text must expose allocator operands
so `.mir` output proves provenance flow before backend lowering.

### Backend

- Centralize allocator carrier LLVM types and descriptor call ABI.
- Extend canonical pointer dereference/place lowering to extract `data` from
  owned pointer carrier.
- Extend one recursive drop path to read carried allocator.
- Extend dynamic allocation/growth path to preserve allocator.
- Extend owned-interface carrier and vtable housekeeping slots.
- Keep borrowed pointer/reference/interface lowering unchanged.
- Keep target `usize`, checked size arithmetic, bounds traps, initializer order,
  reverse destruction, and failure traps.

Runtime symbol validation stays MIR-driven. During bridge stage it validates
default descriptor's required `malloc`/`free` ABI once, not at each allocation
site. When portable runtime replaces bridge, remove old symbol requirements and
call sites directly; do not preserve compatibility wrappers.

## Invariants

1. Every non-null allocation owner has non-null allocator handle.
2. Every move transfers data and allocator together.
3. No implicit copy duplicates allocation owner or provenance.
4. Every deallocation reads allocator from owner being destroyed.
5. Payload destruction completes before storage deallocation.
6. Dynamic-array replacement uses one allocator for new and old buffers.
7. Interface erasure preserves handle without reallocating payload.
8. Borrow creation exposes data address but does not transfer handle.
9. Raw pointers never gain safe provenance implicitly.
10. Ownership CFG decides cleanup timing; runtime carrier decides allocator.

## Diagnostics

Required focused diagnostics:

- `alloc allocator argument must be Allocator`
- `alloc cannot store <type> in owned heap storage`
- `allocator value cannot be constructed directly`
- `extern ownership carrier requires explicit foreign ownership contract`

No ordinary wrong-allocator diagnostic exists because safe deallocation has no
allocator operand. Compiler invariant failure for missing handle is backend
invalid-IR error, not user type error.

## Implementation Sequence

### Step 1: Default parameters

Implement trailing declaration defaults through canonical call-site expansion as
specified in `docs/default-parameters.md`. Preserve full-arity function ABI and
make ownership/HIR consume one shared expanded-argument plan.

Stop for review when direct, imported, extern, and concrete-method calls support
omitted suffix arguments; function values do not; runtime evaluation order and
default suppression pass bundled fixtures.

### Step 2: Carrier and default descriptor

Introduce allocator runtime descriptor, widen `*T` and `?*T`, route pointer
projection and drop through carried handle, and keep default bridge backed by
current allocator symbols. Add focused MIR/backend carrier tests and a positive
source type fixture; source-level allocation begins in Step 3.

Stop for review when MIR/backend tests prove owned pointer moves, optionals,
dereference, automatic drop, explicit `free`, and exactly-once cleanup through
default descriptor, while source fixture proves unchanged type semantics.

### Step 3: Explicit allocation

Add builtin `Allocator`, core default entrypoint, shadowable `alloc`, explicit
HIR/MIR allocation, heap-storage validation, and positive/negative fixtures.

Stop for review when typed owned pointers can be constructed without raw casts
or generic syntax, and shadowing does not reserve runtime symbols.

### Step 4: Dynamic arrays and strings

Widen owners, make default allocator explicit, preserve handle across every
operation, and use carried handle for growth/drop.

Stop for review when empty/non-empty literals, growth, shrink, nested drops, and
allocator failure behavior pass without global allocator selection at operation
sites.

### Step 5: Owned interface erasure

Widen carrier, add release-storage vtable thunk, preserve handle during erasure,
and distinguish normal drop from consuming dispatch cleanup.

Stop for review when direct owner, erased owner, nested owner payload, and
consuming interface calls each destroy and deallocate exactly once.

### Step 6: FFI closure and bridge retirement readiness

Reject all extern signatures containing allocation-owning carriers, document the
rawptr/cstr boundary, centralize bridge validation, and prove a portable runtime
can replace the current malloc/free bridge without source or IR model changes.

Imported Peeper functions remain valid because they use compiler-generated owner
carriers. Bodyless foreign declarations may use rawptr, cstr, references, and
scalars only. Rawptr values are never automatically freed or adopted as owners.

Stop for review when all safe owner-producing paths attach provenance and no
backend path emits unconditional global `free` for an owned carrier.

## Validation Matrix

Each behavior step requires Go tests plus bundled `x_test/` fixtures.

- positive: allocate, move, return, optional some/none, aggregate field, nested
  owner, early free, automatic drop, dynamic growth/shrink, owned interface
- negative: invalid allocator operand, stored reference target, direct allocator
  construction, raw/owner mixing, owner-returning/parameter/nested-owner extern
  declarations
- FFI boundary: raw malloc/free and cstr calls compile; owner-bearing foreign
  declarations fail before LLVM; imported Peeper owner ABI remains valid
- runtime: allocator counters prove allocation/deallocation pair and exactly-once
  release; two allocator descriptors prove each owner routes to origin
- backend: 64-bit and 32-bit layout/object checks; pointer niche, dynamic header,
  vtable slots, size/alignment forwarding
- regression: existing ownership, array, slice, interface, borrow, runtime-symbol,
  and cleanup fixtures
- full: capped Go suite, race suite, vet, bundle, positive bundled runs, intended
  negative diagnostics, formatting, diff, and executable-artifact audit

## Deferred Work

- scoped allocator lifetimes and arenas
- user-defined safe allocator descriptors
- fallible allocation returning optionals/results
- unsafe raw-owner adoption and foreign ownership contracts
- realloc capability negotiation
- allocator identity in source type parameters
- changing panic/abort to unwinding cleanup

These require separate design. None may bypass carried provenance or introduce a
second cleanup path.
