# Peeper Ownership And Pointer Model

This document is the current design reference for ownership, pointers, copy rules,
and optionals.

Target model:

- `T` owns values.
- `^T` is a non-null owned heap handle.
- `*T` is a nullable raw pointer for FFI and unsafe interop.
- `?T` is optional for non-raw values.
- `?^T` is optional owned heap storage.
- `move` transfers ownership explicitly.
- `copy` must be explicit when duplication is allowed.
- Shallow copy of `^T` is never implicit.

Rejected old model:

- `^T` as raw pointer
- `^const T`
- shallow-copy opt-in for owning pointer fields
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
let xs: DynArray[i32] = make_array()
```

Ownership belongs to the binding. A plain `T` may be copied when its type is
copyable. A `T` containing owned heap handles is move-only unless the type
defines an explicit deep copy.

## Owned Heap Handles

### `^T`

`^T` is an owning handle to heap storage containing `T`.

- non-null by default
- must be moved or freed explicitly
- cannot be implicitly copied
- cannot be forged from raw pointer bits with `as`
- may be nullable only when written as `?^T`

Examples:

```peep
let node: ^Node = allocator.alloc<Node>()
let next: ?^Node = none
let moved: ^Node = move node
```

Raw memory returned from C is not automatically owned. Ownership adoption must
go through an explicit allocator/provenance API.

## Raw Pointers

### `*T`

`*T` is a nullable raw pointer to `T`.

- points to storage owned somewhere else
- does not own or free that storage
- may be null
- may dangle
- may be copied because copying raw pointer bits does not copy ownership
- dereference and raw-to-reference conversion require `unsafe`

Examples:

```peep
#[extern("read")]
fn read(fd: i32, buf: *byte, n: usize) -> isize;

let raw: *byte = malloc(16)
if raw == none {
    return
}
```

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
- `?^T` should use pointer niche layout.
- other optionals currently use tagged layout.
- raw `*T` is nullable by default, so `?*T` is not part of the target model.

Future layout work may add niche detection for more types.

## Strings

`str` is a builtin alias for `byte[]`.

There is no special immutability mechanism for strings. The normal binding rule
decides mutation: immutable bindings cannot mutate, mutable bindings may mutate
when the operation itself is available. A decoded Unicode scalar is `char`, a
4-byte value distinct from UTF-8 bytes.

## Copy And Move

Normal `T` values copy by default.

`^T` is move-only. Types containing `^T` are also move-only unless they define
an explicit deep copy.

```peep
struct Buffer {
    ptr: *byte,
    len: int,
}
```

Passing or assigning an owned handle without `move` is invalid when the
operation would transfer ownership.

```peep
let a: ^Buffer = make_buffer()
let b: ^Buffer = move a
```

After `move a`, `a` is dead until reassigned.

`copy` is explicit and must mean an ownership-safe duplication:

```peep
let clone: ^Buffer = copy a
```

The implementation may use memcpy only for trivially copyable payloads. If `T`
contains owned fields, copy must recursively clone those fields or reject the
operation until the type defines clone behavior.

## Function Passing

Passing `T` means value passing:

- copy if type is copyable
- move only when explicitly written and the callee consumes ownership

Passing `^T` means owned heap handle passing. Passing it to a consuming
parameter requires `move`.

Passing `*T` means raw pointer passing. No ownership transfer is implied.

```peep
fn destroy(move buf: ^Buffer) {
    allocator.free(move buf)
}
```

## Allocation

Allocator APIs return owned heap handles.

```peep
let x: ^Buffer = allocator.alloc<Buffer>()
```

Free consumes allocator-owned `^T`:

```peep
defer allocator.free(move x)
```

Calling `free` with a raw `*T` or a non-owned value should be a compile error
once allocator provenance checks exist.

Implementation status:

- allocator provenance tracking is not complete
- `free` ownership validation is future work

## Raw Pointer Escape Analysis

Raw pointers are non-owning. Compiler escape analysis rejects provable attempts
to return raw pointers to local stack storage.

Use `@expr` to produce a raw pointer to addressable storage:

```peep
fn bad() -> *i32 {
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

## Linked Structures

Unsafe linked structures can use raw pointers directly.

```peep
struct Node {
    val: i32,
    next: *Node,
}
```

This is valid because `*Node` has fixed size and `next` is non-owning.

This does not make the list ownership-safe. Whoever owns nodes must keep them
alive longer than all raw pointers that reference them.

Owned recursive structures use `?^Node` and transfer/free recursively by policy:

```peep
struct Node {
    val: i32,
    next: ?^Node,
}
```

## Final Rules

- `T` owns.
- `^T` is non-null owned heap handle.
- `*T` is nullable raw pointer only.
- `@expr` produces a non-owning raw pointer to addressable storage.
- `?T` is optional for non-raw values.
- `?^T` is nullable owned heap handle.
- `str` is builtin `byte[]`.
- allocator returns owned `^T`.
- `free` consumes allocator-owned `^T`.
- type containing `^T` is move-only unless explicit deep copy exists.
- `*T` copy is shallow pointer-bit copy because it owns nothing.
- compiler rejects returned raw pointers when they are known to point at local storage.
