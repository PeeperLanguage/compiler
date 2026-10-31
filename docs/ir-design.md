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
- **decided** Simple constants are folded during that walk. Comptime runs
  in a MIR interpreter (Rust style). Both use the same value
  arithmetic in `eval`; see "Constant evaluation" for where each one stops.
- **decided** Scheduling is per declaration, on demand.
- **decided** LLVM IR is written as text and compiled by clang.
- **decided** One `model` crate owns the program's data: the syntax tree, the
  THIR and MIR node types, the type model, definition ids, constant values
  and operator kinds. What operators mean on constant values is `eval`'s
  (owner, 2026-10-26: "move them to eval"). The crates that build them
  (`parser`, `check`, `lower` and later `llvm`) hold only logic. THIR and
  MIR keep their own node types: a tree and a graph have different shapes.
  (Until 2026-10-17 this was an `ir` crate, with the tree in `ast` and THIR in
  `thir`.)
- **decided** Order: this design first, then a thin program end to end, then one
  feature at a time through every layer.

## Pipeline and walks

```
project ──► modules (AST)            1 parse per file
              │
check ────────┤ per declaration:     function type: walk its header once
              │                      body: 1 walk, AST -> THIR (+ constants)
              ▼
lower ────────┤ per function:        1 walk, THIR -> MIR
              │                      path checks: dataflow over the graph
              ▼                      (init, move, borrow, return) in one pass where they agree
llvm ─────────┘ per module:          1 walk over each function's MIR -> .ll text -> clang
```

A body is walked by three single passes (build THIR, build MIR, emit LLVM) and
by the path checks. The path checks are dataflow: they visit the blocks until
nothing changes, which in reverse postorder is two or three rounds for the
usual graph, more for deep loop nests. Constants and comptime add no walk of
their own: constants fold during the THIR walk; a comptime call interprets MIR
that is built once and kept.

## Crates

```
peeper ─► build ─► project ─► parser ─► model ─► arena
            │
            ├─► check ─► model, project, eval, lower, flow   AST -> THIR, signatures, folding; builds bodies
            ├─► eval ──► model, runtime_abi   operators on constant values; the MIR interpreter
            ├─► lower ─► model            THIR -> MIR; path checks
            └─► llvm ──► model, runtime_abi   MIR -> LLVM text; clang
```

Every phase reads and writes its data in `model`; only `lower` reads THIR, and
`llvm` and the interpreter read MIR and signatures. Signatures (struct fields,
enum variants, function types) are `model` data, written by check.

| crate | owns |
|---|---|
| `model` | data only: `ast` (the syntax tree), `thir` (THIR nodes), `mir` (MIR graphs); `semantic_types` (`TypeId`, the type interner, the built-in types table); `constant` (constant values); `ModuleId`, `DefId`; `operators` (`BinaryOp`, `UnaryOp`); signatures |
| `check` | the checker: scopes, name lookup, building THIR, writing signatures; the declaration tables (`Checker`) |
| `eval` | what operators mean on constant values (`binary`, `unary`, `convert`, `known_trap`, the trap messages `trap_message`); the MIR interpreter (`interpreter::run` for a constant's call, `interpreter::program::run` for a whole program) |
| `runtime_abi` | what a running program does as seen from outside, shared by `llvm` and `eval`: streams and the exit code, runtime function names, panic messages, number text. The two `runtime/` files the runtime library needs too are read from there by `#[path]`, as that library is built with no crates |
| `lower` | lowering from THIR; path checks |
| `llvm` | LLVM text for a module; running clang and the linker |
| `compile` | a compile: walks the modules in order, reuses unchanged ones, lowers each body (the `build` of `overall-design.md`, renamed) |

A `project::Module` holds only what loading read and never changes. What a
compile finds (each module's `Scope`, types, declaration results, diagnostics)
is kept apart, in `check::Checker`, in vectors indexed by `ModuleId`, and
everything links by id. `Checker::new` collects every module's scope, after
every module is loaded. So its checks take `&mut self` and nothing needs a `RefCell`:
holding a type across a check is a compile error, not a run-time panic. A
scope holds declarations; a name it
does not hold is looked up in its parent's, so a module's scope has the
prelude's as its parent. A name stands for a `model::thir::Symbol`: a module,
function, constant or type of a module scope, or a body's local or `const`.

The typed arenas `Arena`, `Id` and `List` live in one small crate (`pkg/arena`)
used by the syntax tree, THIR and MIR. The `Node` trait is generic over the
container that holds the arenas (`trait Node { type Container; fn arena(c: &mut
Self::Container) -> &mut Arena<Self>; }`), and the `nodes!` macro takes the
container's name (`nodes!(Tree; Expr => exprs, ...)`). The syntax tree, a THIR
body and a MIR body each list their arenas once with it.

## The `model` crate's semantic data

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
- **Definition ids.** `DefId { module, index }`: the module (`ModuleId`, its
  place in the program's order) and the declaration's index in its tree. Memo
  tables are vectors indexed by it: no hashing on the hot path.
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

One owner for the meaning of every operation: the `eval` crate's functions, on
top of `pkg/bigint` for integer arithmetic (add, compare, cast; later build an
aggregate, read a field). Two drivers call them, each for
what the other cannot do:

- **Check folds** literals, operators, casts and references to other
  constants, while it builds THIR (not yet struct and enum literals or field
  reads: a value has no struct or array form yet). A `const`
  declaration's value comes from this today.
- **The interpreter** (`eval::interpreter::run`) runs what folding cannot: calls and control
  flow, over MIR. In a constant's value (a module `const` or one in a body), a
  call whose arguments are all constants is run at once
  (`Checker::evaluate_call`), so its result folds on (`f(3) + 2`). It keeps
  variables and values in memory (structs, arrays, ranges) in bytes laid out
  for the target, so references, slices and raw pointers are real addresses,
  checked at each access; a constant's own value is still a number, a bool or
  a `cstr`. `print` and `extern` functions are errors at compile time (T0044),
  as are a `panic`, a failed check, a bad address, running out of stack,
  100,000 calls at once and more than 1,000,000 steps (owner, 2026-10-26: a
  fixed limit, no setting until a program needs one), where each 256 bytes
  copied and each value of a filled range count as a step too. The error is
  at the call, with a label where the run stopped. A constant's value goes into
  the program, and only the running program knows an address, so a constant may
  use an address but not read it (owner, 2026-11-01). `@`, storing a pointer,
  passing it, using it to read or write, and comparing it with `none` all run.
  Comparing two addresses, a cast between raw pointer types and a pointer as
  the final value stop the run. A cast is the only way to a pointer's bytes or
  to what is next to a value, as there is no pointer arithmetic. Rust allows
  such casts and reads the bytes behind them; Peeper's addresses are plain
  numbers, so it stops at the cast.
  Memory is stricter here too, as in Rust, where each thing is its own
  allocation. Each call's frame and each variable, each time it starts, is a
  block of memory never used before (256 MiB in all), and `scope end` ends a
  variable's block. So a pointer to what ended stops the run at its next use,
  and one from an earlier pass of a loop cannot reach this pass's variable. A
  cleanup of MIR must keep `scope end` for this.
- **A whole program** runs on the same interpreter (`interpreter::program::run`,
  through `compile::interpret`), which is how a web page runs code with no
  clang or LLVM. There `print`, panics and the runtime's `alloc`, `free`,
  `calloc`, `write`, `read` (always empty) and `exit` work, and any other
  `extern` stops the run by its name. Output is kept as ordered pieces, each
  with its stream. Limits, each a panic with exit code 101: 2,000,000,000 steps,
  1 MiB of output, 256 MiB of data, 8 MiB of stack, 100,000 calls at once. Past
  10,000 pieces, output joins the last piece, so it stays in order but can be
  marked as the wrong stream. A program with any error, the loader's included,
  does not start. Numbers print through `runtime/number_text.rs`, the runtime's
  own file, which `runtime_abi` includes by `#[path]`, so a run and a compiled program
  print the same text.

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
   `crates/middle/eval/tests/llvm_parity.rs` runs every operator on widths 1
   to 1000 in `lli` and in `model` and requires equal bits.

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
declared_type(d)  the type written after the name of type declaration or
                  alias d
function_type(d)  the function type of function or method d
constant(d)       the type and value of module constant d (without a written
                  type, its type is its value's)
body(d)           THIR of function d (needs the function types it calls)
mir(d)            MIR of function d (needs body(d)); also asked for by a call run at compile time
```

- Module-scope declarations (`DefId`) are checked on demand, one table per
  language category: declared types, function types, constants. A body's own
  names (`Symbol::Local`: a variable or a `const`) are checked in place with
  the body, never asked for. Naming a type declaration checks nothing
  (`Type::Named`); a value's type is complete: a struct value has its
  declaration's type, `Type::Struct { definition, fields }`, and a struct
  that holds another by value asks for that one's type, so a struct that
  holds itself is a cycle. Lowering and LLVM read all of it from `Types`.
- Each declaration's result is written once and never changed. The checker holds
  a table of them per module, indexed by the declaration's place among its
  top-level declarations (`DefId`), so a body edit does not move it.
- A declaration asked for while its check is running is a cycle (`const A = B; const B = A;`)
  and is reported once (T0013), at the declaration asked for again, with a place
  at each use on the path (the checker keeps a stack of the declarations being
  checked and where each was named); the other declarations of the cycle become
  silently invalid.
- `check::Checker` holds the tables and checks each declaration once
  (`check_once`). The `compile` crate runs a compile: it walks the
  modules, reuses unchanged ones and lowers each body. `Checker::new`
  collects the scopes of all modules before any is checked.
  Programs with syntax or import errors are not checked.
- The model is a hybrid. Across modules: the program's order, so imported
  declarations are always ready (import cycles are already errors), and
  independent modules can run on separate threads (not yet: `compile` walks
  them one by one). Inside a module: on demand, in the
  order declarations ask for each other. This replaces the per-module phase
  contract of `architecture.md`.
- Every declaration is checked, used or not, so errors in unused code are still
  reported (unlike Zig).
- A function's body is built (THIR, MIR, the path checks) by
  `Checker::build_function`, for the compile's walk and for a call run at
  compile time; check calls `lower` and `flow` itself (owner, 2026-10-26).
  Its first build reports what its checks find; a later one finds the same
  and drops it. MIR built for a run is kept in `Checker::bodies` for the
  next run; the compile's walk still hands its MIR on and keeps none. A run
  first builds every function it may reach, except one still being checked,
  then only reads the table. A run that calls a function still being checked
  (its body asks, through a constant, for a run that reaches it) is a cycle
  (T0013); one that only may call it is not.

## Reuse between compiles and the language server

Owner's choice (2026-10-17): reuse per module, as Go's and Zig's caches do, not
per declaration. `compile::run` takes the previous compile's `Results` and
gives new ones; the command line passes `Results::default()`, so a cold
compile pays nothing extra. A module is not checked again when its text is
unchanged and the interface of each module it imports (the prelude included)
is unchanged: it keeps its diagnostics. A module's interface is a hash of what
other modules see: its imports, and each declaration's kind, name, and type or
constant value, types hashed by structure. Bodies are not part of it, so a
module whose constants ran functions at compile time also remembers those
functions' modules, and is reused only if each of them was
(`Checker::ran_modules`): such a constant's value, and an error pointing into
the function's file, depend on the function's body.

Only the source, the interface hash and the diagnostics are kept between
compiles. Ids, types, THIR and MIR are rebuilt, since their numbering shifts
with every edit. Spans stay absolute: a module is redone whole. A diagnostic
may point into another module's file (where a type is declared, the function
a constant ran), so a module is also reused only if each module its
diagnostics point into is.

Measured on a generated 20k-function program (100-function modules): a full
check costs about 160 ms; after a body edit the next compile takes about 9 ms,
and after a constant's value changes about 10 ms (two modules). Collecting
every module's names again is most of what remains (about 7 ms).

`peeper lsp` (crate `lsp`, the only user of `lsp-server` and `lsp-types`)
keeps the text of open files, which `project::load` reads before
the disk, and one `compile::Results` per program. A change is checked 200 ms after the
last keystroke; opening, saving and closing a file are checked at once.

Go to definition and hover (owner's choice, 2026-10-18: the checker records
names, not a second resolver). While checking a body, the checker appends a
`thir::Fact` for each name written in it (in `declare`, `resolve`, which
looks every written name up, and module paths), and for each number or string
literal, with the type it got. A name's `Symbol` is what the
checker resolved, not a copy: a local or body `const` by its id (the body's
arenas hold its span and type, so body `const`s get an arena of their own,
`constants`), else a module-level declaration or module. A body with an error
keeps its THIR, marked `has_errors` (as rustc marks typeck results
`tainted_by_errors`), so names work while a function is broken; `lower::lower`
gives no MIR for it, and `use_function` gets its THIR alone. `compile` runs the
phases one by one: `Checker::check_body` gives the THIR, `compile`'s `lower`
the MIR. `compile` has no second way in for editors: the language
server reads the names through `use_function` during its normal check and
keeps them per module, beside the program it compiled, as gopls keeps what
`go/types` records. A module the compile reused gives no names; while its
text is unchanged its names are kept from before, so they hold no ids of one
compile: a local by its span and type's text, a declaration by its module's
path and its index there, found in the current program when asked (a body edit
elsewhere moves spans but not indexes). Definition and hover are lookups;
hover on a declaration shows its source up to the body. A program that fails
to load keeps its last names. Cost on the command line: one push per name;
checkload is unchanged within the noise. Not covered yet: names in signatures
and module-level constants.

## THIR

One per function body, in arenas like the AST. Every expression has a `TypeId`
and a span; every name is resolved to a local or a `DefId`; an expression whose
value is known at compile time is a constant node holding its value, and keeps
its span for messages. Implicit steps the source leaves out (an implicit
conversion, an auto reference) are explicit nodes.

## MIR

One graph per function, shaped like LLVM IR at `-O0` (what clang emits before
optimizing), so writing LLVM text is a direct walk and every part keeps the
source place it came from. Decided by the owner (2026-10-17): flat (shape A),
as close to LLVM as possible, every part traceable to the source for messages
and debuggers; the logic lives in a `lower` crate, the data in `model::mir`.

- **Variables.** Each parameter and `let` is a variable slot with its type,
  `mut`, and the span of its name: an `alloca` plus a `dbg.declare` in LLVM, so a
  debugger finds it by name. Values reach a variable only through `store` and
  leave it only through `load`.
- **Instructions.** A block is a list of instructions, each one operation whose
  id is the value it gives (`%3 = add %1, %2`): `load`, `store`, a binary or
  unary operation, `convert`, `call`, `phi`, and for structs `load` of a
  field path (`load s.1[1, 0]`: one `getelementptr` and load, as clang),
  `aggregate` (a struct value from its fields, LLVM's `insertvalue`) and
  `field` of a struct value not in a variable (`extractvalue`). Operands are a constant, an argument
  or an earlier instruction's value, never a variable. A constant operand is an
  index into the body's constants table (each with its type), so an operand stays 8 bytes. Every
  instruction keeps the span of the expression it came from: LLVM's `!dbg`
  line, and where an error points.
- **Terminators.** Each block ends in one: `br` (goto), `condbr` (with the span
  of its condition), `ret` for a written `return`, `panic` for a written
  `panic "message"` (no successor and no scope ends: a panic does not
  unwind), or `fall off` at the closing
  `}` of every function (LLVM gets `ret void` without a result; with one, the
  missing-return check reports it when it is reachable, and LLVM gets
  `unreachable`). `&&` and `||` lower to `condbr` and a `phi` where both paths
  meet, as clang does.
- **Single assignment by construction.** An instruction's value is written once
  and used after it, so the path checks only follow variables.

```
fn f(a: i32) -> i32 { let b = a * 2; if b > 3 { return b; } return 0; }

bb0:  store a, %arg0          ; a: i32 at 1:6
      %1 = load a              ; 1:31
      %2 = mul %1, const 2     ; 1:31
      store b, %2              ; let b at 1:27
      %3 = load b
      %4 = gt %3, const 3
      condbr %4, bb1, bb2      ; condition at 1:43
bb1:  %5 = load b
      ret %5                   ; 1:52
bb2:  ret const 0
```

Path checks are dataflow passes over the blocks that share one fixed-point
helper; a function whose checking already reported an error is not
path-checked, since its errors would only follow from the first. LLVM emission
and the interpreter both read MIR; nothing below MIR reads THIR.

The missing-return check (T0017, `flow`) reports a function whose
`fall off` block is reachable. It walks back from that block through blocks
with no instructions; on each path, the first branch met names its condition
("if this is false, nothing is returned"), and the first block with
instructions names its last one ("this path ends without returning"). When the
end block itself has instructions, only the end is named, since one `return`
there fixes every path. `Checker::check_body` gives no THIR for a body whose check
reported an error, so such a function is neither lowered nor path-checked.

The variable state check
(`flow::variable_state::report_variable_state_errors`) is forward dataflow
with three bits per variable at each block's start: set on every path to it
("and" at joins), set on some path ("or"), and moved on some path and not set
since ("or"). A `store` to a whole variable sets the first two and clears
the third; a `move` of one clears the first and sets the third.

Lowering decides what copies (`Type::is_copy`: numbers, `bool`, `cstr`,
`&T`): a whole variable whose type copies is a `load`, else a `move`; any
other place whose type copies is a `load` of the place, else a `move` of it.
So the check never looks at types to tell a copy from a move.

A place (`mir::Place`) is a variable and the steps from it, each a field or a
dereference (`mir::Projection`), as in Rust's MIR: `r.*.x` goes through the
reference in `r`, then to a field. The checker makes the implicit step of
`r.x` an explicit `Dereference` in THIR, and lowering turns each into a step,
so neither the check nor the LLVM writer works it out from types. A
reference is never in a field yet, but a raw pointer may be, and `pp.*.*`
takes two steps, so a dereference may be any step. Each element's index is
computed before the place is used, in the order written, and each dereference
reads its pointer only then, as rustc does: in `p.*[f(@p)]` a call that
repoints `p` changes which array is read (owner, 2026-10-29). `&place` and
`&mut place` are a `borrow` of the place, its address. The checker also makes
the spec's reborrow explicit: a `&mut` in a variable passed to a call becomes a
`Borrow` of what it refers to, so the variable stays usable; a `let` moves it.
`@place` is an `address` of the place: the same address as a `borrow`, and
the place must be set, but it makes no loan, so nothing checks how the raw
pointer is used. A `.*` of a typed raw pointer is the same `Dereference` step
as a reference's.

A call of a function with a `...` parameter has no IR of its own. The checker
writes what `f(1, a...)` stands for, the `Borrow` of a `Temporary` holding an
`ArrayLiteral` of the last arguments, or passes a slice spread alone as it is,
so moves, drops and loans need no rule for it.

A value in no variable that is lent, indexed or gone through (`&make()`,
`make()[0]`, `id(&mut x).*`) is THIR's `Temporary`: a hidden variable the
value is stored into where it is computed, so it runs in the order written,
and the place starts there. As in Rust, a temporary ends with its statement;
one in an `if` or loop condition ends before the branch, in each pass, and
one in a side of `&&` or `||` with that side. A `let` whose whole value is a
borrow or slice of one (`let r = &make();`, `let s = &make()[0..2];`) keeps
that one to the block's end: the checker marks it `kept`, and any other in the
value still ends with the `let` (`let e = &view(&make())[0];` keeps the slice,
not the array, so `e` cannot be used after it). Through a call nothing is kept
(`let r = id(&num());` is an error when `r` is used), as in Rust. A `for`
loop keeps every temporary of its iterable to the loop's end: the hidden `let`
of its array ends none, so they end with the block around the loop. Its own
storage is never written (T0007, "cannot assign to a temporary value") and its
address never taken (T0004, "cannot take address of a temporary value", Rust's
E0745), since it is gone before anything could read it; through a reference
it holds, both are normal: `id(&mut x).* = 7`. `&mut` of a constant's name is
an error (T0018, "cannot mutably borrow constant `LIMIT`"), since a constant
has no storage and the write would not change it; Rust only warns
(`const_item_mutation`). `&LIMIT`, `&mut 5` and `&mut (LIMIT + 1)` are
temporaries; Rust's promotion of `&LIMIT` to a value that lasts forever is not
done, so such a reference cannot outlive its statement unless a `let` keeps it.

A slice (`[&]T`, `[&mut]T`) is its own type, not a reference to some
unsized `[T]`: the address of some elements and their count, known only when
the program runs. The loan rules hold for it as for a reference: both answer
`Type::borrows`, which the holder, origin and write checks ask, so no rule is
copied. A slice is never in a field, an array element or behind a reference
or raw pointer, so a slice place is always all of a variable. THIR writes
`s[i]` as an `Index` of `s` with no dereference; lowering adds the
`Dereference` step before the index, so a write through a slice is behind a
reference for every check, as `r.*[i]` is. `&a` where a slice is expected is
a `Borrow` of `a` with the slice type (`Conversion::Reborrow`); a borrow of a
slice place (a `[&mut]` lent again, or used as a `[&]`) is a `Borrow` of its
elements, `s.*`. `s.len` loads the slice, never moving a `[&mut]`, and takes
its field 1.

`&a[i..j]` (or `&mut`, or `..=`) is THIR's `Slice` of the array place `a`,
each end left out or an integer of any type, as an index is; ends known at
compile time are checked there. Lowering makes it one `Borrow` of the place
`a[from i, count n]`, as the MIR prints it: its last step, a `Subslice`,
names an entry of the body's `subslices`, which starts at `i` and counts
`n = j - i`, both `usize`s. The loan rules see one borrow of `a`, two-phase
`&mut` included, and a subslice may overlap any element. The start and count
sit in that side table, being rare, so no place step grows. Each end not
known at compile time is checked before that by `Check`s that lowering
writes: an end no wider than a `usize` is converted first (a negative one,
extended, is above any length), a wider one is compared in its own type
before it is cut down; a `..=` end is checked below the length before one is
added, and the ends against each other unless a start of 0 or an end at the
length makes them in order.

`&s[i..j]` of a slice `s` is the same `Slice`, of the slice place, and the
same `Borrow`, of `s.*[from i, count n]`: its elements, behind it. The
length is the slice's, loaded once before (field 1), so every end but a
start of 0 is checked when the program runs, even a constant one; the
checker rejects only constant ends that no slice can hold (negative, past
the target's `usize`) or that are out of order. `&mut` needs a `[&mut]`
source, as writing through a slice does.

`for v in s` over a slice is THIR's array loop: the slice is held in a
hidden variable (a `[&mut]` lent again, so `s` is usable after), the range
ends at its `len`, read once before the first pass, and each pass sets `v`
to `&items[i]` or `&mut items[i]`, as for a loop over a reference. The loop
lends the slice for all its passes, so its body cannot use `s`.
`[N]T{s...}` is one part of an `ArrayParts`, its elements copied; they must
copy, and a `[&mut]` variable is lent as a `[&]`, so it is usable after. Its
count is known only when the program runs, so `[_]T{s...}` is rejected.

A use (`load`, `move`, `borrow`, `address`, or a read or write of a place) not set on
every path is T0034 ("`x` is used after it was moved", with each place it was
moved) when the moved bit is on, else T0039 ("`x` is used before it is set").
A write through a reference only reads its variable. A `move` of a field, or
through a reference, is always T0035 ("cannot move a field out of its struct", or "cannot move out of
a reference or pointer"). A `store` to
a variable without `mut` that may already be set is T0007 ("cannot assign
twice to immutable `x`"). So `let x: T;` may be set once later even without
`mut`: check rejects other assignments to a variable without `mut` only when
its `let` has a value. Only a `let` without a value starts unset and only a
variable whose type moves can be moved, so the check is skipped for a function
where every variable starts set and copies (decided by the owner, 2026-10-17;
extended to moves 2026-10-23).

The loan check (T0037, `flow::loans::report_loan_errors`) rejects an access that a
reference still in use forbids. A loan is one `borrow`; it lasts until the
last use of a reference holding it (owner, 2026-10-23), found by liveness of
the variables and values of reference type. A `borrow` through a reference
(`&mut r.*`, the call reborrow) also holds `r`'s loans, so what `r` refers to
stays borrowed while the new reference is in use. A shared loan forbids
changing, moving, ending and `&mut` borrowing its place; a `&mut` loan
forbids every access; places overlap when one's steps start with the
other's, so other fields stay free. Lowering writes a `scope end` at a
block's `}`, and at a `break`, `continue` or `return` that leaves the block,
for each variable of that block, and one where each temporary ends; it writes no code (`InstructionKind::runs`) and is how a reference
that outlives its variable is caught. The variable state check forgets a
variable at its `scope end`, so a loop's next pass may set it again. Changing a variable, or its
scope ending, does not touch a loan reached through the reference stored
in it: `r = &mut b` while `s = &mut r.*` is in use is valid (owner,
2026-10-24). After it, `r.*` names `b`, so the loan of `r.*` ends there and
`s` keeps `a` borrowed through the loans it took from `r`: `r.* = 4` then
`s.* = 3` is valid, as rustc's kill of loans through an overwritten reference
(owner, 2026-10-29). The message names the variable: "`x` is changed while a
`&mut` reference to it is still in use" (events: read, changed, moved,
borrowed, borrowed as `&mut`, gone), a temporary as "a temporary value",
since its code may take several lines, with the help to keep it in a
variable so it lasts as long as the reference; with "reference made here" and
"reference used later here" (owner, 2026-10-24). A function with no
`borrow` is not walked. A `&mut` borrow whose only use is a call argument is
reserved until that call (two-phase, as the spec on `legacy` asks): until
then it acts as a shared loan, so `update(&mut x, read(&x))` and
`take(&mut x, x > 0)` are valid; when the call starts it is checked as a
`&mut` borrow against every reference still in use there, the call's other
arguments included, so `both(&mut x, &x)` is an error, with the note "a
`&mut` argument takes hold when the call starts".

A function may return a reference (owner, 2026-10-25). Its type's
`origins` are the parameters the result may point into: those `-> &T in a`
or `in (a, b)` names, else every reference parameter, or every `&mut` one
for a `&mut` result; the signature errors are T0016 (and T0002 for a name
that is no parameter). The loan check gives each reference parameter a
parameter loan, numbered after the borrows, at the entry `store` of its
argument; it conflicts with nothing in the body. A `return` uses its value,
and a returned reference may hold no loan of the function's own storage
(T0036, "returns a reference into `x`, which is gone once the function
returns") and only the parameter loans the origins name (T0016, with the
help "add `b` to `in`"); that check runs before a block's walk, so the
local is not reported again as gone. At a call, the result holds the loans
of the arguments at the callee's origins, so they stay borrowed while it
is in use, and a `&mut` argument there is not reserved: its loan outlives
the call. Callers read only the signature, never the callee's body.

The unreachable-code check (W0001, `flow::warn_unreachable_code`)
warns once at the first code of each stretch of blocks no path reaches, such
as code after a `return` ("this never runs"); the blocks that stretch leads to
are not warned about again. Reachability ignores constant values, as for the
missing-return check, and there is no warning that a condition is always true
or false: `if DEBUG` with a constant is how code is switched off (decided by
the owner, 2026-10-17).

The checker warns (W0018) at an inner block's `let` that hides an outer `mut`
local: the `let` was likely meant as an assignment, and the outer local is
unchanged. Shadowing an outer block's name is otherwise silent (decided by the
owner). A block declares each name once: a `let`, body `const` or parameter
reusing a name of its own block is T0003, and the first keeps the name. The
parameters are in the body's outermost block, as in Go, so a `let` there
cannot reuse one; named results can join that block later (owner, 2026-10-18).
These live in the checker, which knows the blocks. The blocks are the body's
scopes: THIR's `blocks` arena holds each with its parent, the function's
first, and each local names its block. Lowering copies them into MIR's
`debug_scopes` by the same ids, and stamps each instruction, terminator and
variable with its debug scope; only debug info reads them. The LLVM backend
makes a `DILexicalBlock` of each that declares a variable, under its nearest
such ancestor.

`peeper -dump [thir | mir | ll] <path>` writes the THIR, MIR and LLVM IR of every function
of a program without errors, or only the kind named, one file per module under
build/debug/dumps in the project folder or beside the file
(`myapp:deep/x` to `myapp/deep/x.mir`), like the old `-keep-gen` and rustc's
`-Zdump-mir`. The printers are `thir::Body::text` and `mir::Body::text`, beside
the data, and `llvm::Module::text` for `ll`. MIR tests are `x_test`
folders with `mode = "mir"` and an `expected.mir` file, as in rustc's
`tests/mir-opt`; `PEEPER_BLESS=1` rewrites the expected files.

## LLVM

`crates/backend/llvm` writes one module's LLVM text from its functions' MIR,
one walk per function (`peeper -dump ll` shows it):

- Each variable is an `alloca` in an `entry` block, with `llvm.dbg.declare`;
  each instruction gets a `!dbg` line from its span; each function a
  `DISubprogram`. A debugger shows variables by name.
- Symbols: a function's `#[extern("name")]`, else `module_path.name`, quoted.
- An `extern` function is C's, so it has no body, and every other function
  has one; `extern` on a function is the one attribute there is, and any
  other is an error (owner, 2026-10-29). Its integers pass as clang passes
  C's `_BitInt(N)` on the target (`Machine::c_abi`): extended to `int`, as
  themselves, as two `i64` limbs, or in memory (`sret`, `byval`), with x86-64
  System V's register count; `i128` passes as `__int128` (on Windows x64 too,
  owner, 2026-10-29). An integer wider than C has on the target (128 bits on
  AArch64) is an error.
- Integer division, remainder and shifts by a value not known at compile time
  check first and continue in a new block (`bb3.1`); a `phi` names the block
  where its incoming MIR block ends. Division by zero and a shift count
  outside 0 to width - 1 panic; signed `MIN / -1` is `MIN` and `MIN % -1` is
  0, as check folds them.
- A panic, a failed check or a written `panic "message"`, writes one string
  constant made at compile time (`panic: division by zero` or `panic:
  message`, then `  --> file:line:column`) through the prelude's `write` to the
  standard error and calls its `exit` with 101: no runtime function of its own.
- A slice is `%slice = type { ptr, iN }`, N the pointer's width, written at
  the top of every module, and passed as that one value, not kept in memory
  like an array, so a slice argument is never copied. At a place's step
  through a slice the walker loads both parts and checks the next index
  against the count when the program runs, even a constant one; a borrow of
  a slice builds both parts with `insertvalue`; a subslice step is a
  `getelementptr` index at its start, an `iN` checked before, and the slice
  takes its count. A debugger shows it as a
  struct with `ptr` and `len`. A slice spread in an array literal is
  counted from its length, one bit wider than the element, checked with the
  other parts before any element is written, and copied with one `memcpy`;
  that array is built in its own memory, never at the place it is stored to
  or `%result`, which the slice may view.
- `print` and `println` call the runtime's print functions, one per number,
  `bool`, `cstr` or pointer, with the text known at compile time (spaces,
  struct names, brackets) gathered into one string between them. An array
  prints through a private function per array type that loops over its
  elements, so the code does not grow with the length.

`peeper build [-o program] [path]` and `peeper run [path] [-- arguments...]`
work as the Go compiler's did, except that `run` takes the program's
arguments only after `--`. The entry file needs `fn main()` or `fn main()
-> i32` (M0006, which `compile::run` checks only when the CLI passes it the
entry module, for these two commands); the entry module gets a C
`main` that calls it and returns its result, or 0. `llvm::link::link_executable`
writes each module's text to a temporary folder, compiles each with `clang -c`
and links the objects with the runtime library (`<installation>/runtime/
<target>/libpeeper_rt_v1.a`, built with `rustc` as `runtime/runtime.toml` says (read by `llvm`'s build.rs) by `llvm::runtime`,
which `scripts/build.sh` runs, so only building the compiler needs `rustc`)
and `-lm`; the program is linked beside its output and renamed into place.
Clang is the installation's own, in
`<installation>/toolchains/native/bin` (`llvm::link::tool`), never ones on the
PATH; `scripts/setup.sh` puts the CI-built pack, or an installed LLVM 23, in
`build/toolchains/native`. `build` names the program after the project or file
in the current folder; `run` links to a temporary file, runs it and exits with
its code (a signal gives 128 plus its number). `-target <name>` checks and
builds for a row of `model::machine::MACHINES`, the host's row otherwise: each
row is a machine's name, clang triple, pointer bits and the alignment of `i64`
and `f64` in a struct (4 on `i686-linux`, 8 elsewhere), so a new target is a new
row. Clang gets `--target=<triple>` only for a target that is not the host, and
the runtime library of each is in `<installation>/runtime/<name>`;
`scripts/build.sh` builds the host's and `i686-linux`'s, the latter when the
32-bit C library is installed. Only the Linux rows are run here; a build for
another system needs that system's C library and linker, which the toolchain
does not have. The language server checks for the host only. Not brought back
yet: managed toolchains and sysroots for cross builds, linker response files,
`-keep-gen` (`-dump ll` covers it), and the manifest's `build = "program"`.

## Cost on the hot path

- Type equality: one integer comparison. Building a type: one interner lookup,
  none for primitives.
- Declaration tables: one vector index per ask.
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
- **decided** One rule, `Types::conversion(from, to)` in `model`, says how a
  value of one type becomes another: the same type, an implicit conversion,
  a reborrow, only with `as`, or never. Every place that needs a value of a
  type asks it; check builds the result (a `Convert` node, folded for a
  constant) and makes every error on a value's type in one function, which
  adds the `.*` help when the value is a reference to an accepted type.
  (Owner, 2026-10-24; the Go compiler's `CheckCompatibility` was the idea,
  without its per-site messages or its side table of conversions.)
- **decided** `x as T` converts what converts without `as`, and between any
  two number types and from `bool` to an integer (`true` is 1); nothing else
  (no struct, no `&T` to `&mut T`, no integer to `bool`: write `x != 0`). A
  narrower integer keeps the low bits; a float rounds to nearest, ties to
  even; a float becomes an integer cut toward zero, saturated at the ends
  of its range, NaN as 0 (Rust's rule, as LLVM's plain `fptosi` gives
  poison there). Folding and LLVM agree for every width
  (`crates/middle/eval/tests/llvm_parity.rs`): `llvm` writes LLVM's own
  `sitofp`/`uitofp` and `llvm.fpto{s,u}i.sat`, which need LLVM 23 or newer
  above 128 bits (LLVM 22 crashes on the one and gives a wrong finite float
  for the other, llvm/llvm-project#189054), so the bundled toolchain is LLVM
  23 or newer, whatever clang the system has. (Owner, 2026-10-24.) Peeper
  runs only its installation's toolchain (owner, 2026-10-28).
- **decided** A number literal takes the type cast to when it can, as it
  takes any expected type, so `300 as u8` is an error, not 44. (Owner,
  2026-10-24.)
- **decided** A `&mut T` is taken where a `&T` is expected by lending it
  again as a `&` (`&m.*`), never by moving it: the `&mut` stays usable after
  the shared reference's last use, and is not written through before it.
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
