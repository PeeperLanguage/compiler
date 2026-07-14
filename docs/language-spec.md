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
- Scalars and raw pointers copy implicitly.
- Composites move implicitly on every by-value use.
- Duplication beyond implicit scalar/raw copy is an ordinary user-defined method API.
- Types containing tracked ownership cannot be copied.
- Live owned values are destroyed automatically at normal scope exit.
- Safe references and raw pointers are different syntax and different semantics.

## Types

| Syntax | Meaning | Copy behavior |
| --- | --- | --- |
| scalar builtin | Scalar value | Implicit copy |
| `T` | Composite value | Implicit move; user methods may construct duplicates |
| `?T` | Optional value | Implicit move |
| `*T` | Unique non-null heap handle to `T` | Implicit move; never copyable |
| `rawptr` | Opaque nullable pointer | Copyable pointer bits |
| `&T` | Shared reference to `T` | Copyable temporary view |
| `&mut T` | Mutable exclusive reference to `T` | Implicit transfer; never copyable |
| `[N]T` | Fixed array value | Implicit move; duplication is user-defined |
| `[]T` | Dynamic array value | Implicit move; never copyable |
| `&[]T` | Shared slice view | Copyable temporary view |
| `&mut []T` | Mutable exclusive slice view | Implicit transfer; never copyable |

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
reserved for the future formatting API. Concrete references and heap owners
that satisfy an interface remain the deliberate exception and convert to the
matching interface carrier implicitly.

## Operators And Precedence

Integer and `byte` values support bitwise AND (`&`), OR (`|`), XOR (`^`),
complement (`~`), left shift (`<<`), and right shift (`>>`). Floats, booleans,
raw pointers, and composites do not support bitwise operators. Binary operands
use normal common-numeric-type conversion and produce that integral type.

Bitwise results use finite-width two-complement representation. Right shift is
arithmetic for signed integers and logical for unsigned integers and `byte`.
Shift count must be non-negative and smaller than operand width. Invalid
constant counts are compile errors; invalid runtime counts trap before shift.

Expression precedence, highest to lowest, is:

1. call, index, and selector
2. prefix `+`, `-`, `!`, `~`, `&`, `&mut`, and `@`
3. cast `as`
4. multiplicative `*`, `/`, `%`
5. additive `+`, `-`
6. shift `<<`, `>>`
7. relational `<`, `<=`, `>`, `>=`
8. equality `==`, `!=`
9. bitwise AND `&`
10. bitwise XOR `^`
11. bitwise OR `|`
12. logical AND `&&`
13. logical OR `||`

Compound bitwise assignments are not part of current operator surface.

## Basic Output

`print(expr)` writes one primitive scalar to standard output without appending
a newline.

Supported values are signed and unsigned integers, floats, `bool`, `byte`,
`cstr`, and `rawptr`. Integers and bytes use decimal text, booleans use `true`
or `false`, and raw pointers use hexadecimal pointer notation. Composite and
ownership-bearing values are rejected before lowering.

`println` and character formatting are not part of this first output slice.

## Heap Handles

`*T` is a unique heap handle. It is not a raw pointer.

Rules:

- `*T` is non-null.
- `*T` controls access and cleanup for one heap value.
- `*T` moves on every by-value use.
- After a move, the source is unusable until reassigned.
- no builtin copy operation exists; a user-defined method may allocate a distinct owner.
- `*T` may produce `&T` or `&mut T` views.
- A heap handle cannot be moved or freed while references derived from it are live.

Use `?*T` when absence is needed.

## Raw Pointers

`rawptr` is an opaque nullable pointer for unsafe or foreign-memory boundaries.

Rules:

- Raw pointer address syntax is `@expr`.
- `@expr` produces `rawptr`, not `&T`.
- Raw pointers do not own storage.
- Raw pointers may dangle.
- `rawptr` has no pointee type, field access, method lookup, or safe dereference.
- Future raw-to-typed conversion must require explicit unsafe syntax.

Keeping `@` for raw address syntax makes safe reference syntax (`&`) visually and
semantically separate from unsafe pointer creation.

## References

`&T` and `&mut T` are temporary safe views.

Rules:

- `&x` creates a shared reference.
- `&mut x` creates a mutable exclusive reference.
- `&h`, where `h: *T`, borrows the heap value and produces `&T`.
- `&mut h`, where `h: *T`, borrows the heap value and produces `&mut T`.
- Reference-to-reference types are not part of v1.
- References may be local variables and parameters.
- References cannot be stored in structs, arrays, dynamic arrays, globals, or heap objects in v1.
- `?&T` is allowed; arrays or dynamic arrays of references are not.

Borrowing a field preserves the root origin:

```peep
let r = &player.position
let h: *Player = make_player()
let hp = &h.position
```

`r` originates from `player`; `hp` originates from the heap value controlled by
`h`.

## Mutable References

`&mut T` is exclusive for the borrowed storage.

Binding an existing mutable reference to another local transfers the one
exclusive access capability implicitly. Passing it to a reference parameter
creates a temporary reborrow instead:

```peep
let next = current_mut_ref
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
calls requiring a mutable receiver. By-value composite parameters consume their
arguments automatically; reference parameters borrow.

A parameter whose type is already `&mut T` does not need a mutable binding to
mutate referenced value. Its binding remains immutable and cannot be reassigned.
Parameter mutability is therefore not part of function type compatibility.

## Automatic Destruction

Owned values that remain live on normal scope exit are destroyed automatically.
Moves invalidate source and suppress later destruction. Replacing a live owned
destination destroys its old value before storing replacement.

Unnamed ownership-bearing temporaries are destroyed after their final use in
the full expression. Scalar projection materializes the selected value before
destroying the temporary aggregate or owning pointer that supplied it.

Drop glue recurses through owning pointers, structs, arrays, strings, dynamic
arrays, optionals, and erased interface payloads. Locals, fields, and array
elements drop in reverse declaration or index order. Ownership state must agree
at every control-flow join and loop backedge; ambiguous conditional ownership is
a compile error, never a runtime drop flag.

`free(owner)` performs same cleanup early for an owning pointer and consumes
owner. Panics abort without unwinding in initial model. Ownership-tracked globals
remain rejected until module shutdown ordering exists.

## Interface Contracts And Carriers

`iface` declares a contract. Bare interface names are unsized contracts, not
runtime value types. Runtime dispatch requires `&Shape`, `&mut Shape`, or
`*Shape`.

```peep
iface Reader {
    fn (&Self) read() -> i32
}

fn (counter: &Counter) read() -> i32 {
    return counter.value
}
```

Interface satisfaction is implicit and requires exact method signature after
replacing receiver `Self` with concrete receiver type. `Self` may appear only as
receiver. Generic interface methods and `Self` in parameters or returns reject.

Methods are top-level receiver functions. Receiver target must be concrete named
type declared in current module. Interfaces, aliases, imported types, and
builtins cannot receive user methods. `impl` is not target syntax.

Interface carriers provide runtime dispatch without copying concrete storage:

```peep
fn read(reader: &Reader) -> i32
fn write(writer: &mut Writer, value: i32)

read(&counter)
write(&mut counter, 7)
```

`&Interface` is a shared fat view of original concrete value. `&mut Interface`
is an exclusive fat view, so mutations performed through interface methods
update original value. Neither form owns or allocates concrete storage.

`*Concrete` converts to `*Interface` by moving existing allocation owner and
adding vtable metadata. Conversion never allocates or copies payload storage.
`Concrete -> Interface` and `Concrete -> *Interface` are illegal. Bare interface
types are also illegal in parameters, returns, globals, and aggregate storage.

All carriers lower as `{ rawptr, vtable }`. Vtable slot zero destroys erased
payload; remaining slots dispatch methods. Borrowed carriers never invoke
cleanup. Dropping `*Interface` destroys payload through vtable, then releases
allocation through selected program allocator.

Callable receiver set follows carrier:

- `&Shape`: `&Self` only.
- `&mut Shape`: `&Self` and `&mut Self`.
- `*Shape`: all receivers, including consuming `Self`.

Mutable receivers require mutable binding. Consuming dispatch through `*Shape`
moves payload, invalidates carrier, releases allocation storage, and suppresses
later drop.

Compile-time `T: Shape` constraints remain future generics work; current type
parameter syntax has no constraint checking or monomorphization.

## Returned References

Peeper has no lifetime syntax.

Returned-reference origin summaries are future work. The current compiler
rejects every reference return rather than accepting an unproven lifetime.

```peep
fn choose(a: &Item, b: &Item, use_a: bool) -> &Item // rejected for now
```

Future work will infer parameter-origin sets and substitute them at call sites.

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

## Remaining Implementation Work

Current branch already parses owned `*T`, opaque `rawptr`, implicit composite
moves, automatic drop, `iface`, receiver functions, and bare-interface
rejection. Remaining work:

1. Replace old interface allocation path with direct borrowed and owned carriers.
2. Add typed allocation construction and allocator selection as separate work.
3. Add generic interface constraints and returned-reference origin summaries as separate work.
