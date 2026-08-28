# Comparison Semantics And Owned String Equality Plan

## Goal

Make every comparison accepted by typechecking lower safely. Implement owned
`str` value equality, restore canonical comparison-capability checks, and reject
unsupported comparisons before HIR instead of allowing LLVM aggregate-compare
panics.

No new syntax, HIR/MIR node, runtime symbol, or ABI shape.

## Language Contract

Each operator has an explicit contract below. `Rejected` always means a source
diagnostic before HIR; it never means an LLVM panic.

| Type | `==` | `!=` | `<` | `<=` | `>` | `>=` |
| --- | --- | --- | --- | --- | --- | --- |
| Signed integer | Equal numeric value | Unequal numeric value | Signed numeric order | Signed numeric order | Signed numeric order | Signed numeric order |
| Unsigned integer | Equal numeric value | Unequal numeric value | Unsigned numeric order | Unsigned numeric order | Unsigned numeric order | Unsigned numeric order |
| `byte` | Equal byte value | Unequal byte value | Unsigned byte order | Unsigned byte order | Unsigned byte order | Unsigned byte order |
| `f32`, `f64` | IEEE ordered equal | IEEE unordered-or-unequal | IEEE ordered less | IEEE ordered less-or-equal | IEEE ordered greater | IEEE ordered greater-or-equal |
| `bool` | Equal Boolean value | Unequal Boolean value | Rejected | Rejected | Rejected | Rejected |
| `char` | Equal Unicode scalar value | Unequal Unicode scalar value | Unicode scalar-value order | Unicode scalar-value order | Unicode scalar-value order | Unicode scalar-value order |
| `cstr` | Equal pointer value | Unequal pointer value | Rejected | Rejected | Rejected | Rejected |
| `rawptr` | Equal address value | Unequal address value | Rejected | Rejected | Rejected | Rejected |
| `Allocator` | Equal handle value | Unequal handle value | Rejected | Rejected | Rejected | Rejected |
| Owned `str` | Equal length and exact bytes | Unequal length or bytes | Rejected | Rejected | Rejected | Rejected |
| `?T` against `none` | Value is absent | Value is present | Rejected | Rejected | Rejected | Rejected |
| `?T` against `?T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Shared reference `&T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Mutable reference `&mut T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Owned pointer `*T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Fixed array `[N]T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Dynamic array `[]T` | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Struct | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Interface carrier | Rejected | Rejected | Rejected | Rejected | Rejected | Rejected |
| Named enum | Use `is` or `match` | Use `is` or `match` | Rejected | Rejected | Rejected | Rejected |
| Borrowed slice `&[..]T` | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic |
| Borrowed string `&str` | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic | Rejected with view diagnostic |

Important distinctions:

- `bool == bool` and `bool != bool` are supported. Boolean ordering is rejected;
  language defines no `false < true` contract.
- Every `char` comparison is supported. Ordering compares Unicode scalar values,
  not locale, collation, UTF-8 byte order, or normalized text.
- Owned `str == str` and `str != str` compare content. Owned-string ordering is
  rejected because no lexicographic, Unicode, or locale contract is defined.
- `cstr`, `rawptr`, and `Allocator` equality compares opaque handle value. It
  does not inspect pointed-to content or allocator state.
- Borrowed `&str` is a view and remains rejected for every comparison operator;
  owned-string content equality does not silently define borrowed-view equality.

Owned-string comparison is read-only and non-consuming. It compares length and
then exact bytes. Allocator provenance does not participate. Equal empty strings
must not dereference their data pointers. UTF-8 text is not normalized.

Floating comparison follows runtime and constant-evaluation semantics:

- `NaN == NaN` is false.
- `NaN != NaN` is true.
- Ordered comparisons involving NaN are false.
- NaN is a valid `f32`/`f64` runtime value, not a separate source type.
- Bare `nan`/`inf` literals are not part of the current literal surface; NaN may
  still arise from IEEE operations such as runtime `0.0 / 0.0`.
- Do not coerce NaN to zero. Any future NaN literal or predicate API must define
  width and payload semantics explicitly.

## Step 1: Tracking And Regression Tests

1. Check clean task scope while preserving unrelated `todo.md` work.
2. Create `fix/comparison-semantics` from `main`.
3. Create active `comparison-semantics.localplan.md` with required full status,
   risks, validation, and resume sections.
4. Recheck GitHub issues. If no matching issue exists, create one under
   `0.2 Language Foundations`, add it to `Peeper Roadmap`, and set `In Progress`.
5. Add failing focused tests and Peeper fixtures before production edits.
6. Stop for review.

Regression coverage must reproduce:

- owned `str == str` LLVM aggregate-layout panic;
- unsupported aggregate/reference comparisons reaching lowering;
- string ordering reaching aggregate comparison;
- incorrect LLVM ordered-not-equal predicate for NaN.

## Step 2: Canonical Semantic Validation

Use type information as single source of truth:

1. Keep `IsEquatable`; add `RawPtrType` because existing raw-address identity
   runtime behavior requires it.
2. Add `IsOrderable` for integers, `byte`, floats, and `char`. Do not expand
   `IsArithmetic`; character ordering must not enable character arithmetic.
3. Make `validBinaryTypes` use `IsEquatable` for equality and `IsOrderable` for
   ordering.
4. Restructure `typeBinaryExpr` so dedicated optional, named-enum, slice-view,
   and string-view diagnostics run first. Then execute one canonical capability
   validation before returning `bool`.
5. Preserve common-numeric conversion, existing diagnostic codes, optional
   narrowing evidence, and logical-operator diagnostics.
6. Reject references and aggregates before HIR. Address identity remains
   available explicitly through `rawptr`.
7. Stop after focused typeinfo/typechecker tests and rules audit.

Do not add a second comparison policy in HIR or LLVM. Lower phases consume the
typecheck result.

## Step 3: LLVM Owned-String Equality

Keep ordinary typed `ir.Binary` and `mir.Binary`; specialize only physical LLVM
lowering:

1. Detect `TypeString` with existing `isTypeKind`.
2. Add `emitStringEqual` beside current string-carrier lowering. Helper is
   justified because it owns non-trivial control flow and the named-layout
   invariant; it is not a pass-through wrapper.
3. Reuse `emitStringDataAndLength` and named `data`/`length` fields.
4. Compare lengths first. Length mismatch returns false.
5. Return true for equal zero lengths without loading data.
6. Loop with target-width index, load one byte from each carrier, and stop on
   first mismatch or when length is exhausted.
7. Merge true/false paths with existing typed branch/phi builders.
8. Implement `!=` by inverting canonical equality result.
9. Never extract or compare allocator field.
10. Keep generic LLVM `compare` scalar/pointer-only. Invalid aggregate MIR
    remains an internal invariant violation.
11. Use unsigned scalar predicates for `char` ordering.
12. Emit `fcmp une` for floating `!=`; keep ordered predicates for equality and
    relational operators.
13. Stop after 32-bit/64-bit LLVM tests and rules audit.

Do not add `memcmp`, libc coupling, a compiler runtime symbol, duplicated carrier
extraction, or string-specific HIR/MIR nodes.

## Step 4: End-To-End Coverage

### Typechecker matrix

- Exercise every cell in operator/type matrix: each accepted operator must type
  as `bool`; each rejected operator must emit intended source diagnostic.
- Accept all six operators for integers, `byte`, floats, and `char`.
- Accept only `==` and `!=` for `bool`, `cstr`, `rawptr`, `Allocator`, and owned
  `str`.
- Accept only `?T == none` and `?T != none` for optional comparisons.
- Reject all six operators for references, owners, arrays, structs, interfaces,
  borrowed views, and named enums, preserving their dedicated diagnostics.
- Preserve optional-to-`none`, borrowed-view, and named-enum diagnostics.
- Verify invalid comparisons stop before HIR.

### LLVM tests

- Validate owned-string equality on 32-bit and 64-bit targets.
- Check target-width loop index matches string length layout.
- Check length mismatch and empty-string paths dominate byte loads.
- Check allocator field is unused.
- Check `char` ordering predicates and `fcmp une` for float inequality.
- Validate emitted LLVM text with existing backend validation path.

### Peeper source fixtures

Positive runtime fixtures must cover:

- empty owned strings;
- equal and unequal ASCII strings;
- same-length mismatch and prefix/length mismatch;
- equal and unequal UTF-8 strings;
- both `==` and `!=`;
- using both owners after comparison;
- `bool` equality;
- `char` equality and Unicode scalar ordering;
- runtime-generated NaN equality, inequality, and ordering;
- existing `rawptr` address equality.

Negative fixtures must cover:

- boolean ordering;
- owned-string ordering;
- `cstr`, `rawptr`, and allocator ordering;
- safe-reference equality;
- owned-pointer, array, struct, and interface equality;
- preservation of borrowed-view and named-enum diagnostics.

## Step 5: Documentation And Close-Out

1. Add comparison matrix and exact owned-string contract to
   `docs/language-spec.md`.
2. Move comparison P0 item in `todo.md` to solved state and retain only genuine
   remaining work.
3. Run mandatory post-patch audit on every touched function.
4. Update GitHub issue with validation. Move project item to `Done` only after
   approved delivery.
5. Mark local plan done. Keep it until task is fully reviewed; then remove it if
   it has become stale and unnecessary.
6. Do not commit, push, or merge without explicit approval.

## Validation

```bash
gofmt -w <touched-go-files>
GOCACHE=/tmp/peeper-go-cache CCACHE_DISABLE=1 go test -count=1 \
  ./internal/semantics/typeinfo \
  ./internal/semantics/typechecker \
  ./internal/ir/... \
  ./internal/backend/llvm \
  ./internal/pipeline
GOCACHE=/tmp/peeper-go-cache CCACHE_DISABLE=1 go run ./scripts/bundle.go
GOCACHE=/tmp/peeper-go-cache CCACHE_DISABLE=1 \
  PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
git diff --check
git status --short
```

Run focused new fixtures with bundled `build/bin/peeper` before full `x_test`.

## Rules Check Required After Every Step

- No pass-through wrapper added.
- No stale alias retained.
- No parameter ignored.
- No comparison policy duplicated across phases.
- `IsOrderable` exists only as canonical semantic capability.
- `emitStringEqual` exists only for complex backend CFG and carrier invariant.
- Existing diagnostics, narrowing, ownership reads, allocator provenance, and
  scalar comparison invariants preserved.
- Focused validation and exact results recorded in local plan.
