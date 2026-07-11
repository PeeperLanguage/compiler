# Peeper Language Specification Draft

This file is the canonical draft for the current Peeper language model.
Implementation notes and older pressure-test docs must follow this file, not the
other way around.

## Core Model

Peeper is a systems language with explicit allocation, visible mutation, and no
user-written lifetime parameters.

Core rules:

- Builtin concepts must use builtin syntax, not library-shaped names.
- Heap allocation is explicit.
- Resource-owning values cannot be implicitly copied.
- `move` transfers resource authority explicitly.
- `copy` means an explicit duplication operation, never silent shallow copy.
- Safe references and raw pointers are different syntax and different semantics.

## Types

| Syntax | Meaning | Copy behavior |
| --- | --- | --- |
| `T` | Value | Copyable when all contained values are copyable |
| `?T` | Optional value | Copyable when `T` is copyable |
| `^T` | Unique heap handle to `T` | Move-only |
| `*T` | Raw nullable pointer to `T` | Copyable pointer bits |
| `&T` | Shared reference to `T` | Copyable temporary view |
| `&mut T` | Mutable exclusive reference to `T` | Not copyable |
| `[N]T` | Fixed array value | Copyable when `T` is copyable |
| `[]T` | Dynamic array value | Move-only |
| `&[]T` | Shared slice view | Copyable temporary view |
| `&mut []T` | Mutable exclusive slice view | Not copyable |

`str` is a builtin text type. It must not force a library-shaped type into user
code. Exact `str` storage remains tied to the array/string design work.

## Numeric Literals And Conversions

Numeric literals may carry an attached explicit source type:

```peep
42i32
255u8
0xffu24
2.4f32
1e3f64
```

`iN` and `uN` accept every LLVM integer width from 1 through 2^23. Floating
postfixes are limited to `f32` and `f64`; other `fN` forms are reserved for a
future language-owned floating representation.

Signed and unsigned integers share one conversion class. Conversion to a wider
integer width is implicit regardless of signedness. Same-width signedness
changes and narrowing require `as`. Float widening from `f32` to `f64` is
implicit; float narrowing requires `as`.

Integer, float, byte, character, and string are separate conversion classes.
Cross-class conversion is never implicit. `byte` is semantically distinct from
`u8`, even though both currently lower to 8-bit storage; byte formatting is
reserved for the future formatting API. Concrete values that satisfy an
interface remain the deliberate exception and convert to that interface
implicitly.

## Heap Handles

`^T` is a unique heap handle. It is not a raw pointer.

Rules:

- `^T` is non-null.
- `^T` controls access and cleanup for one heap value.
- `^T` cannot be implicitly copied.
- `move h` transfers the handle.
- After `move h`, `h` is unusable until reassigned.
- `copy h` is allowed only when the type defines a valid duplication operation.
- `^T` may produce `&T` or `&mut T` views.
- A heap handle cannot be moved or freed while references derived from it are live.

Use `?^T` when absence is needed.

## Raw Pointers

`*T` is a raw nullable pointer for unsafe or foreign-memory boundaries.

Rules:

- Raw pointer address syntax is `@expr`.
- `@expr` produces `*T`, not `&T`.
- Raw pointers do not own storage.
- Raw pointers may dangle.
- Raw pointer dereference and raw-to-reference conversion require `unsafe`.
- `*T` is nullable by default; `?*T` is not part of the target model.

Keeping `@` for raw address syntax makes safe reference syntax (`&`) visually and
semantically separate from unsafe pointer creation.

## References

`&T` and `&mut T` are temporary safe views.

Rules:

- `&x` creates a shared reference.
- `&mut x` creates a mutable exclusive reference.
- `&h`, where `h: ^T`, borrows the heap value and produces `&T`.
- `&mut h`, where `h: ^T`, borrows the heap value and produces `&mut T`.
- Reference-to-reference types are not part of v1.
- References may be local variables and parameters.
- References cannot be stored in structs, arrays, dynamic arrays, globals, or heap objects in v1.
- `?&T` is allowed; arrays or dynamic arrays of references are not.

Borrowing a field preserves the root origin:

```peep
let r = &player.position
let h: ^Player = make_player()
let hp = &h.position
```

`r` originates from `player`; `hp` originates from the heap value controlled by
`h`.

## Mutable References

`&mut T` is exclusive for the borrowed storage.

Binding an existing mutable reference to another local requires `move` because
the binding transfers the one exclusive access capability. Passing it to a
non-consuming reference parameter creates a temporary reborrow instead:

```peep
let next = move current_mut_ref
inspect(current_mut_ref) // invalid after transfer
mutate(next)             // call temporarily reborrows next
```

The compiler must reject obvious alias conflicts:

```peep
foo(&mut x, &x)      // error
foo(&mut x, &mut x)  // error
```

For aggregate paths, the compiler may be conservative:

```peep
foo(&mut xs[i], &mut xs[j]) // may be rejected when equality is unknown
```

## Mutable Parameters

Parameters are immutable bindings by default. `mut` makes owned parameter
binding mutable:

```peep
fn update(mut writer: Writer, mut value: i32) {
    writer.write(value)
    value = 9
}
```

Mutable binding permits reassignment, field mutation, mutable borrowing, and
calls requiring a mutable receiver. It is independent from ownership transfer;
`move mut value: T` is both consuming and mutable.

A parameter whose type is already `&mut T` does not need a mutable binding to
mutate referenced value. Its binding remains immutable and cannot be reassigned.
Parameter mutability is therefore not part of function type compatibility.

## Interface Values

Bare interfaces follow ordinary value rules. Converting copyable `T` to an
interface boxes a copy. Converting move-only `T` requires `move`, like any other
value-consuming operation.

Interface references provide borrowed runtime dispatch without copying concrete
value:

```peep
fn read(reader: &Reader) -> i32
fn write(writer: &mut Writer, value: i32)

read(&counter)
write(&mut counter, 7)
```

`&Interface` is a shared fat view of original concrete value. `&mut Interface`
is an exclusive fat view, so mutations performed through interface methods
update original value. Neither form owns or boxes a copy of concrete value.

## Returned References

Peeper has no lifetime syntax.

When a function returns a reference, the compiler records which reference
parameters can be the return origin.

```peep
fn choose(a: &Item, b: &Item, use_a: bool) -> &Item {
    if use_a {
        return a
    }
    return b
}
```

The summary for `choose` says the return may originate from `a` or `b`. At each
call site, the returned reference must not outlive any possible origin.

Invalid:

```peep
fn bad() -> &i32 {
    let x = 1
    return &x
}
```

## Arrays And Slice Views

`[N]T` and `[]T` are the array family.

`[N]T` is fixed-size inline storage. `[]T` is dynamic array storage with runtime
length/capacity and heap-backed elements.

Slice views use reference syntax over dynamic-array element spelling:

```peep
fn sum(xs: &[]i32) -> i32
fn fill(xs: &mut []i32, value: i32)
```

`&[]T` can read elements but cannot mutate them. `&mut []T` can mutate elements
but cannot resize the dynamic array that supplied the view.

These reference forms are the language's slice views. There is no separate
slice type: borrowing dynamic-array elements produces `&[]T` or `&mut []T`,
and neither form owns or resizes the source array.

## Ranges

Range syntax is exclusive by default.

```peep
a..b   // excludes b
a..=b  // includes b
```

Slicing uses range syntax:

```peep
data[..]      // full view
data[..n]     // prefix, excludes n
data[n..]     // suffix
data[i..j]    // excludes j
data[i..=j]   // includes j
```

`..<` is not part of the target language.

## Implementation Pre-Work

Current branch already split `^T` from raw `*T` and tracks move-only heap handles.
Remaining work:

1. Add returned-reference origin summaries.
2. Add borrow locks so heap handles cannot move while derived refs are live.
3. Lower dynamic-array operations and slice-view creation once bounds and ABI policy are complete.
