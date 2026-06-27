# Peeper Memory Model

## Goal

Keep language:

- simple
- explicit
- less painful than Rust
- safer than raw C

Do not mix:

- ownership
- borrowing
- raw pointer semantics

Each surface form should have one job.

## Core Types

### `T`

Owned value.

- normal user-facing type
- may be stack-backed or heap-backed internally
- user does not need to care where storage lives
- compiler/runtime may choose storage strategy

Examples:

```peep
let n: i32 = 10
let s: string = "fuad"
let xs: DynArray[i32] = make_array()
```

### `^T`

Mutable raw pointer.

- points to some `T`
- does not own
- mutable access
- low-level / unsafe tool

### `^const T`

Read-only raw pointer.

- points to some `T`
- does not own
- copyable view-style pointer
- low-level / unsafe tool

### `?T`

Optional.

- either value of `T`
- or `none`

Examples:

- `?i32`
- `?string`
- `?^Node`

## Ownership Rule

Ownership belongs to `T`, not pointers.

So:

- `T` owns
- `^T` points
- `^const T` points

Pointer types never imply ownership by themselves.

## Storage Rule

Heap vs stack is not encoded in type spelling.

This is valid:

```peep
let myname: string = "Fuad"
```

even if runtime stores string data outside stack object.

This avoids ugly APIs like:

```peep
let myname: ^string = "Fuad"
```

User should see ownership and value kind, not storage placement details.

## Strings

`string` is a non-owning immutable view type.

Like Odin-style string:

- cheap to copy
- cheap to pass
- no in-place mutation
- usually represented internally as pointer plus length

Because `string` is immutable and non-owning:

- shallow copy is fine
- pass-by-value does not create mutation confusion

If language later needs owned mutable text, that should be a different type.

Examples:

- `StringBuf`
- `DynString`
- `OwnedString`

## Copy And Move

### Copyable types

Normal value types are copyable unless marked otherwise.

### `#[no_copy]`

Marks type as move-only.

Passing by value moves it.

Assigning to another binding moves it.

### Default rule for pointer-containing types

If a type contains `^T`, it is `#[no_copy]` by default.

Reason:

- shallow-copying mutable raw pointers is easy to misunderstand
- accidental aliasing and shared mutation become likely
- move-only is safer default

This rule does **not** apply to `^const T`.

So:

- contains `^T` => default `#[no_copy]`
- contains only `^const T` => may still be copyable

### `#[allow_copy]`

User may explicitly opt in to shallow copy for a `^T`-containing type.

Meaning:

- pointer value is duplicated
- pointee is not cloned
- aliasing consequences are user responsibility

Example:

```peep
#[allow_copy]
struct Cursor {
    ptr: ^u8,
}
```

This should be treated as an explicit expert override, not compiler-proven safety.

## Function Passing

### Passing `T`

Passing `T` means value passing.

Behavior:

- copy if type is copyable
- move if type is `#[no_copy]`

For `string`, this is cheap because `string` is immutable non-owning view.

For `#[no_copy]` container types, this transfers ownership.

### Passing `^T`

Passing `^T` means raw pointer passing.

- no ownership transfer implied by type
- mutation through same pointed object/storage possible
- should be treated as low-level / unsafe capability

### Passing `^const T`

Passing `^const T` means read-only raw pointer passing.

## Mutation Rule

If user wants callee to mutate same underlying object, pass pointer.

Example:

```peep
fn set_first(xs: ^DynArray[i32]) {
    unsafe {
        (*xs)[0] = 10
    }
}
```

Do not allow confusing mutation-through-shallow-copy semantics for owned mutable values.

If same data should be mutated, pointer passing must be explicit.

## Allocation

Allocator returns `T`, not `^T`.

Example:

```peep
let x: Buffer = allocator.alloc(Buffer)
let y: Buffer = Buffer{}
```

Both are owned `T`.

Difference in storage origin is compiler/runtime knowledge, not surface type distinction.

### Free

Free consumes owned value allocated by allocator.

Example:

```peep
defer allocator.free(x)
```

Calling `free` on non-allocator-owned stack-only value is compile error.

Compiler should track allocation provenance as best as possible.

## Move

`move` transfers ownership of `T`.

Example:

```peep
let a: Buffer = allocator.alloc(Buffer)
let b: Buffer = move a
```

After move:

- `a` is dead
- `b` is owner

Implicit move on return is allowed:

```peep
fn make(a: Allocator) -> Buffer {
    let buf: Buffer = a.alloc(Buffer)
    return buf
}
```

## Raw Pointer Safety

Raw pointers are unsafe.

There is no ownership rule for pointers themselves.

So:

- `^T` does not auto-free pointee
- `^T` may alias
- `^T` may dangle if user violates lifetime discipline

But compiler should still check best-effort safety when provenance is known.

## Best-Effort Compile-Time Checks

Peeper should not be fully C-like footgun.

Compiler should check what it reasonably can:

- no `free` on non-heap / non-allocator value
- no obvious double free
- no obvious use after free
- no return of pointer to dead local
- no storing pointer to shorter-lived local into longer-lived object when provable
- no obvious mutable/const alias conflict when same root is known

Once provenance is lost through casts, pointer arithmetic, FFI, or opaque escapes, compiler may require `unsafe` and weaken guarantees.

## Lifetime Rule For Views

`^const T` and `string` views can still dangle if built from dead storage.

So even though `^const T`-only types may be copyable, compiler should still reject obvious dangling escapes.

Example bad case:

```peep
fn bad() -> string {
    let tmp = make_temp_string()
    return tmp.view()
}
```

If returned view points to dead storage, compiler should reject it when possible.

## Linked Structures

Unsafe linked structures can use raw pointers directly.

Example:

```peep
struct Node {
    val: i32,
    next: ?^Node,
}
```

This is valid because:

- `^Node` has fixed size
- `next` is non-owning raw pointer

But this is not automatically safe ownership.

Meaning:

- list correctness is manual / unsafe responsibility
- compiler should not pretend `next` owns pointee

If user wants safe owned linked/container structures later, language/runtime may provide other abstractions. Core raw-pointer model does not imply that.

## Example Types

### Copyable view

```peep
struct string {
    data: ^const u8,
    len: int,
}
```

### Move-only mutable container

```peep
struct DynArray[T] {
    data: ^T,
    len: int,
    cap: int,
}
```

Because it contains `^T`, it is `#[no_copy]` by default.

### Explicit shallow-copy pointer wrapper

```peep
#[allow_copy]
struct Cursor {
    ptr: ^u8,
}
```

## Generic Caveats

Generic types/functions may become non-copyable depending on instantiated fields.

Example:

```peep
struct Pair[T] {
    a: T,
    b: T,
}
```

`Pair[i32]` may be copyable.

`Pair[DynArray[i32]]` may be move-only.

This is acceptable and should be handled by generic constraints or instantiation errors.

## Final Locked Rules

- `T` owns
- `^T` is mutable raw pointer only
- `^const T` is read-only raw pointer only
- `?T` is optional
- `string` is immutable non-owning view
- allocator returns owned `T`
- `free` consumes allocator-owned `T`
- type containing `^T` is `#[no_copy]` by default
- `^const T` alone does not imply `#[no_copy]`
- `#[allow_copy]` permits explicit shallow copy of `^T`-containing type
- mutation of same underlying object/data should use pointer explicitly
- compiler performs best-effort pointer safety checks where provenance is known

This keeps Peeper:

- explicit
- simple
- safer than C
- much less painful than Rust
