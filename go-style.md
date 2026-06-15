# Go Style Guide

This file contains all Go-specific idioms, linter rules, and code patterns for the `compiler` repository.
General engineering rules live in [RULES.md](./RULES.md).

---

## String writing

```go
// BAD — redundant Sprintf inside WriteString
b.WriteString(fmt.Sprintf("expected %s", name))

// GOOD — write directly with Fprintf
fmt.Fprintf(&b, "expected %s", name)
```

```go
// BAD — inefficient concat in WriteString
b.WriteString("hello " + name + "!")

// GOOD — split into multiple calls or use Fprintf
b.WriteString("hello ")
b.WriteString(name)
b.WriteString("!")
// or:
fmt.Fprintf(&b, "hello %s!", name)
```

```go
// BAD — O(n²) allocations from concat in a loop
result := ""
for _, s := range parts {
    result += s
}

// GOOD — use strings.Builder
var b strings.Builder
for _, s := range parts {
    b.WriteString(s)
}
return b.String()
```

```go
// BAD — Builder overkill for joining a slice
var b strings.Builder
for i, s := range parts {
    if i > 0 { b.WriteString(", ") }
    b.WriteString(s)
}

// GOOD
strings.Join(parts, ", ")
```

---

## String prefix / suffix — use Cut* variants

```go
// BAD — HasPrefix + TrimPrefix is two calls
if strings.HasPrefix(s, "//") {
    s = strings.TrimPrefix(s, "//")
}

// GOOD — CutPrefix does both atomically (Go 1.20+)
if rest, ok := strings.CutPrefix(s, "//"); ok {
    s = rest
}
```

```go
// BAD
if strings.HasSuffix(s, ".peep") {
    s = strings.TrimSuffix(s, ".peep")
}

// GOOD
if rest, ok := strings.CutSuffix(s, ".peep"); ok {
    s = rest
}
```

```go
// BAD — SplitN to get left/right of a delimiter
parts := strings.SplitN(s, "=", 2)
if len(parts) == 2 {
    key, val := parts[0], parts[1]
}

// GOOD — strings.Cut is cleaner and safe
if key, val, ok := strings.Cut(s, "="); ok {
    // use key, val
}
```

---

## For loops — use range idioms

```go
// BAD — C-style index loop over a slice
for i := 0; i < len(items); i++ {
    fmt.Println(items[i])
}

// GOOD
for _, item := range items {
    fmt.Println(item)
}
```

```go
// BAD — unused blank identifier in range
for i, _ := range items { ... }

// GOOD
for i := range items { ... }
```

```go
// BAD — index loop just to repeat n times
for i := 0; i < n; i++ { doSomething() }

// GOOD — range over integer (Go 1.22+)
for range n { doSomething() }
```

---

## Error handling

```go
// BAD — silently ignoring an error
val, _ := strconv.Atoi(s)

// GOOD — handle or explicitly justify ignoring
val, err := strconv.Atoi(s)
if err != nil {
    return fmt.Errorf("invalid number %q: %w", s, err)
}
```

```go
// BAD — Sprintf nested inside Errorf
return fmt.Errorf(fmt.Sprintf("unexpected token %s", tok))

// GOOD
return fmt.Errorf("unexpected token %s", tok)
```

```go
// BAD — %v loses the error chain
return fmt.Errorf("lookup failed: %v", err)

// GOOD — %w preserves it for errors.Is / errors.As
return fmt.Errorf("lookup failed: %w", err)
```

```go
// BAD — string comparison on errors
if err.Error() == "not found" { ... }

// GOOD
if errors.Is(err, ErrNotFound) { ... }
```

```go
// BAD — err shadowed across nested scopes
val, err := foo()
if err == nil {
    other, err := bar() // shadows outer err
    _ = other
}

// GOOD — use distinct names or restructure
val, err := foo()
if err != nil {
    return err
}
other, err := bar()
```

---

## Conditionals

```go
// BAD — unnecessary else after return
if condition {
    return x
} else {
    return y
}

// GOOD
if condition {
    return x
}
return y
```

```go
// BAD — comparing to bool literal
if found == true { ... }
if ok == false { ... }

// GOOD
if found { ... }
if !ok { ... }
```

---

## Guard clauses — return early

```go
// BAD — happy path buried in nesting
func process(n *Node) error {
    if n != nil {
        if n.Valid() {
            // actual logic here
        }
    }
    return nil
}

// GOOD — guard at the top, logic stays flat
func process(n *Node) error {
    if n == nil || !n.Valid() {
        return nil
    }
    // actual logic here
}
```

---

## Slice and map patterns

```go
// BAD — no preallocation when length is known
var result []T
for _, v := range items {
    result = append(result, process(v))
}

// GOOD
result := make([]T, 0, len(items))
for _, v := range items {
    result = append(result, process(v))
}
```

```go
// BAD — double map lookup
if _, ok := m[k]; ok {
    v := m[k]
}

// GOOD
if v, ok := m[k]; ok {
    // use v directly
}
```

```go
// BAD — appending slice element by element
for _, v := range other {
    result = append(result, v)
}

// GOOD — spread append
result = append(result, other...)
```

Use `slices.Contains`, `slices.Index`, `maps.Keys` etc. (Go 1.21+) instead of hand-rolling loops for these operations.

---

## Type assertions

```go
// BAD — bare assertion panics on wrong type
node := expr.(*BinaryExpr)

// GOOD — comma-ok unless a panic on wrong type is intentional
node, ok := expr.(*BinaryExpr)
if !ok {
    // handle gracefully
}
```

---

## Zero values

```go
// BAD — explicit zero-value initialisation
var count int = 0
var name string = ""
var ok bool = false

// GOOD — rely on zero values
var count int
var name string
var ok bool
```

```go
// BAD — map literal instead of make
e := Env{vars: map[string]Type{}}

// GOOD
e := Env{vars: make(map[string]Type)}
```

---

## Receiver rules

```go
// BAD — mixed pointer and value receivers on the same type
func (n Node) Name() string { ... }
func (n *Node) SetName(s string) { ... }

// GOOD — consistent; use pointer receivers if any method mutates
func (n *Node) Name() string { ... }
func (n *Node) SetName(s string) { ... }
```

---

## Miscellaneous

- Use `any` instead of `interface{}` (Go 1.18+).
- Don't use `new(T)` when `&T{}` is clearer.
- Named return values only when they meaningfully document the output — not as a shortcut for bare `return`.
- `defer` for cleanup is good. `defer` inside a loop is a bug waiting to happen — the deferred call runs at function exit, not loop iteration end.
- Avoid `init()` unless truly necessary; prefer explicit initialisation at call sites.
- Don't use `fmt.Println` / `log.Print` for debug output in committed code. Use the compiler's diagnostic system or remove before committing.