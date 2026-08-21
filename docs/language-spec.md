# Peeper Language Specification Draft

This file is the canonical draft for the current Peeper language model.
Implementation notes and older pressure-test docs must follow this file, not the
other way around.

## Core Model

Peeper is a systems language with explicit allocation and visible mutation.
Reference-return relationships use `from` clauses instead of lifetime type
parameters.

Core rules:

- Builtin concepts must use builtin syntax, not library-shaped names.
- Heap allocation is explicit.
- Scalars and raw pointers copy implicitly.
- Composites move implicitly on every by-value use.
- Duplication beyond implicit scalar/raw copy is an ordinary user-defined method API.
- Types containing tracked ownership cannot be copied.
- Live owned values are destroyed automatically at normal scope exit.
- Safe code cannot suppress destruction of an owned value.
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
| `&[..]T` | Shared slice view | Copyable temporary view |
| `&mut [..]T` | Mutable exclusive slice view | Implicit transfer; never copyable |

`str` is a builtin owned immutable text type. Its binding owns the string value,
but indexing cannot mutate its contents. A mutable binding may be reassigned to
another `str` value. `&str` is a temporary borrowed view and never owns or frees
the backing bytes. Literals may use permanent program storage; runtime-created
strings follow normal owner destruction and allocator-provenance rules.

Literal forms are explicit at the boundary: `"text"` produces owned `str`,
`c"text"` produces non-owning `cstr`, `b'X'` produces `byte`, and `'X'`
produces `char`. These forms are not implicitly interchangeable.

`len` is a compiler-owned free function for strings, arrays, and borrowed
views. Pipe syntax adapts argument zero like a method receiver: `text |> len()`,
`fixed |> len()`, or `[]i32{1, 2} |> len()`. It returns string byte length or
array element count. A shared borrow of a temporary remains valid for the call
and cannot escape it.

String indexing unit is explicit. `text[i]` is rejected; use
`(text |> as_bytes())[i]` for a byte or `(text |> as_chars())[i]` for a decoded
Unicode character. `as_bytes` returns a borrowed `&[..]byte` view over string
carrier bytes and never allocates. `as_chars` returns an owned `[]char`
decoded from UTF-8, and its storage follows normal dynamic-array cleanup.

`value |> function(args)` is a free-function call with `value` as argument
zero. Argument zero may move or receive an implicit shared/mutable borrow using
same adaptation rules as method receivers; remaining arguments stay explicit.
Ordinary `function(value, args)` calls do not gain implicit borrowing. Dot calls
remain user-declared methods only. LSP completion shows applicable methods and
free functions after either `.` or `|>` and rewrites selected syntax as needed.

String ranges use byte offsets and return borrowed `&str` views. Start and end
must be ordered, within the byte length, and on UTF-8 codepoint boundaries.
Invalid bounds or boundaries trap at runtime. The owner remains responsible
for backing storage and is dropped exactly once.

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

Integer addition, subtraction, multiplication, division, and remainder use the
same finite-width representation. Signed division truncates toward zero. The
unrepresentable signed case `MIN / -1` wraps to `MIN`, and `MIN % -1` is zero.
Integer division or remainder by zero traps at runtime. Floating-point division
and remainder keep IEEE behavior.

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
a newline. `println(expr)` writes the same supported scalar and then appends a
newline.

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
- A borrowed rvalue temporary lives through its enclosing full expression only.
- A reference to a temporary cannot escape through a binding, return, or stored value.

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

Mutable call borrows use reservation and activation. Receiver and arguments
still evaluate left-to-right. A mutable reference argument reserves its storage
when evaluated, but becomes exclusive only when the call starts. Reads and
shared borrows may occur while reserved when they finish before activation:

```peep
update(&mut x, read(&x)) // valid when read returns an owned or copied value
update(&mut items, items |> len()) // mutable borrow activates after len returns
```

Mutation, move, free, destruction, and another mutable call activation remain
invalid while storage is reserved. Shared references passed into the same call
remain live at activation and therefore still conflict:

```peep
both(&mut x, &x)             // error: shared loan enters both
outer(&mut x, write(&mut x)) // error: nested mutable activation
```

This rule applies to explicit `&mut`, implicit mutable reborrows, mutable method
receivers, and optional mutable-reference parameters. It changes only static
loan checking; runtime reference representation and call ABI do not change.

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

Safe code has no `forget` or `leak` operation. A future foreign-memory handoff
must make ownership transfer explicit and require `unsafe`; it cannot silently
disable normal destruction.

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

Borrowed carriers lower as `{ data: rawptr, dispatch: rawptr }`; owning
`*Interface` lowers as `{ data: rawptr, dispatch: rawptr, allocator: rawptr }`.
Owned vtables reserve payload-destruction and storage-release slots before
method slots. Borrowed carriers never invoke cleanup.
Dropping `*Interface` destroys payload through vtable, then releases allocation
through selected program allocator.

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

Every direct or optional reference return declares its possible parameter
origins with a `from` clause. This is a compile-time callable contract, not a
runtime lifetime value.

```peep
fn first(items: &[]Item) -> &Item from items

fn choose(a: &Item, b: &Item, use_a: bool) -> &Item from(a, b)

fn element(items: &mut []Item, index: usize) -> &mut Item from items
```

`from source` declares one possible origin. `from(a, b)` declares alternatives;
the returned loan remains valid only while every possible source remains valid.
Sources are borrowed parameters or `self`. A mutable reference result requires
a mutable-capable source. Function bodies may use fewer origins than declared,
but may never return an undeclared local or parameter origin.

The clause is required even when only one source is possible. It is part of
function-type compatibility and imported callable metadata, so extern and
interface declarations do not require a body for origin inference. It is erased
before HIR and has no runtime representation.

Invalid:

```peep
fn bad(source: &i32) -> &i32 from source {
    let x = 1
    return &x // error: x does not originate from source
}
```

Returning references is rejected until `from` checking is implemented. Safe
self-referential aggregates remain forbidden. Externally borrowed aggregate
fields may be designed later without changing this callable contract.

## Borrowed Temporaries

Safe references may borrow an rvalue only for the enclosing full expression:

```peep
inspect(&make_item()) // valid: temporary is destroyed after inspect returns

let item = &make_item() // error: reference escapes full expression
return first(&make_items()) // error: returned reference escapes temporary
```

Temporary storage is materialized without hidden heap allocation. Temporaries
are destroyed in reverse creation order when the full expression completes.
Raw `@temporary` remains invalid because raw pointers are outside safe-loan
escape enforcement.

## Arrays And Slice Views

`[N]T` and `[]T` are the array family.

`[N]T` is fixed-size inline storage. `[]T` is dynamic array storage with runtime
length/capacity and heap-backed elements.

`[]T{...}` constructs a dynamic-array owner through selected program allocator:

```peep
let empty = []i32{}
let values = []i32{1, 2, 3}
```

Empty literal is `{null, 0, 0, allocator}` and performs no allocation.
Non-empty literal starts with length and capacity equal to element count.
Length and capacity use target `usize`/backend `IndexType`. Allocation and
checked size calculation happen before element initializers run; allocation
failure traps in initial infallible literal model. Category B element
initializers move into array slots.

Dynamic-array owner operations mutate owner header through `&mut []T` and
return no value. Pipe syntax creates mutable borrow automatically:

```peep
let mut values = []i32{1, 2}
values |> append(3)
values |> reserve(16)
values |> resize(8, 0)
values |> shrink(4)
```

`append(&mut array, value)` moves Category B values into new slot. It writes within
capacity or grows geometrically with checked arithmetic. `reserve(&mut array,
minimum)` preserves length and relocates elements only when capacity is too
small. Relocation does not copy or drop moved source slots.

Initial `resize(&mut array, length, fill)` accepts only Category A element types. It
reserves when growing, repeats implicitly copyable `fill`, and shortens length
without element destruction when shrinking. Category B arrays grow through
`append`.

`shrink(&mut array, length)` accepts every dynamic-array element type. It
preserves allocation and capacity and performs no change when `length` is not
smaller. Removed elements are destroyed in
reverse index order, so Category B arrays shrink without a reusable fill value.

Slice views use explicit slice target syntax:

```peep
fn sum(xs: &[..]i32) -> i32
fn fill(xs: &mut [..]i32, value: i32)
```

`&[..]T` can read elements but cannot mutate them. `&mut [..]T` can mutate elements
but cannot resize the dynamic array that supplied the view.

Indexed value access follows element copy category:

```peep
let number = numbers[index]       // Copy element: copies
let item = items[index]           // error when Item is move-only
let shared = &items[index]        // shared borrow
let exclusive = &mut items[index] // exclusive borrow
items[index] = replacement        // drops old element, moves replacement
let address = @items[index]       // raw address; does not extract element
```

Move-only elements cannot be moved out through indexing. Borrow element or
replace slot instead. No indexed partial-move state exists.

Fixed, dynamic, and slice-view indexes may be runtime integers. Known constant
out-of-bounds fixed-array indexes are compile errors; runtime out-of-bounds
indexes trap before element access.

Field access, element indexing, and implicit pointer/reference dereference
compose into one storage place. Reading loads from that place; assignment stores
into it; `&`, `&mut`, and `@` reuse the same location instead of materializing a
copied intermediate. Place roots evaluate once, and index operands evaluate
left-to-right once.

A range creates a slice-view value from a source place. The range is not an
addressable element projection. Future map elements will follow array-style
place semantics for reading, replacement, and borrowing. Map structural
mutation while an element reference is live requires reference-origin conflict
tracking and remains future work.

`[..]T` is a distinct non-owning slice target and is not valid bare storage.
Borrowing array ranges produces `&[..]T` or `&mut [..]T`; neither form owns or
resizes source array.

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
