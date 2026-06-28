# Peeper Ownership And Pointer Model

This document is the current design reference for ownership, pointers, copy rules,
and optionals.

Current model:

- `T` owns values.
- `^T` is a mutable raw pointer.
- `^const T` is a read-only raw pointer.
- `^T` and `^const T` do not own.
- `?T` is optional.
- `move` transfers ownership of `T`.
- `#[no_copy]` makes `T` move-only.
- `#[allow_copy]` explicitly permits shallow copy of a `^T`-containing type.

Rejected model:

- no `&T`
- no `&mut T`
- no separate owned pointer type
- no `Box`-style core ownership wrapper
- no `^T` ownership token

## `T`

`T` is the user-facing owned value type.

Storage location is not encoded in type spelling. A `T` value may be stack-backed,
heap-backed, or backed by runtime-managed storage. User code should not need a
different surface type for that detail.

Examples:

```peep
let n: i32 = 10
let s: string = "fuad"
let xs: DynArray[i32] = make_array()
```

Ownership belongs to the `T` binding, not to raw pointers that point at it.

## Raw Pointers

### `^T`

`^T` is a mutable raw pointer to `T`.

- points to storage owned somewhere else
- does not own or free that storage
- permits mutation through pointer operations
- low-level tool with unsafe failure modes

### `^const T`

`^const T` is a read-only raw pointer to `T`.

- points to storage owned somewhere else
- does not own or free that storage
- may be copied like other read-only view data unless contained type says otherwise
- can still dangle if provenance/lifetime rules are violated

## Optionals

`?T` is either a `T` value or `none`.

Examples:

```peep
let x: ?i32 = none
let next: ?^Node = none
```

Implementation status:

- `none` lowers in expected optional contexts.
- `T` can lower to `?T` as `some(T)`.
- `?^T` and `?^const T` use pointer niche layout.
- other optionals currently use tagged layout.

Future layout work may add niche detection for more types.

## Strings

`string` is an immutable non-owning view type.

It is cheap to copy and cheap to pass by value because mutation is not exposed
through `string`. If Peeper needs owned mutable text, that should be a distinct
type such as `StringBuf`, not a changed meaning for `string`.

## Copy And Move

Normal `T` values copy by default.

`#[no_copy]` marks a type as move-only:

```peep
#[no_copy]
struct Buffer {
    ptr: ^u8,
    len: int,
}
```

Passing or assigning a `#[no_copy]` value without `move` is invalid when the
operation would copy ownership.

```peep
let a: Buffer = make_buffer()
let b: Buffer = move a
```

After `move a`, `a` is dead until reassigned.

Return statements may move implicitly when returning an owned local:

```peep
fn make() -> Buffer {
    let buf: Buffer = make_buffer()
    return buf
}
```

## Pointer-Containing Types

Types containing `^T` are `#[no_copy]` by default.

Reason:

- shallow-copying mutable raw pointers is easy to misunderstand
- accidental aliasing and shared mutation become likely
- move-only default prevents silent duplication of mutable pointer handles

This default does not apply to `^const T`.

If a type with `^T` must be shallow-copyable, user must opt in:

```peep
#[allow_copy]
struct Cursor {
    ptr: ^u8,
}
```

`#[allow_copy]` means pointer value is copied; pointee is not cloned.

## Function Passing

Passing `T` means value passing:

- copy if type is copyable
- move if call consumes a `#[no_copy]` value

Passing `^T` or `^const T` means pointer passing. No ownership transfer is
implied by pointer type alone.

If callee must mutate caller-owned data, pass `^T` explicitly.

```peep
fn set_first(xs: ^DynArray[i32]) {
    unsafe {
        (*xs)[0] = 10
    }
}
```

## Allocation

Allocator APIs should return owned `T`, not owned pointer types.

```peep
let x: Buffer = allocator.alloc(Buffer)
let y: Buffer = Buffer{}
```

Both bindings are owned `T`. Compiler/runtime tracks whether a value came from
allocator-managed storage.

Free consumes allocator-owned `T`:

```peep
defer allocator.free(x)
```

Calling `free` on a non-allocator-owned stack-only value should be a compile
error once allocator provenance checks exist.

Implementation status:

- allocator provenance tracking is not complete
- `free` ownership validation is future work

## Raw Pointer Safety

Raw pointers are unsafe, but compiler should still check provable cases.

Target checks:

- reject return of pointer to dead local
- reject storing pointer to shorter-lived local into longer-lived object when provable
- reject obvious mutable/read-only alias conflicts for same known root
- reject obvious use after free once allocator provenance exists
- reject obvious double free once allocator provenance exists

When provenance is lost through casts, pointer arithmetic, FFI, or opaque calls,
compiler may require `unsafe` and weaken guarantees.

Implementation status:

- move/no-copy checks for owned `T` exist
- full raw-pointer alias/lifetime checking is future work

## Linked Structures

Unsafe linked structures can use raw pointers directly.

```peep
struct Node {
    val: i32,
    next: ?^Node,
}
```

This is valid because `^Node` has fixed size and `next` is non-owning.

This does not make the list ownership-safe. Whoever owns nodes must keep them
alive longer than all raw pointers that reference them.

## Final Rules

- `T` owns.
- `^T` is mutable raw pointer only.
- `^const T` is read-only raw pointer only.
- `^T` and `^const T` never own.
- `?T` is optional.
- `string` is immutable non-owning view.
- allocator returns owned `T`.
- `free` consumes allocator-owned `T`.
- type containing `^T` is `#[no_copy]` by default.
- `^const T` alone does not imply `#[no_copy]`.
- `#[allow_copy]` permits explicit shallow copy of a `^T`-containing type.
- mutation of same underlying object uses `^T` explicitly.
- compiler performs best-effort raw-pointer safety checks where provenance is known.
