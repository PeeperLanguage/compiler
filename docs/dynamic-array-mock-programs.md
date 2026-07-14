# Dynamic Array Construction Mock Programs

Status: implemented Step 15D construction and consuming owner operations.

## Construction

Dynamic-array literal syntax is the existing reserved `[]T{...}` form:

```peep
let empty = []i32{}
let values = []i32{1, 2, 3}
```

The `[]T` spelling makes creation of heap-backed owner storage visible. This is
not implicit promotion of another value and does not produce `*[]T`. The result
is the ordinary move-only `[]T` owner header `{data, len, cap}`.

Empty construction performs no allocation and produces `{null, 0, 0}`.
Non-empty construction allocates exactly enough storage for its elements, so
initial `len` and `cap` both equal literal element count.

Elements evaluate from left to right after successful allocation. Category A
elements copy into slots. Category B elements move into slots and their source
bindings become dead:

```peep
struct Point {
    x: i32
}

fn consume_points() {
    let first = .Point{x = 1}
    let points = []Point{first}
    use(first) // error: first moved into points
}
```

Literal allocation uses selected program allocator. Initial selected-allocator
contract is infallible at language level: allocation failure traps before any
element initializer runs. Future fallible allocator APIs may return an optional
or result without changing literal type.

## Owner Operations

Slice views cannot resize. `&mut []T` grants mutable element access only and
does not contain owner capacity. Dynamic-array owner operations therefore take
and return `[]T` by value:

```peep
fn build() {
    let mut values = []i32{1, 2}
    values = append(values, 3)
    values = reserve(values, 16)
    values = resize(values, 8, 0)
    values = shrink(values, 4)
}

fn compose() {
    let one = append([]i32{}, 1)
    let extended = append(make_values(), 4)
}
```

This uses normal language ownership. Each operation consumes old owner and
returns replacement owner. Reassignment reinitializes moved source binding.
Compiler already accepts this move-then-reinitialize control-flow shape for
ordinary Category B values. Taking owner by value also permits literals and
other temporary owners to flow directly into an operation; an `&mut` API would
require an addressable binding and lose this composition.

`append(array, value)`:

- consumes array owner and value according to normal by-value rules
- writes within capacity when possible
- otherwise allocates larger storage, relocates existing elements, releases old
  storage without dropping relocated elements, and returns new owner
- grows capacity geometrically with checked size arithmetic

`reserve(array, minimum)`:

- consumes array owner
- returns it unchanged when capacity is sufficient
- otherwise relocates elements into storage with at least `minimum` capacity
- never changes length

`resize(array, length, fill)`:

- consumes array owner
- is available only for Category A element types in initial implementation
- shrinking shortens length; removed scalar slots require no destruction
- growing reserves space and initializes new slots from `fill`
- Category B arrays grow through repeated `append`, which supplies one distinct
  value per new slot
- Category B arrays cannot use this operation because one reusable fill value
  cannot represent repeated moves

`shrink(array, length)`:

- consumes array owner and accepts every element type
- returns array unchanged when `length` is not smaller than current length
- otherwise destroys removed elements in reverse index order
- preserves data pointer and capacity and never allocates
- supplies Category B shrinking without pretending a fill value can be reused

These are compiler-owned polymorphic operations, not user methods and not
generic core-library functions. Current compiler parses generic declarations
but does not instantiate generic function bodies, and user methods cannot
target builtin `[]T`. Keeping these operations in language surface avoids a
tactical library API that current semantics cannot implement.

## Views Stay Non-Owning

```peep
fn fill(view: &mut []i32) {
    view[0] = 9
    view = append(view, 10) // error: append requires []i32 owner
}

fn inspect(view: &[]i32) {
    let value = view[0]
    view = reserve(view, 32) // error: reserve requires []i32 owner
}
```

Views remain `{data, len}`. Capacity never enters borrowed ABI.

## Move And Drop

```peep
fn transfer() {
    let values = []i32{1, 2}
    let moved = values
    use(values) // error: values moved
    consume(moved)
}
```

Dropping live dynamic array destroys elements in reverse index order, then
releases backing storage. Empty owner drop may pass null to selected allocator's
deallocation boundary or skip call; both must be defined as no-op behavior.

Growth relocation is a move of element storage, never a user-visible copy.
Relocated source slots are not dropped. No copy hook or runtime drop flag is
introduced.

## Runtime Boundary

HIR and MIR must represent construction and owner operations explicitly. LLVM
lowering may use current selected-program allocator implementation, but source
semantics and phase models must not name libc `malloc`, `realloc`, or `free`.
Allocator selection and instance provenance remain issue #26; this step must not
create a second public allocation model.

Required invariants:

- size and capacity arithmetic traps on overflow before allocator call
- allocation result is checked before element evaluation or relocation
- failed infallible allocation traps
- old storage remains valid until replacement allocation succeeds
- initialized elements are moved exactly once
- old relocated slots are not dropped
- final dynamic-array destruction runs in reverse element order
- owner header updates only after operation succeeds
- views never gain capacity or resize authority

## Rejected Shapes

`append(&mut values, item)` is rejected because `&mut []T` already means mutable
slice view, not mutable access to owner header. It would also reject useful
literal and temporary forms such as `append([]i32{}, item)`.

`values.append(item)` is rejected for initial implementation because it looks
like an ordinary method while builtins cannot receive user-defined methods and
generic method instantiation does not exist.

`allocator.array<T>(...)` is deferred because typed generic allocation and
allocator-instance provenance are issue #26 work. Requiring it now would make
dynamic-array basics depend on an unimplemented generic call path.

Implicit fixed-to-dynamic conversion is rejected. It would hide allocation
behind ordinary assignment and blur fixed inline storage with dynamic owner
storage.

General Category B `resize(..., fill)` is rejected because repeating one moved
value would duplicate ownership. Future closures or iterator-based extension
may provide one freshly constructed value per slot.

## Runtime Fixed-Array Indexes

Fixed arrays accept integral runtime indexes through the same checked element
projection used by dynamic arrays and slice views:

```peep
fn read(values: [4]i32, index: usize) -> i32 {
    return values[index]
}
```

Known constant out-of-bounds indexes remain compile errors. Runtime
out-of-bounds indexes trap before pointer projection. Reads, writes, references,
and raw addresses use one first-class place path composed from Deref, Field, and
Index projections. MIR `Load`, `Store`, `AddrOf`, and `SliceView` consume that
path directly; runtime indexing does not add a one-off address node or
partial-move model.

Range slicing creates a view value from its source place rather than adding a
range projection. Future map element access extends the same place model.
Reference-origin analysis must block map structural mutation while a derived
element reference is live; that enforcement remains separate borrow-checking
work.
