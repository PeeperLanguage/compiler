# Semantics v2 Mock Programs

These programs are design sketches, not compiler fixtures. They intentionally use future syntax so the pointer model can be judged before lexer/parser/typeinfo work starts.

## 1. Heap Ownership Is Visible In APIs

```peep
struct Buffer {
    ptr: *u8,
    len: usize,
    cap: usize,
}

fn make_buffer(cap: usize) -> move ^Buffer {
    let mut buf = Allocator().Allocate<Buffer>();
    buf.len = 0;
    buf.cap = cap;
    buf.ptr = malloc(cap);
    return buf;
}

fn destroy_buffer(move buf: ^Buffer) {
    free(buf.ptr);
    Allocator().Free(buf);
}

fn main() {
    let buf: ^Buffer = make_buffer(1024);
    defer destroy_buffer(buf);
}
```

Effectiveness: good. `^Buffer` makes allocation and free responsibility visible at the call boundary. This matches explicit systems ergonomics better than returning `Buffer` and hiding storage policy.

Pressure: `move` on the return should be treated like a result-transfer modifier, not as part of the core type identity. `^Buffer` remains the type; `move` says returning this value transfers ownership out of the callee. This matches existing parameter style (`move p: T`) better than requiring `return move buf` at every return site.

Recommendation: support modifier slots in a canonical internal order: `move`, then `mut`, then binding name/type. Preserve source spelling for diagnostics if needed, but store normalized modifier flags internally.

## 2. Borrowed Parameters Avoid Copies Without Ownership Transfer

```peep
struct Mat4 {
    values: [16]f32,
}

struct Transform {
    world: Mat4,
}

fn transform_point(m: &Mat4, x: f32, y: f32, z: f32) -> Vec3 {
    return Vec3{
        x: m.values[0] * x + m.values[4] * y + m.values[8] * z + m.values[12],
        y: m.values[1] * x + m.values[5] * y + m.values[9] * z + m.values[13],
        z: m.values[2] * x + m.values[6] * y + m.values[10] * z + m.values[14],
    };
}

fn draw(t: &Transform) {
    let p = transform_point(&t.world, 1.0, 2.0, 3.0);
    Renderer().DrawPoint(p);
}
```

Effectiveness: good. `&T` solves the expensive value-copy problem without making storage policy part of the API. It is strictly a temporary view.

Pressure: field borrow syntax `&t.world` must not imply a reference can outlive `t`. That is fine if returns, fields, globals, and arrays containing borrows are banned.

## 3. Mutable Borrow Is A Temporary Capability

```peep
struct Counter {
    value: i32,
}

fn increment(c: &mut Counter) {
    c.value = c.value + 1;
}

fn main() {
    let mut counter = Counter{ value: 0 };
    increment(&mut counter);
    increment(&mut counter);
}
```

Effectiveness: good. `&mut T` expresses "callee may mutate this object" without transferring or heap-allocating ownership.

Pressure: compiler does not need Rust-style borrow graph if this remains call-local, but it must still reject obvious simultaneous mutable and immutable uses in the same call expression if those can alias:

```peep
fn bad(counter: &mut Counter) {
    takes_two(counter, counter); // should this be illegal if first param is &mut and second is &?
}
```

Recommendation: keep initial rule simple. Enforce storage/return bans first. Add alias exclusivity later only if mutation bugs show up.

## 4. Borrow Storage Must Fail, Including Nested Forms

```peep
struct Enemy {
    target: &Player,       // error
}

struct MaybeTarget {
    target: ?&Player,      // error
}

struct WatchList {
    targets: []&Player,    // error
}

fn target_of(enemy: &Enemy) -> &Player { // error
    return enemy.target;
}

fn callbacks() -> fn() -> &Player {      // error
    return fn() -> &Player { ... };
}
```

Effectiveness: this is the rule that keeps `&T` cheap. The ban must be structural, not textual. It must find borrows inside optionals, arrays, slices, function types, aliases, and future generic instantiations.

Recommendation: implement a single semantic predicate like "type contains borrow" in typeinfo, then use it at storage boundaries. Do not scatter AST-only checks across parser/typechecker.

## 5. Local Borrows Are Useful But Need Scope Edges

```peep
fn sum_pair(pair: &Pair) -> i32 {
    let left: &i32 = &pair.left;
    let right: &i32 = &pair.right;
    return *left + *right;
}

fn bad_escape() {
    let x = 10;
    global_ref = &x; // error: module/global storage of borrow
}
```

Effectiveness: local borrows make traversal ergonomic.

Pressure: if Peeper does not want borrow deref syntax, field/index access through `&T` should auto-deref. Raw `*T` should not get the same comfort outside unsafe code.

Recommendation: allow local borrow bindings, but ban assigning borrow-typed values into any module/global/static or heap field. This is still a scope checker, not lifetime inference.

## 6. Raw C Pointer Is Quarantined

```peep
#[extern("read")]
fn read(fd: i32, buf: *u8, len: usize) -> isize;

#[extern("malloc")]
fn malloc(size: usize) -> *u8;

#[extern("free")]
fn free(ptr: *u8);

fn fill(fd: i32, out: &mut Buffer) -> isize {
    return unsafe {
        read(fd, out.ptr, out.cap)
    };
}

fn from_c() -> move ^Buffer {
    let mem: *u8 = unsafe { malloc(4096) };
    let mut buf = Allocator().Allocate<Buffer>();
    buf.ptr = mem;
    buf.len = 0;
    buf.cap = 4096;
    return buf;
}
```

Effectiveness: good. `*T` makes C memory clearly unmanaged while `^T` remains Peeper heap-handle storage.

Pressure: `Buffer.ptr: *u8` inside a Peeper `^Buffer` is valid but unsafe responsibility remains inside Buffer APIs. That is acceptable for systems code, but docs should say `^Buffer` controls the Buffer object, not automatically all raw pointers inside it unless destructor/free policy says so.

## 7. Optional Pointers Stay Clear

```peep
struct Node {
    value: i32,
    next: ?^Node,
}

struct Cursor {
    current: ?*Node,
}

fn next_node(node: &Node) -> ?*Node {
    return node.next as ?*Node; // raw observation, no ownership transfer
}
```

Effectiveness: mixed. `?^Node` means optional owned child, which is clean for tree ownership. `?*Node` means optional non-owning/raw link, useful for C-like cursors.

Pressure: linked structures with `next: ?^Node` imply recursive ownership and destructive free traversal. Linked structures with `next: ?*Node` are unsafe/non-owning. There is no safe shared graph pointer in this model by design.

Recommendation: document that graph relationships use IDs/handles or raw pointers in unsafe containers, not `&T` fields.

## 8. Slice Borrowing Looks Promising

```peep
fn sum(xs: &[i32]) -> i32 {
    let mut total = 0;
    for x in xs {
        total = total + x;
    }
    return total;
}

fn fill(xs: &mut [i32], value: i32) {
    for mut i in 0..xs.len {
        xs[i] = value;
    }
}

fn main() {
    let xs = [1, 2, 3, 4];
    let n = sum(&xs);
}
```

Effectiveness: very good. `&[T]` and `&mut [T]` are probably the biggest ergonomic win from reintroducing neutered borrows.

Pressure: grammar must define `[T]` as an unsized contiguous sequence target so `&[T]` and `&mut [T]` can be slice views while `[]T` remains available for dynamic array storage.

Recommendation: use `[]T` for dynamic arrays and reserve `&[T]` / `&mut [T]` for slice views.

## 9. Method Receiver Story Improves

```peep
struct File {
    fd: i32,
}

impl File {
    fn read(self: &mut Self, out: &mut [u8]) -> isize {
        return unsafe { read(self.fd, out.ptr, out.len) };
    }

    fn is_open(self: &Self) -> bool {
        return self.fd >= 0;
    }
}
```

Effectiveness: good. `self: &Self` and `self: &mut Self` are clearer than using old raw `^Self` receivers for normal methods.

Pressure: old tests and interfaces currently expect `^Self`. Migration should decide whether interface methods accept borrow receivers or keep explicit first parameter only.

Recommendation: implement receiver migration as a separate slice after core type model split.

## 10. Value vs Heap API Refactor Cost Is Intentional

```peep
fn make_stack_buffer() -> Buffer {
    return Buffer{ ptr: null, len: 0, cap: 0 };
}

fn make_heap_buffer() -> move ^Buffer {
    let buf = Allocator().Allocate<Buffer>();
    return buf;
}

fn use_buffer(buf: &Buffer) {
    print(buf.len);
}
```

Effectiveness: good for explicit APIs. Changing stack to heap changes function signature, and callers see it. That is desired under this philosophy.

Pressure: generic APIs may need overloads or borrow parameters to avoid caring whether caller owns value or heap object:

```peep
let a: Buffer = make_stack_buffer();
let b: ^Buffer = make_heap_buffer();

use_buffer(&a);
use_buffer(&*b); // exact syntax TBD
```

Recommendation: define borrow-from-owned-pointer syntax early. It affects how usable `^T` is.

## Summary

The model is effective if three constraints stay hard:

1. `^T`, `&T`, and `*T` must be separate AST/typeinfo concepts from day one.
2. Borrow bans must be semantic and structural, not parser-only.
3. Raw pointers must stay uncomfortable enough that normal Peeper APIs prefer values, heap handles, or borrows.

Open decisions before implementation:

- Does passing `^T` consume by default, or must calls write `move`?
- Should `-> move T` be required for any owned return transfer, or only for move-only/owned-pointer returns?
- What is borrow-from-owned-pointer syntax: `&*p`, `&p.value`, auto-borrow, or something else?
- Should slice views lower as a distinct internal `SliceViewType`, or as references to an unsized array target?
- Does initial `&mut` enforce alias exclusivity inside one expression, or only storage/return escape bans?
- Is `*T` mutable by default, or do we need `*const T` / `*mut T` later?
