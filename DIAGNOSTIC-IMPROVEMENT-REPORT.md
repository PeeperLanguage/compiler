# Multi-Group Diagnostic Improvement Report

Errors that currently emit a single label but should show additional context locations to answer the user's natural follow-up question.

Each entry includes: the error code, what the user sees today, what question they'd ask next, and what secondary label(s) to add.

---

## Tier 1 — High impact, common errors

### 1. Type mismatch (T0001) — "Where is the expected type defined?"

**Files:** `typechecker/typechecker.go` lines 218, 261, 363, 642, 798, 937, 1016, 1114  
**Current:**
```
[T0001]: cannot return String from function returning i32
  --> main.peep:10:12
10 |     return "hello";
                ~~~~~~~
```
**User asks:** "Where does the function say it returns `i32`?"  
**Add:** `WithSecondaryLabel(fnDecl.ReturnType.Location, "expected i32 because of this return type")`

Applies to:
- Return type mismatch (line 218) → show function signature's return type
- Binding type mismatch (line 363) → show the `: Type` annotation
- Assignment type mismatch (line 261) → show the variable's declaration type
- Argument type mismatch (lines 1016, 1114) → show the parameter declaration
- Binary operator type mismatch (line 642) → show the other operand's type
- Struct field type mismatch (line 798) → show the field declaration

---

### 2. Wrong argument count (T0006) — "What parameters does it expect?"

**File:** `typechecker/typechecker.go` lines 1002, 1083  
**Current:**
```
[T0006]: wrong number of arguments: got 3, want 2
  --> main.peep:5:5
5 | add(1, 2, 3)
    ~~~~~~~~~~~
```
**User asks:** "What are the expected parameters?"  
**Add:** `WithSecondaryLabel(fnDecl.Location, "function defined here with parameters (a: i32, b: i32)")`  
**Add:** `WithCodeHint(...)` showing the expected signature

---

### 3. Immutable assignment (T0007) — "Where was it declared immutable?"

**File:** `typechecker/typechecker.go` lines 281–286  
**Already done** — this is the one existing example of the pattern (shows "make this binding mutable" at declaration site). Good.

But line 1104 (`mutableReceiverDiagnostic`) does the same for method receivers — also already done.

---

### 4. Constant reassignment (T0018) — "Where was it declared const?"

**File:** `typechecker/typechecker.go` line 276  
**Current:**
```
[T0018]: cannot assign to const `MAX`
  --> main.peep:8:1
8 | MAX = 100;
    ~~~
```
**User asks:** "Where is `MAX` declared as const?"  
**Add:** `WithSecondaryLabel(sym.Location, "declared as const here")`

---

### 5. Method not found (T0011) — "What methods does this type have?"

**File:** `typechecker/typechecker.go` line 733  
**Current:**
```
[T0011]: unknown method `fly`
  --> main.peep:12:5
12 | bird.fly();
          ~~~
```
**User asks:** "What methods are available on this type?"  
**Add:** `WithHelp(...)` listing available methods on the type (if any exist)

---

### 6. Field not found (T0010) — "What fields does this struct have?"

**File:** `typechecker/typechecker.go` lines 703, 805  
**Current:**
```
[T0010]: unknown member `naame`
  --> main.peep:6:5
6 | point.naame
        ~~~~~~
```
**User asks:** "What fields does this struct have? Did I misspell one?"  
**Add:** `WithHelp("available fields: x, y")` or `WithHelp("did you mean 'name'?")` using shared levenshtein

---

### 7. Symbol not exported (M0004) — already done

**File:** `resolver/resolver.go` line 368  
**Already has:** `WithSecondaryLabel(resolved.Symbol.Location, "defined here")` + note about uppercase exports. Good.

---

## Tier 2 — Medium impact

### 8. Undefined symbol (T0002) — "Was it defined in another module?"

**File:** `resolver/suggest.go`  
**Current:** Shows "did you mean X?" when close match exists in scope. Good.  
**Missing:** When the symbol exists in an imported module but wasn't qualified, suggest `module::symbol`.

---

### 9. Interface not implemented (T0012) — "Which method is missing?"

**File:** `typechecker/typechecker.go` `satisfiesInterface`  
**Current:** Only used implicitly via `assignable`. No dedicated error for interface non-implementation.  
**When it triggers:** Assignment of a type to an interface variable.  
**User asks:** "Which method am I missing?"  
**Add:** List the missing method(s) with `WithSecondaryLabel` on the interface declaration showing which method signature is unfulfilled.

---

### 10. Invalid cast (T0014) — "What casts are valid?"

**File:** `typechecker/typechecker.go` line 919  
**Current:**
```
[T0014]: cannot cast String to i32
  --> main.peep:9:5
9 | let x = "hello" as i32;
          ~~~~~~~~~~~~~~~~~
```
**User asks:** "What can I cast this to?"  
**Add:** `WithHelp(...)` listing valid cast targets for the source type

---

### 11. Cyclic import (M0002) — "Where is the cycle?"

**File:** `pipeline/pipeline.go` line 99  
**Current:** Shows the cycle path as text.  
**Missing:** Secondary labels on each import statement in the cycle chain, showing the user exactly which `import` lines form the loop.

---

### 12. Circular type dependency (T0013) — "Where is the cycle?"

**File:** `semantics/deps/type_decls.go` line 60  
**Current:** Shows "type declaration cycle: A -> B -> C" as text.  
**Missing:** Secondary labels on each type declaration in the cycle, so the user can see the chain visually.

---

### 13. Missing return (T0017) — already done

**File:** `analysis/cfg/analyze.go`  
**Already has:** Secondary labels on the function return type AND on each branch that doesn't return. Good example of multi-group diagnostics.

---

### 14. Invalid method receiver (T0022) — "What should the receiver look like?"

**File:** `typechecker/typechecker.go` lines 444, 449  
**Current:**
```
[T0022]: impl methods must declare a `Self` receiver as the first parameter
  --> main.peep:15:5
15 | fn do_thing(x: i32) { ... }
    ~~~~~~~~~~~~~~~~~~~~~~~~~~~~
```
**User asks:** "What does a valid Self receiver look like for this impl?"  
**Add:** `WithSecondaryLabel(implDecl.Target.Location, "impl target is Counter")` + `WithHelp("first parameter should be 'self: Counter' or 'self: ^Counter'")`

---

### 15. Field assignment on immutable (T0007) — "Where is the base binding?"

**File:** `typechecker/typechecker.go` line 302  
**Current:**
```
[T0007]: field assignment requires a mutable pointer or mutable local binding
  --> main.peep:14:1
14 | obj.field = 10;
    ~~~~~~~~~~~~~~
```
**User asks:** "Where is `obj` declared? Is it mutable?"  
**Add:** `WithSecondaryLabel(objSym.Location, "binding declared here")` + suggest `let mut`

---

## Tier 3 — Nice to have

### 16. Unused warnings (W0007–W0012) — "Where is the import/symbol?"

**File:** `semantics/usage/usage.go`  
**Current:** Just shows the unused symbol's location.  
**Missing:** For unused imports, show where the module was imported. For unused private functions, could show call sites of similarly-named functions (typo detection).

---

### 17. Ambiguous import (M0005) — "Which modules provide this?"

**File:** `semantics/collector/collector.go` line 35  
**Current:** Shows error message text only.  
**Add:** Secondary labels on each conflicting import statement.

---

### 18. Missing struct field in literal (T0029) — "What fields are required?"

**File:** `typechecker/typechecker.go` line 788  
**Current:**
```
[T0029]: missing struct literal field `z`
  --> main.peep:7:5
7 | let p = .Point{ x = 1, y = 2 };
          ~~~~~~~~~~~~~~~~~~~~~~~~~~
```
**Add:** `WithHelp("required fields: x, y, z")` showing all expected fields

---

### 19. Invalid number literal (L0003/T0009) — "What's the valid range?"

**File:** `typechecker/typechecker.go` line 942  
**Current:**
```
[L0003]: literal `999` does not fit i8
  --> main.peep:3:9
3 | let x: i8 = 999;
            ~~~
```
**Add:** `WithHelp("i8 range: -128 to 127")` showing the valid range for the target type

---

### 20. Not callable (T0005) — "What is this thing?"

**File:** `typechecker/typechecker.go` lines 729, 997  
**Current:**
```
[T0005]: field `name` is not callable
  --> main.peep:10:5
10 | obj.name()
          ~~~~
```
**Add:** `WithHelp("field 'name' has type String — did you mean to access it without '()'?")`

---

## Summary

| Priority | Count | Pattern |
|----------|-------|---------|
| Tier 1   | 6     | Type mismatches, wrong arg count, redeclaration context |
| Tier 2   | 7     | Interface satisfaction, cycles, cast targets, receiver shape |
| Tier 3   | 5     | Unused hints, literal ranges, ambiguous imports |

**Existing good examples to follow:**
- `ErrRedeclaredSymbol` in `errors/common.go` — shows both declaration sites
- `ErrSymbolNotExported` in `resolver.go:368` — shows definition + note about naming
- `ErrInvalidAssignment` in `typechecker.go:281` — shows immutable declaration + "make mutable" hint
- `ErrMissingReturn` in `cfg/analyze.go` — shows return type + each missing branch

**Shared infrastructure available:**
- `diagnostics.NearestName()` — levenshtein for "did you mean?" suggestions
- `diagnostics.Levenshtein()` — raw edit distance
- `CodeHint` system — diff-style replacement/insertion hints
- `WithSecondaryLabel()` — multi-location context
- `WithHelp()` / `WithNote()` — inline guidance
