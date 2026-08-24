# Peeper Ownership And Pointer Model

This document is the current design reference for ownership, pointers, copy rules,
and optionals.

Target model:

- `T` owns values.
- `*T` is a non-null unique heap handle.
- `rawptr` is an opaque nullable pointer for FFI and unsafe interop.
- `?T` is optional for non-raw values.
- `?*T` is optional heap-handle storage.
- scalar/raw values copy implicitly; composites move implicitly.
- duplication beyond implicit scalar/raw copy is ordinary user-defined method behavior.
- Shallow copy of `*T` is never implicit.
- `@expr` creates raw pointers; `&expr` creates safe references.
- `[]T` is the target spelling for dynamic arrays.
- `[..]T` is the non-owning slice target; `&[..]T` and `&mut [..]T` are views.
- `&[]T` and `&mut []T` borrow dynamic-array owner headers for structural operations.

Rejected old model:

- `^T` as raw pointer
- `^const T`
- type-level copy/nocopy annotations
- allocator provenance hidden behind plain value `T`

## `T`

`T` is the user-facing owned value type.

Storage location is not encoded in type spelling. A `T` value may be stack-backed,
heap-backed, or backed by runtime-managed storage. User code should not need a
different surface type for that detail.

Examples:

```peep
let n: i32 = 10
let s: str = "fuad"
let xs: []i32 = make_array()
```

Ownership belongs to the binding. Plain composites move on assignment, argument
passing, aggregate insertion, and return. The compiler provides no generic copy
operation; methods may construct independent results using normal ownership rules.

## Heap Handles

### `*T`

`*T` is a unique handle to heap storage containing `T`.

- non-null by default
- moves on transfer and drops automatically when still live
- cannot be implicitly copied
- cannot be forged from raw pointer bits with `as`
- may be nullable only when written as `?*T`

Examples:

```peep
let node: *Node = allocator.alloc<Node>()
let next: ?*Node = none
let moved: *Node = node
```

Raw memory returned from C is not automatically owned. Ownership adoption must
go through an explicit allocator/provenance API.

## Raw Pointers

### `rawptr`

`rawptr` is an opaque nullable raw address.

- points to storage owned somewhere else
- does not own or free that storage
- may be null
- may dangle
- may be copied because raw pointers carry no tracked ownership
- has no pointee type, field access, method lookup, or safe dereference

Examples:

```peep
#[extern("read")]
fn read(fd: i32, buf: rawptr, n: usize) -> isize;

let raw: rawptr = malloc(16)
if raw == none {
    return
}
```

## Optionals

`?T` is either a `T` value or `none`.

Examples:

```peep
let x: ?i32 = none
let next: ?*Node = none
```

Implementation status:

- `none` lowers in expected optional contexts.
- `T` can lower to `?T` as `some(T)`.
- every optional uses tagged `{present, value}` layout, including `?*T`.
- `rawptr` is nullable by default, so `?rawptr` is not part of target model.

CFG flow typing narrows stable optional variables, fields, nested projections,
constant-folded indexes, and direct integral binding indexes after semantic
`none` tests. Joins intersect presence facts, loops run to a fixed point, and
carrier, alias, index-dependency, call, global, and scope invalidation remove
facts when their proof may no longer hold.

An optional copies only when its payload copies. Reading a proven copyable or
shared-reference payload preserves the carrier. Moving a move-only payload from
a direct named optional consumes the whole carrier; moving one from a field,
index, pointee, or other partial place is rejected. Presence checks never
consume. Pointer niche layout remains future issue #30 work.

## Strings

`str` is an owned immutable text value. Its binding owns the string value and
follows normal composite move and destruction rules. String contents cannot be
changed through indexing or mutation operations.

The binding may still be reassigned when declared mutable; reassignment replaces
the owned string value. A string literal may use permanent program storage and
therefore needs no runtime deallocation, while runtime-created strings carry
their allocation provenance and are cleaned up normally. A decoded Unicode
scalar is `char`, a 4-byte value distinct from UTF-8 bytes.

`&str` is a borrowed string view. It may refer to an owned `str` value or to
permanent literal storage, but never owns or frees its backing bytes.

## Copy And Move

Integer/float scalars, bool, byte, char, raw pointers, and cstr copy implicitly.
Shared references duplicate their borrow header. Optionals follow their payload:
`?T` copies when `T` copies and moves otherwise. Every remaining value moves on
a by-value use.

```peep
struct Buffer {
    ptr: rawptr,
    len: int,
}
```

```peep
let a: *Buffer = make_buffer()
let b: *Buffer = a
use(a) // error: a moved into b
```

After a move, the source is dead until reassigned.

A user-defined method may expose duplication:

```peep
let point_copy = point.copy()
```

This is an ordinary method call. Its implementation may copy scalar fields,
allocate new storage, or reject duplication by not defining the method.

## Function Passing

Passing `T` means value passing: Category A copies; Category B moves. Use `&T`
or `&mut T` when the callee must borrow instead.

Passing `*T` transfers the heap handle implicitly.

Passing `rawptr` copies opaque address. No ownership transfer is implied.

```peep
fn destroy(buf: *Buffer) {
    allocator.free(buf)
}
```

## Allocation

Allocator APIs return heap handles.

```peep
let x: *Buffer = allocator.alloc<Buffer>()
```

Free consumes allocator-owned `*T`:

```peep
free(x) // explicit early destruction; scope drop is suppressed
```

Calling `free` with `rawptr` or a non-owned value is a compile error.

Implementation status:

- allocator provenance tracking is not complete
- typed allocation construction and allocator-instance provenance remain future work

## Raw Pointer Escape Analysis

Raw pointers are non-owning. Compiler escape analysis rejects provable attempts
to return raw pointers to local stack storage.

Use `@expr` to produce a raw pointer to addressable storage:

```peep
fn bad() -> rawptr {
    let x: i32 = 1
    return @x // error: pointer to local storage escapes
}
```

Owned heap allocation is the explicit way to make returned storage outlive the
current function.

Target checks:

- reject return of pointer to dead local
- reject storing pointer to shorter-lived local into longer-lived object when provable
- reject obvious use after free once allocator provenance exists
- reject obvious double free once allocator provenance exists

When provenance is lost through casts, pointer arithmetic, FFI, or opaque calls,
the compiler treats the pointer as opaque for this analysis.

Implementation status:

- move/no-copy checks for owned `T` exist
- explicit `@` address expressions exist
- returning pointers to known local storage is rejected
- allocator provenance tracking is future work

## References And Slice Views

Safe reference syntax is separate from raw pointer syntax:

```peep
let r: &T = &value
let m: &mut T = &mut value
let p: rawptr = @value
```

References are temporary views. They cannot be stored inside structs, arrays,
dynamic arrays, globals, or heap objects in the first implementation slice.

Reference returns use explicit source contracts rather than lifetime type
parameters:

```peep
fn first(xs: &[]i32) -> &i32 from xs
fn choose(left: &i32, right: &i32, pick_left: bool) -> &i32 from(left, right)
```

The ownership phase validates returned origins and substitutes caller origins
at calls. `from` metadata is erased before HIR. User aggregates containing
external references remain future work; safe self-referential aggregates are
forbidden because moving inline storage would invalidate their internal views.

Arrays and dynamic arrays own storage:

```peep
let fixed: [4]i32 = [4]i32{1, 2, 3, 4}
let dynamic: []i32 = []i32{1, 2, 3}
```

Slice views borrow contiguous storage:

```peep
fn sum(xs: &[..]i32) -> i32
fn fill(xs: &mut [..]i32, value: i32)
```

Implementation status:

- `&T`, `&mut T`, `&expr`, and `&mut expr` are implemented for current v1 storage boundaries.
- `[]T` is represented as dynamic-array storage, not a slice view.
- `[..]T` is a distinct reference-only slice target.

## Automatic Destruction

Every live owned value is destroyed on normal scope exit. Moves and explicit
`free` invalidate source and suppress later drop. Assignment into a live owned
destination drops old value before storing replacement.

Drop planning belongs to ownership phase. It produces explicit cleanup actions
for lowering; HIR and backend do not re-infer liveness. Control-flow joins and
loop backedges require identical ownership state, so compiler never needs a
runtime drop flag.

Plans cover named values and unnamed full-expression temporaries. When a scalar
is projected from an ownership-bearing temporary, lowering preserves the scalar
result and then executes the ownership-planned drop for the projection base.

Drop glue recurses through owning pointers, structs, arrays, strings, dynamic
arrays, optionals, and erased payloads. Locals, fields, and elements drop in
reverse declaration/index order. Panic aborts without unwind cleanup. Owned
globals reject until module shutdown ordering exists.

Safe code cannot suppress destruction with a `forget` or `leak` operation.
Normal scope exit, return, overwrite, discarded ownership-bearing expressions,
and explicit early cleanup all follow the same ownership plan. Process abort is
outside structured cleanup; future foreign ownership handoff must be explicit
and unsafe.

Safe rvalue borrows use one fixed temporary rule: storage lives through the
enclosing full expression and is then destroyed in reverse creation order. A
loan derived from that storage cannot escape the expression. This avoids
syntax-dependent temporary lifetime extension and hidden heap promotion.

## Two-Phase Call Borrows

Every mutable reference passed to a call first reserves its canonical storage
origin. Method receivers reserve before explicit arguments; remaining arguments
then evaluate left-to-right. Reservation permits reads and shared borrows, but
blocks mutation, ownership transfer, destruction, and nested mutable activation.

The mutable loan activates immediately before entering the callee. Activation
requires all overlapping shared loans created during argument evaluation to have
ended. A shared reference passed into the same call has not ended and conflicts.
Unequal concrete fields and fixed indexes may reserve independently; dynamic
indexes and slice views remain conservative.

Reservation facts exist only while checking one expression. Persistent reference
liveness remains on the existing ownership CFG, and HIR/MIR/backend call lowering
does not carry reservation state.

## Interface Ownership

Bare interface values are unsized and illegal at runtime. Interface dispatch
uses explicit carriers:

```peep
let view: &Reader = &counter
let owner: *Reader = concrete_owner
```

`&Reader` and `&mut Reader` borrow existing concrete storage and never allocate.
`*Reader` accepts only existing `*Concrete`, adopts that allocation without
moving payload storage, and never allocates replacement storage.

Interface methods declare receiver explicitly:

```peep
iface Reader {
    fn (&Self) read() -> i32
}
```

Borrowed carriers use `{ data: rawptr, dispatch: rawptr }`. Owned `*Reader`
uses `{ data: rawptr, dispatch: rawptr, allocator: rawptr }`; allocator field
preserves allocation provenance. Slot zero is payload-destruction thunk for
both tables; owned tables add storage-release thunk at slot one before method
slots. Borrowed carriers invoke neither housekeeping slot. Dropping `*Reader`
destroys erased payload, then releases allocation through carrier allocator.
`&Reader` calls shared receivers, `&mut Reader` calls shared or mutable
receivers, and `*Reader` may also call consuming receivers.

Concrete methods are receiver functions declared in concrete type's module:

```peep
fn (reader: &Counter) read() -> i32 {
    return reader.value
}
```

`impl` declarations, interface receivers, non-receiver `Self`, generic interface
methods, and bare interface storage are not part of target model.

## Linked Structures

Unsafe linked structures can use raw pointers directly.

```peep
struct Node {
    val: i32,
    next: rawptr,
}
```

This is valid because `rawptr` has fixed size and `next` is non-owning.

This does not make the list ownership-safe. Whoever owns nodes must keep them
alive longer than all raw pointers that reference them.

Owned recursive structures use `?*Node` and drop recursively:

```peep
struct Node {
    val: i32,
    next: ?*Node,
}
```

Direct by-value recursion such as `next: Node` is unsized and rejected. Runtime
lowering reserves one identity for each named recursive composite before
lowering its children. LLVM emits identified aggregate declarations and one
reusable private drop function per owning named composite, so recursive
destruction calls the existing function instead of recursively expanding IR.

## Final Rules

- `T` owns.
- `*T` is non-null unique heap handle.
- `rawptr` is nullable raw pointer only.
- `@expr` produces a non-owning raw pointer to addressable storage.
- `?T` is optional for non-raw values.
- `?*T` is nullable heap-handle storage.
- an optional copies only when its payload copies; otherwise it moves.
- move-only payload extraction consumes a direct named carrier and is rejected from partial places.
- `str` is an owned immutable text value; `&str` is its borrowed view.
- allocator returns `*T`.
- `free` consumes allocator-created `*T`.
- live owned values drop automatically on normal scope exit.
- safe code cannot forget or leak an owned value.
- reference returns declare parameter or receiver origins with `from`.
- borrowed rvalue temporaries live through one full expression only.
- every non-optional composite moves implicitly on by-value use.
- `*T`, `*Interface`, dynamic arrays/strings, and mutable references never duplicate implicitly.
- `rawptr` copy is shallow address-bit copy because it owns nothing.
- bare interfaces are unsized contracts; runtime values require `&`, `&mut`, or `*`.
- compiler rejects returned raw pointers when they are known to point at local storage.
