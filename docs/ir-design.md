# Semantic design: check, THIR, MIR, LLVM

Status: PROPOSAL. Decided by the owner (2026-10-17): the points marked
**decided**. Open points are listed at the end.

This replaces two statements in older documents: `overall-design.md` (no `thir`
package; check writes tables over the AST) and the "Later phases" contract in
`architecture.md` (work scheduled per module, phase by phase).

## Goals

1. Constant evaluation now, compile-time code (comptime) later.
2. As few walks over each function body as possible.
3. One owner per fact: a fact computed once, stored once, read by everyone.

## What other compilers do

| Compiler | Constants | Compile-time code | Walks per body |
|---|---|---|---|
| Go (types2) | folded while type checking, exact values | none | check, IR, SSA |
| Odin | folded in the checker | none | check, LLVM |
| Zig | folded by Sema while it types | Sema runs comptime code in the same walk | AST to ZIR, ZIR to AIR, AIR to code |
| Rust | Miri interprets MIR | `const fn` runs on the callee's MIR, built on demand | HIR, typeck, THIR, MIR, LLVM |

Two lessons decide the shape. Folding constants inside the type-checking walk
costs nothing extra (Go, Odin, Zig). Running user code at compile time needs the
called function ready to run while its caller is being checked, so work must be
scheduled per declaration, on demand (Zig, Rust), not module by module.

## Decisions

- **decided** IRs: THIR (typed tree per body), MIR (control-flow graph per
  function), LLVM IR.
- **decided** Check builds THIR in one walk: names, types and constants
  together. There are no side tables over the AST.
- **decided** Simple constants are folded during that walk. Comptime, when it
  comes, runs in a MIR interpreter (Rust style). Both use the same value
  arithmetic in `ir`; see "Constant evaluation" for where each one stops.
- **decided** Scheduling is per declaration, on demand.
- **decided** LLVM IR is written as text and compiled by clang.
- **decided** A shared `ir` crate owns the type model, definition ids, constant
  values with their arithmetic, and operator kinds. THIR and MIR keep their own
  node types: a tree and a graph have different shapes.
- **decided** Order: this design first, then a thin program end to end, then one
  feature at a time through every layer.

## Pipeline and walks

```
project ──► modules (AST)            1 parse per file
              │
check ────────┤ per declaration:     signature: walk its header once
              │                      body: 1 walk, AST -> THIR (+ constants)
              ▼
mir ──────────┤ per function:        1 walk, THIR -> MIR
              │                      flow checks: dataflow over the graph
              ▼                      (init, move, borrow, return) in one pass where they agree
llvm ─────────┘ per module:          1 walk over each function's MIR -> .ll text -> clang
```

A body is walked by three single passes (build THIR, build MIR, emit LLVM) and
by the flow checks. The flow checks are dataflow: they visit the blocks until
nothing changes, which in reverse postorder is two or three rounds for the
usual graph, more for deep loop nests. Constants and comptime add no walk of
their own: constants fold during the THIR walk; a comptime call interprets MIR
that is built once and kept.

## Crates

```
peeper ─► build ─► project ─► parser ─► ast ─► arena
            │
            ├─► thir ─► ir, ast, arena   check: AST -> THIR and signatures; folding
            ├─► mir ──► ir, thir, arena  THIR -> MIR; flow checks; (later) interpreter
            └─► llvm ─► ir, mir          MIR -> LLVM text; clang
```

Only `mir` reads THIR. `llvm` and the interpreter need struct fields, enum
variants and function types for layout and values; those are signatures, and
signatures are `ir` data, written by check.

| crate | owns |
|---|---|
| `ir` | `TypeId` and the type interner; `DefId`; signatures (fields, variants, parameters, results); constant values and their arithmetic; `BinaryOp`, `UnaryOp` |
| `thir` | THIR node types; the checker that builds THIR and writes signatures |
| `mir` | MIR node types; lowering from THIR; flow checks; later the interpreter |
| `llvm` | LLVM text for a module; running clang and the linker |
| `build` | the scheduler: memo tables, demand order, worker threads (the `build` of `overall-design.md`) |

The typed arenas `Arena`, `Id` and `List` now in `ast` are needed by THIR and MIR
as well. They move to one small crate (`pkg/arena`) that all three use, instead
of being written again. The `Node` trait moves with them, generic over the
container that holds the arenas (`trait Node { type Container; fn arena(c: &mut
Self::Container) -> &mut Arena<Self>; }`), and so does the `nodes!` macro, which
takes the container's name (`nodes!(Tree; Expr => exprs, ...)`). The AST tree,
a THIR body and a MIR body each list their arenas once with it.

## The `ir` crate

- **Types.** `TypeId(u32)` names an interned type: integers and floats by width
  and sign, `bool`, `char`, strings, references, pointers, optionals, arrays,
  slices, function types, and nominal types (struct, enum, interface) by their
  `DefId`. A nominal type's fields or variants are not in the type; they are in
  its declaration's signature, written once by check. Equal types have equal ids,
  so comparing types is comparing two numbers. Primitive types have fixed ids and
  need no lookup.
- **Signatures.** What check learns from a declaration's header: a function's
  parameter and result types, a struct's fields, an enum's variants and payloads,
  an interface's methods. Written once per declaration by check; read by check
  itself, by MIR lowering, by LLVM layout and by the interpreter.
- **Definition ids.** `DefId { module, index }`: the module's place in the
  program's order and the declaration's index in its tree. Memo tables are
  vectors indexed by it: no hashing on the hot path.
- **Constants.** A constant value is an integer of any width (1 to 2^23 bits,
  held as 64-bit limbs, inline up to 128 bits; the arithmetic is the
  `pkg/bigint` crate's, which works like LLVM's APInt), a float, `bool`,
  `char`, string bytes, or an aggregate (struct fields, enum variant and payload,
  array elements). Integer arithmetic wraps to the type's width, as the fixtures
  expect (`127i8 + 1i8 == -128i8`); division rounds toward zero, `MIN / -1` is
  `MIN`; division by zero and out-of-range shifts give no value (they trap at
  run time). The same functions serve the checker's folding and the future
  interpreter.
- **Operators.** `BinaryOp` and `UnaryOp`, mapped once from tokens by check;
  THIR, MIR, the interpreter and LLVM read them.

## Constant evaluation

One owner for the meaning of every operation: `ir`'s value functions, on top
of `pkg/bigint` for integer arithmetic (add,
compare, cast, build an aggregate, read a field). Two drivers call them, each for
what the other cannot do:

- **Check folds** literals, operators, casts, struct and enum literals, field
  reads and references to other constants, while it builds THIR. A `const`
  declaration's value comes from this today.
- **The interpreter** (later) runs what folding cannot: calls and control flow,
  over MIR. Once comptime exists, a `const` whose value needs a call is handed to
  it; everything else still folds in check.

Neither driver implements an operation itself, so the two cannot disagree.

## Integer values must match LLVM

The Go compiler shipped three bugs where a folded value and the value LLVM
computed differed (issues #154, #155, PR #158). The rules that prevent them:

1. **One value, one type.** A constant's bits are read only through the type
   check gave it. Nothing evaluates a constant again under another type (#154
   read a checked `i64` index again as the default `i32`: 4294967296 became 0).
2. **A constant is stored with its declared type** after its initializer's
   conversion; a constant that cannot be emitted is an error, never a silently
   missing symbol (#155).
3. **Widening follows the source's signedness.** Where LLVM needs a wider
   operand (a GEP index, a shift count), LLVM emission extends it explicitly:
   `zext` for unsigned, `sext` for signed. LLVM sign-extends a narrow GEP index
   by itself, which read `u1` 1 as -1 (#158).
4. **A shift count has its own type** (`value << count`, fb88e30e). The runtime
   guard compares the count with the width at a type wide enough to hold the
   width; the Go guard wrote the width as a constant of the count's type, so a
   `u2` count compared against 8 cut to 0.
5. **Constants are written from the same bits** (`iN u0x...`), and
   `crates/ir/tests/llvm_parity.rs` runs every operator on widths 1 to 1000 in
   `lli` and in `ir` and requires equal bits.

Floats (`f32`, `f64`) follow the same idea:

6. **Each precision computes in itself.** An `f32` operation is done in `f32`,
   never kept in `f64` between operations; `%` is `frem` (C `fmod`). The parity
   test covers both precisions with zeros, infinities, NaN, subnormals and
   random bits.
7. **A float constant is its bits.** `Value::Float` holds the `f64` bits (an
   `f32` widened exactly) and LLVM gets them as a double hex constant
   (`float 0x3FB99999A0000000`), never as decimal text.
8. **`==` is `fcmp oeq`, `!=` is `fcmp une`,** the others ordered (`olt` ...):
   NaN equals nothing and differs from everything, as in Rust and the Go
   backend.
9. **Which NaN is not specified.** A folded NaN may have other sign or payload
   bits than the NaN the machine makes at run time; LLVM's own folding differs
   from its generated code the same way (73 of 7,200 cases in the parity test).
10. **A float literal is parsed straight to its precision.** Parsing an `f32`
    literal as `f64` and then rounding to `f32` rounds twice and can give the
    wrong `f32` (the Go compiler did this).
11. **Programs using float `%` link the C math library** (`-lm`): LLVM turns
    `frem` into a call to `fmod` or `fmodf`.

## Scheduling per declaration

```
signature(d)   header of d: parameter and result types, fields, variants
constant(d)    the type and value of constant d (a constant has no signature:
               without a written type, its type is its value's)
body(d)        THIR of function d (needs the signatures it calls)
mir(d)         MIR of function d (needs body(d)); later also comptime calls
```

- Each query's result is written once and never changed. A table per module and
  per query, indexed by declaration, holds it.
- A query asked for while it is running is a cycle (`const A = B; const B = A;`)
  and is reported once (T0013), at the declaration asked for again; the other
  declarations of the cycle become silently invalid.
- The `build` crate is the scheduler. It implements `thir::Queries`; the scopes
  of all modules are collected first and handed to each checker as plain data.
  Programs with syntax or import errors are not checked.
- The model is a hybrid. Across modules: the program's order, so imported
  declarations are always ready (import cycles are already errors), and
  independent modules run on separate threads. Inside a module: on demand, in the
  order declarations ask for each other. This replaces the per-module phase
  contract of `architecture.md`.
- Every declaration is checked, used or not, so errors in unused code are still
  reported (unlike Zig).
- Comptime later: check asks `mir(f)` and runs it in the interpreter. The checker
  reaches `mir` through the scheduler, never by depending on the `mir` crate.

## THIR

One per function body, in arenas like the AST. Every expression has a `TypeId`
and a span; every name is resolved to a local or a `DefId`; an expression whose
value is known at compile time is a constant node holding its value, and keeps
its span for messages. Implicit steps the source leaves out (an implicit
conversion, an auto reference) are explicit nodes.

## MIR

One graph per function: locals with their types, basic blocks of statements
(`place = value`) ending in a terminator (goto, branch, switch, call, return).
Flow checks are dataflow passes over the blocks that share one fixed-point
helper. LLVM emission and the future interpreter both read MIR; nothing below MIR
reads THIR.

## Cost on the hot path

- Type equality: one integer comparison. Building a type: one interner lookup,
  none for primitives.
- Memo tables: one vector index per query.
- Constants: folded where they are met; no separate evaluation pass.
- Interner shared by threads: one lock; measure before changing.

## Decided after review (2026-10-17)

- **decided** Untyped constants: typed at once. A literal without a suffix takes
  the type its context expects (`i32`, `f64` by default) and is checked to fit.
- **decided** Generics: checked per instance, remembered by (`DefId`, type
  arguments) so each instance is checked once. Revisit if bounds enter the
  language.
- **decided** Interner: one lock, primitives at fixed ids with no lookup. Change
  only if a large-project benchmark shows contention.

- **decided** Implicit number conversions only widen when every value of the
  source fits the target: `u8` to `i16`, `i8` to `i16`, `u8` to `u16`, `f32` to
  `f64`. `i8` to `u16`, narrowing, and integer-float conversions need `as` (the
  Go compiler also allowed `i8` to `u16`, turning -1 into 65535).
- **decided** A binary operator has its left operand's type: the right operand
  is converted to it when that is value-safe, otherwise it is an error
  (`1i64 + 2i32` is `i64`; `1i32 + 2i64` is an error). A shift's count keeps its
  own type. A literal without a suffix takes the type its context expects, else
  `i32` or `f64`; the right operand's context is the left operand's type, and
  the left operand's is the type the whole expression is expected to have. So
  `x + 1` and `let y: i64 = 1 + x` are `i64`, and `1 + x` with nothing expected
  is `i32 + i64`, an error whose help suggests `1i64`. (Provisional: the left
  literal could later take the right operand's type instead.)
- **decided** An operation check knows will trap (division by a constant zero,
  a constant shift count below zero or at least the width) is a compile error,
  in a body as in a `const`.

- **decided** A module's name is reachable from another module (`m::Name`)
  only when it starts with an upper case letter; otherwise M0004. Names of the
  prelude (`core:global`) are in every module's scope without that rule.
- **decided** `cstr` (a pointer to a C string) is a built-in type, so the
  prelude's `write` and `read` check.

## Open decisions

1. **Disk cache.** `TypeId` is not stable between runs, so a cached module
   interface stores types by structure. Decide with the cache (roadmap M3);
   nothing in this design blocks it.
