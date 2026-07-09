# Peeper Language Specification (v0.0.2 Draft)

## 1. Core Philosophy

Peeper is a systems-level programming language designed for maximum performance, explicit memory control, and strict safety without the cognitive overhead of garbage collectors or explicit lifetime annotations.

* **No Hidden Allocations:** Heap allocation is always explicit.
* **No Implicit Ownership Transfer:** Moving and cloning owned data must be explicit.
* **Inherited Mutability:** Mutability flows downward from the owner.
* **Explicit C-Quarantine:** Unsafe foreign memory is isolated from the safe language.

---

# 2. The Memory Model

Peeper uses a strict four-tier memory model.

| Syntax            | Name          | Storage        | Ownership | Core Rules                                                                                                                                             |
| ----------------- | ------------- | -------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **`T`**           | Value         | Stack / Inline | Owned     | Copyable by default. Types containing an owned pointer (`^`) become move-only.                                                                         |
| **`^T`**          | Owned Pointer | Heap           | Owned     | Ownership must be explicitly transferred using `move` or duplicated using `copy`. Memory must be explicitly freed using the allocator that created it. |
| **`&T` `&mut T`** | Borrowed View | Anywhere       | Unowned   | Temporary view only. Cannot be stored in aggregate types. May only be returned if derived from borrowed parameters.                                    |
| **`*T`**          | Raw Pointer   | Anywhere       | Unmanaged | Intended exclusively for C interoperability. Dereferencing requires `unsafe`.                                                                          |

### Nullability

Most types are **non-nullable by default**. Prefix a non-raw type with `?` to
allow an absent state.

```rust
?T
?^T
```

Raw pointers are the exception: `*T` is nullable by default because it models C
pointers.

The compiler enforces a null check before allowing access to the underlying value.

### Builtin Text Types

```rust
byte // raw 1-byte data
char // 4-byte decoded Unicode scalar
str  // builtin alias for byte[]
```

`str` has no special immutability rule. The normal binding rule controls whether
the byte slice can be mutated. Unicode decoding APIs produce `char`; indexing a
`str` addresses bytes, not decoded scalars.

---

# 3. Variable Bindings & Mutability

Mutability belongs to the **binding**, not the type. If an object is bound immutably, every field and every owned object reachable through that binding is also immutable.

| Declaration     | Meaning                                  | Can Reassign Binding? | Can Mutate Data? |
| --------------- | ---------------------------------------- | --------------------- | ---------------- |
| `const x: T`    | Compile-time constant                    | No                    | No               |
| `let x: T`      | Immutable value                          | No                    | No               |
| `let mut x: T`  | Mutable value                            | Yes                   | Yes              |
| `let x: ^T`     | Immutable owner                          | No                    | No               |
| `let mut x: ^T` | Mutable owner                            | Yes                   | Yes              |
| `let x: &T`     | Immutable borrow                         | No                    | No               |
| `let x: &mut T` | Mutable borrow                           | No                    | Yes              |
| `let mut x: &T` | Mutable binding holding immutable borrow | Yes                   | No               |

### Struct Fields

Struct fields never declare mutability. Mutability is inherited from the binding.

```rust
struct Player {
    health: i32,
}

let mut player = Player { health: 100 };
player.health = 90;
```

### Parameters and Loop Variables

Function parameters and loop variables introduce immutable bindings by default.

Use `mut` to create a mutable local binding.

```rust
fn heal(mut amount: i32, target: &mut Player) {
    amount = 10;
    target.health = 100;
}

for mut value in values {
    value *= 2;
}
```

---

# 4. Ownership and Moving

Owned pointers (`^T`) cannot be implicitly copied.

```rust
let p1 = allocator.alloc<Player>();
let p2 = p1;        // ERROR
let p3 = move p1;   // OK
```

Types containing owned pointers are also move-only.

```rust
struct Texture {
    image: ^Image,
}

let a = Texture { ... };
let b = a;          // ERROR
let c = move a;     // OK
```

### Explicit Copy

Cloning ownership must always be explicit.

```rust
let a = allocator.alloc<Player>();
let b = copy a;
```

The type decides what `copy` means. It may perform a deep copy or reject copying entirely.

### Freeing Memory

Peeper has no garbage collector.

Every owned allocation must eventually be returned to the allocator that created it.

```rust
let shader = allocator.alloc<Shader>();
defer allocator.free(move shader);
```

Passing an owned pointer to `free` consumes ownership.

---

# 5. Structs and References (Borrowing)

Borrowed references (`&T`, `&mut T`) provide temporary access to existing objects without transferring ownership.

To keep the language simple and avoid explicit lifetime annotations, references are intentionally restricted.

## Reference Rules

### 1. References Cannot Be Stored

Borrowed references cannot be stored inside aggregate types.

This includes:

* structs
* arrays
* tuples
* enums
* unions
* generic containers

Aggregates may only contain values (`T`) or owned pointers (`^T`).

Long-lived relationships should instead be modeled using owned pointers, IDs, handles, or application-defined references.

```rust
// ERROR
struct Enemy {
    target: &Player,
}
```

```rust
// OK
struct Enemy {
    target: ^Player,
}

// or

struct Enemy {
    target_id: EntityId,
}
```

---

### 2. References May Only Be Returned from Parameters

A function may return a borrowed reference only if every possible returned reference originates from one or more borrowed parameters.

```rust
fn first(s: &str) -> &u8 { ... }

fn longest(a: &str, b: &str) -> &str { ... }

fn bad() -> &i32 {
    let x = 5;
    return &x;    // ERROR
}
```

---

### 3. References Are Temporary

References are intended for temporary access.

They may be:

* passed as parameters,
* stored in local variables,
* reassigned locally,
* returned according to the previous rule.

---

## Borrow Analysis

Peeper performs borrow analysis automatically.

The language exposes **no lifetime annotations**.

For every function returning a borrowed reference, the compiler analyzes its control-flow graph and records every possible origin of the returned reference.

```rust
fn longest(a: &str, b: &str) -> &str {
    if len(a) > len(b) {
        return a;
    }

    return b;
}
```

The compiler records that the returned reference may originate from either `a` or `b`.

At every call site, these origins are substituted with the actual arguments.

```rust
let result = longest(&x, &y);
```

The compiler verifies that every possible origin remains valid for the entire lifetime of `result`.

If any possible returned borrow would become invalid, compilation fails.

No lifetime syntax is required from the programmer.

---

# 6. C Interoperability and `unsafe`

Raw pointers (`*T`) exist solely for interoperability with foreign code.

They carry:

* no ownership,
* no lifetime information,
* no safety guarantees.
* nullable C-pointer semantics.

Safe references may be converted into raw pointers when calling C.

```rust
let value = 10;
c_function(&value as *i32);
```

Converting a raw pointer to a reference is unsafe and requires a null proof.
Converting a raw pointer to `^T` is not a cast; ownership adoption must go
through an allocator/provenance API.

Raw pointers returned from foreign code remain unmanaged until explicitly converted into safe language constructs.

## The `unsafe` Keyword

Heap allocation and manual memory management are **safe** language features.

`unsafe` is required only for operations that the compiler cannot verify.

This includes:

* Dereferencing raw pointers (`*T`)
* Manual address casting
* Unsynchronized mutation of global state
