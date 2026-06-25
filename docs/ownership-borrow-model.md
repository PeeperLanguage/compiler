# Ownership And Borrow Model

## Goal

Keep user model small.

Do not split concepts into many pointer kinds.

Use:

- `T`
- `^T`
- `^const T`
- `move`

Ownership is tracked by compiler flow analysis, not by a separate owner type.

## Core Types

### `T`

Plain value.

- May live on stack or inside aggregate
- May be copyable or non-copyable
- Can be borrowed

### `^T`

Exclusive handle.

This is single unified concept for:

- exclusive mutable access
- ownership-capable handle

Important:

- `^T` does **not** always mean owner
- some `^T` values are ownership roots
- some `^T` values are temporary borrows
- compiler tracks which by provenance and flow

### `^const T`

Shared read-only borrow.

- never owner
- any number may coexist
- cannot coexist with live `^T` to same storage

## Ownership Sources

There are two main ways to get `^T`.

### Fresh owning `^T`

Example:

```peep
let buff = Allocator::Allocate<Buffer>(100);
```

`Allocate` returns `^Buffer`.

That returned handle is a fresh ownership root.

Binding receives it in move context, so `buff` becomes owning `^Buffer`.

This is equivalent in meaning to:

```peep
let buff: ^Buffer = move Allocator::Allocate<Buffer>(100);
```

even if `move` is implicit for fresh return values.

### Borrowed `^T`

Example:

```peep
let a = 10;
let p = &a;
```

`p` is also `^i32`, but this one is **not** owner.

It is a temporary exclusive borrow rooted in `a`.

So type alone is not enough to tell ownership. Provenance matters.

## Core Rule

`^T` is linear.

Compiler must enforce:

- cannot duplicate owning `^T`
- cannot use after move
- cannot keep multiple live `^T` to same storage
- cannot mix any live `^T` with any live `^const T` to same storage

This is required for allocator-returned `^T` to be sound.

## Borrow Rules

For same storage:

- many `^const T` allowed together
- exactly one `^T` allowed
- `^T` and `^const T` may not coexist

This applies whether handle came from:

- allocator
- local borrow
- field borrow
- parameter reborrow

## Call Semantics

This part must be explicit.

### Borrowed exclusive parameter

```peep
fn update(x: ^Buffer)
```

Call:

```peep
update(buff);
use(buff);
```

Meaning:

- `update` gets temporary exclusive borrow
- caller keeps ownership if caller had ownership
- `buff` remains valid after call

So passing `^T` to plain `^T` parameter is **borrow**, not transfer.

### Shared parameter

```peep
fn inspect(x: ^const Buffer)
```

Call gives temporary shared borrow.

### Ownership-taking parameter

```peep
fn destroy(move x: ^Buffer)
```

Call:

```peep
destroy(move buff);
```

Meaning:

- ownership transfers into callee
- caller may not use `buff` after move

## Why This Choice

Do **not** make plain call of `^T` an implicit move.

That would make ordinary calls silently kill variables.

Do **not** require `move` for every use of `^T` parameter either.

That would make temporary exclusive access awkward and noisy.

So chosen rule is:

- plain `^T` parameter means borrow
- `move ^T` parameter means ownership transfer

## Return Semantics

Function may return fresh owning `^T`.

Example:

```peep
fn alloc_buffer() -> ^Buffer
```

Returned `^Buffer` is treated as movable into binding/result position.

This is ownership-producing return, not shared borrow return.

Returning a borrowed handle to dead local storage is invalid.

Compiler must reject escapes of non-owning borrow when source lifetime ends.

## `move`

`move` is explicit ownership transfer.

Examples:

```peep
let b = move a;
consume(move buff);
return move buff;
```

After move:

- source binding is invalid
- any borrow derived from source must already be dead

## `take`

If language keeps both `take` and `move`, they should not overlap loosely.

Recommended split:

- `move`: transfer ownership of handle/value to new owner
- `take`: extract value/resource from container/field/location that remains structurally valid after extraction

If that distinction is not needed yet, keep only `move` first.

## Copy Rules

`#[no_copy]` marks type as non-copyable.

Recommended rules:

- primitive scalars may be copyable
- `^const T` is copyable as shared borrow handle
- `^T` is not copyable
- types with `#[no_copy]` are not copyable
- structs are copyable only if all fields are copyable and type is not marked `#[no_copy]`

Owning `^T` must never be implicitly copied.

## Lifetime / Provenance Model

Compiler should track each `^T` by provenance tag.

Each handle knows whether it came from:

- fresh owning source
- exclusive borrow of some root
- reborrow from another handle

Flow analysis then tracks:

- live
- moved
- exclusively borrowed
- shared borrowed

This is enough to model ownership without exposing more user-facing pointer types.

## Valid Examples

### Fresh owner from allocator

```peep
let buff = Allocator::Allocate<Buffer>(100);
process(buff);        // borrow
destroy(move buff);   // transfer ownership
```

### Exclusive borrow from local

```peep
let value = 10;
let p = &value;
mutate(p);
```

Here `p: ^i32` but owner is still `value`.

### Shared borrows

```peep
let view1 = &const value;
let view2 = &const value;
inspect(view1);
inspect(view2);
```

## Invalid Examples

### Use after move

```peep
let buff = Allocator::Allocate<Buffer>(100);
destroy(move buff);
use(buff); // invalid
```

### Two live exclusive handles

```peep
let p1 = &value;
let p2 = &value; // invalid if both are ^T
```

### Shared and exclusive at same time

```peep
let s = &const value;
let m = &value; // invalid while s alive
```

## Relationship To Rust

Very close in enforcement spirit:

- `^T` behaves like exclusive mutable reference plus linear capability
- `^const T` behaves like shared borrow

But syntax and exposed model stay simpler:

- no separate surface owner pointer type
- ownership provenance is compiler-tracked internal state

## Dependency Order For Language Work

Recommended order:

1. arrays
2. optionals
3. pointer / borrow semantics
4. move / ownership transfer rules
5. non-copy and escape checks
6. proper `for` built on real containers and optionals

## For Loop Direction

Current temporary iterator protocol should not be final.

Once arrays and optionals exist, better iterator shape is:

```peep
interface Iterator[T] {
    next(self: ^Self) -> ?T
}
```

Reason:

- one method only
- yielded item type explicit
- end-of-iteration encoded naturally as `none`

But this depends on:

- array/container work
- optional `?T`
- generic item type

So iterator redesign should happen after those foundations.

## Final Decision

Lock these semantics:

- `^T` is exclusive handle
- `^T` may be owning root or non-owning borrow
- compiler tracks which by provenance
- plain call to `^T` param is borrow
- `move` param transfers ownership
- allocator returns fresh owning `^T`
- `^const T` is shared non-owning borrow
- no simultaneous `^T` with any `^const T` to same storage

This keeps user model small while still supporting ownership tracking.
