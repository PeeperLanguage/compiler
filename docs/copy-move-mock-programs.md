# Copy And Move Pressure Tests

These programs define intended semantics before implementation. They are not
expected to compile on the current compiler.

## Implicit Scalar And Raw Copy

```peep
fn scalar_and_raw() {
    let value: i32 = 7
    let duplicate = value

    let raw: rawptr = @value
    let raw_alias = raw

    use(value)
    use(raw)
}
```

Integers and raw pointers copy implicitly. Raw aliasing stays unsafe-domain
responsibility and does not create tracked ownership.

## Composite Move And Explicit Copy

```peep
struct Point {
    x: i32,
    y: i32
}

fn transfer() {
    let point: Point = .{x = 1, y = 2}
    let next = point
    use(point) // error: point moved into next
}

fn duplicate() {
    let point: Point = .{x = 1, y = 2}
    let next = point.copy()
    use(point) // valid
    use(next)
}
```

Struct layout never grants implicit copy. `point.copy()` is an ordinary method
whose body explicitly constructs the independent result.

## User-Defined Duplication

```peep
struct NodeOwner {
    node: *Node
}

fn (owner: &NodeOwner) copy() -> NodeOwner {
    return .{node = allocate_node_from(&owner.node)}
}
```

Compiler provides no method automatically. User implementation must obey normal
ownership rules and may allocate independent owned storage.

## Fixed And Dynamic Arrays

```peep
fn arrays() {
    let fixed: [2]i32 = [2]i32{1, 2}
    let fixed_copy = fixed.copy()

    let dynamic: []i32 = make_values()
    let moved = dynamic
    use(dynamic) // error: moved

    let cloned = moved.clone() // ordinary user/library allocating method
}
```

Fixed arrays are inline Category B composites. Dynamic arrays own hidden buffer
storage and require a named allocating clone method.

## Optional Propagation

```peep
fn optionals() {
    let scalar: ?i32 = 7
    let scalar_copy = optional_copy(&scalar)

    let owner: ?*Node = none
    let owner_clone = clone_optional_owner(&owner)
}
```

Optional values always move implicitly. Duplication APIs are user-defined.

## By-Value Calls And Borrows

```peep
fn consume(point: Point) {}
fn inspect(point: &Point) {}

fn calls() {
    let first: Point = .{x = 1, y = 2}
    consume(first)
    use(first) // error: by-value call moved first

    let second: Point = .{x = 3, y = 4}
    inspect(&second)
    use(second) // valid
}
```

Parameter type, not a move modifier, communicates transfer versus borrow.

## Returns

```peep
fn make_point() -> Point {
    let point: Point = .{x = 1, y = 2}
    return point
}

fn identity(value: i32) -> i32 {
    return value
}
```

Returning `Point` moves it out. Returning `i32` copies it out. No return or
statement modifier exists.

Reference returns remain rejected in this implementation slice:

```peep
fn first(values: &[]i32) -> &i32 {
    return &values[0] // error until returned-origin summaries exist
}
```

## Mutable Reference Transfer

```peep
fn exclusive(current: &mut Point) {
    let next = current
    use(current) // error: exclusive reference moved
    next.x = 9
}
```

`&mut T` preserves one exclusive capability without user-written `move`.

## Live Owner Overwrite

```peep
fn overwrite(mut first: *Node, second: *Node) {
    first = second // old first drops, then second moves into first
}
```

Automatic destruction makes drop-then-assign only coherent overwrite rule.

## Aggregate Insertion

```peep
struct Pair {
    left: Point,
    right: Point
}

fn pair(left: Point, right: Point) -> Pair {
    return .{left = left, right = right}
}
```

Each Category B field insertion consumes its source automatically.

## Receivers

```peep
fn (point: &Point) inspect() -> i32 {
    return point.x
}

fn (point: Point) consume() -> i32 {
    return point.x
}

fn receivers() {
    let observed: Point = .{x = 1, y = 2}
    observed.inspect()
    use(observed) // valid

    let consumed: Point = .{x = 3, y = 4}
    consumed.consume()
    use(consumed) // error: value receiver consumed it
}
```

## Interface Carriers

```peep
iface Reader {
    fn (&Self) read() -> i32
}

fn borrowed(counter: &Counter) -> i32 {
    let reader: &Reader = counter
    return reader.read()
}

fn owned(counter: *Counter) -> *Reader {
    let reader: *Reader = counter
    return reader
}
```

`&Reader` aliases existing storage and does not allocate. `*Reader` adopts an
existing `*Counter` allocation and does not allocate replacement storage. Bare `Reader`
values reject.

Invalid conversions:

```peep
let concrete: Counter = .{value = 1}
let invalid: Reader = concrete  // error: bare interface is unsized
let invalid_owner: *Reader = concrete // error: allocation must be explicit
```

Owned carrier vtable destroys erased payload. Carrier then releases existing
allocation through selected program allocator.

## Automatic Drop

```peep
fn cleanup(owner: *Node, replacement: *Node, take: bool) {
    let local = owner
    if take {
        consume(local)
    } else {
        consume(local)
    }
    // both paths moved local; no drop

    let mut current = replacement
    current = make_node() // old current drops before replacement
} // remaining current drops here
```

Different ownership state at a join rejects instead of adding runtime flag.

## Function Values

```peep
let callback: fn(i32) -> i32 = transform
let callback_copy = duplicate_callback(callback)
```

Function values are Category B under the closed scalar list. Any duplication API
is user-defined; closure capture policy is future work.
