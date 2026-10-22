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
  arithmetic in `model`; see "Constant evaluation" for where each one stops.
- **decided** Scheduling is per declaration, on demand.
- **decided** LLVM IR is written as text and compiled by clang.
- **decided** One `model` crate owns the program's data: the syntax tree, the
  THIR and MIR node types, the type model, definition ids, constant values
  with their arithmetic, and operator kinds. The crates that build them
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
            ├─► check ─► model, project   AST -> THIR and signatures; folding
            ├─► lower ─► model            THIR -> MIR; path checks; (later) interpreter
            └─► llvm ──► model            MIR -> LLVM text; clang
```

Every phase reads and writes its data in `model`; only `lower` reads THIR, and
`llvm` and the interpreter read MIR and signatures. Signatures (struct fields,
enum variants, function types) are `model` data, written by check.

| crate | owns |
|---|---|
| `model` | data only: `ast` (the syntax tree), `thir` (THIR nodes), `mir` (MIR graphs); `semantic_types` (`TypeId`, the type interner, the built-in types table); `constant` (constant values and their arithmetic); `DefId`; `BinaryOp`, `UnaryOp`; signatures |
| `check` | the checker: scopes, name lookup, building THIR, writing signatures; the query tables (`Queries`) |
| `lower` | lowering from THIR; path checks; later the interpreter |
| `llvm` | LLVM text for a module; running clang and the linker |
| `compile` | a compile: walks the modules in order, reuses unchanged ones, lowers each body (the `build` of `overall-design.md`, renamed) |

A `project::Module` holds only what loading read and never changes. What a
compile finds (each module's `Scope`, types, query results, diagnostics) is
kept apart, in `check::Queries`, in vectors indexed by `ModuleId`, and
everything links by id. `Queries::new` collects every module's scope, after
every module is loaded. So its queries take `&mut self` and nothing needs a `RefCell`:
holding a type across a query is a compile error, not a run-time panic. A
scope holds declarations; a name it
does not hold is looked up in its parent's, so a module's scope has the
prelude's as its parent. A name stands for a `model::Symbol`: a module,
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

One owner for the meaning of every operation: `model`'s value functions, on top
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
   `crates/model/tests/llvm_parity.rs` runs every operator on widths 1 to 1000 in
   `lli` and in `model` and requires equal bits.

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
function(d)    the function type of function or method d
constant(d)    the type and value of module constant d (without a written
               type, its type is its value's)
body(d)        THIR of function d (needs the function types it calls)
mir(d)         MIR of function d (needs body(d)); later also comptime calls
```

- Queries cover module-scope declarations (`DefId`), one per kind, as
  `Symbol` names them. A body's own names (`Symbol::Local`: a variable or a
  `const`) are checked in place with the body, never asked for. Type
  declarations are no query until something asks for a type.
- Each query's result is written once and never changed. The compilation holds
  a table of them per module, indexed by the declaration's place among its
  top-level declarations (`DefId`), so a body edit does not move it.
- A query asked for while it is running is a cycle (`const A = B; const B = A;`)
  and is reported once (T0013), at the declaration asked for again; the other
  declarations of the cycle become silently invalid.
- `check::Queries` holds the query tables and answers each query once; the
  checker calls it directly. The `compile` crate runs a compile: it walks the
  modules, reuses unchanged ones and lowers each body. `Queries::new`
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
- Comptime later: check asks `mir(f)` and runs it in the interpreter, without
  depending on the `lower` crate. How it reaches `lower` is open, since the
  query tables now live in `check`: a callback that `compile` passes in, or
  another owner for the tables.

## Reuse between compiles and the language server

Owner's choice (2026-10-17): reuse per module, as Go's and Zig's caches do, not
per declaration. `compile::run` takes the previous compile's `Results` and
gives new ones; the command line passes `Results::default()`, so a cold
compile pays nothing extra. A module is not checked again when its text is
unchanged and the interface of each module it imports (the prelude included)
is unchanged: it keeps its diagnostics. A module's interface is a hash of what
other modules see: its imports, and each declaration's kind, name, and type or
constant value, types hashed by structure. Bodies are not part of it.

Only the source, the interface hash and the diagnostics are kept between
compiles. Ids, types, THIR and MIR are rebuilt, since their numbering shifts
with every edit. Spans stay absolute: a module is redone whole, and its
diagnostics only point into its own file. A diagnostic pointing into another
module's file would break reuse of it after an edit there.

Measured on a generated 20k-function program (100-function modules): a full
check costs about 160 ms; after a body edit the next compile takes about 9 ms,
and after a constant's value changes about 10 ms (two modules). Collecting
every module's names again is most of what remains (about 7 ms).

`peeper lsp` (crate `lsp`, the only user of `lsp-server`, `lsp-types` and
`serde_json`) keeps the text of open files, which `project::load` reads before
the disk, and one `compile::Results` per program. A change is checked 200 ms after the
last keystroke; opening, saving and closing a file are checked at once.

Go to definition and hover (owner's choice, 2026-10-18: the checker records
names, not a second resolver). While checking a body, the checker appends a
`thir::Name` for each name written in it (in `declare`, `resolve`, which
looks every written name up, and module paths). Its `Symbol` is what the
checker resolved, not a copy: a local or body `const` by its id (the body's
arenas hold its span and type, so body `const`s get an arena of their own,
`constants`), else a module-level declaration or module. A body with an error
keeps its THIR, marked `has_errors` (as rustc marks typeck results
`tainted_by_errors`), so names work while a function is broken; `lower::lower`
gives no MIR for it, and `use_function` gets its THIR alone. `compile` runs the
phases one by one: `ModuleChecker::check_body` gives the THIR, `compile`'s `lower`
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
  unary operation, `widen`, `call`, `phi`. Operands are a constant, an argument
  or an earlier instruction's value, never a variable. A constant operand is an
  index into the body's constants table (each with its type), so an operand stays 8 bytes. Every
  instruction keeps the span of the expression it came from: LLVM's `!dbg`
  line, and where an error points.
- **Terminators.** Each block ends in one: `br` (goto), `condbr` (with the span
  of its condition), `ret` for a written `return`, or `fall off` at the closing
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
and the future interpreter both read MIR; nothing below MIR reads THIR.

The missing-return check (T0017, `lower::path_checks`) reports a function whose
`fall off` block is reachable. It walks back from that block through blocks
with no instructions; on each path, the first branch met names its condition
("if this is false, nothing is returned"), and the first block with
instructions names its last one ("this path ends without returning"). When the
end block itself has instructions, only the end is named, since one `return`
there fixes every path. `ModuleChecker::check_body` gives no THIR for a body whose check
reported an error, so such a function is neither lowered nor path-checked.

The initialization check (`lower::path_checks::report_initialization_errors`) is forward dataflow
with two bits per variable at each block's start: set on every path to it
("and" at joins) and set on some path ("or"). A `load` not set on every path is
T0039 ("`x` is used before it is set"); a `store` to a variable without `mut`
that may already be set is T0007 ("cannot assign twice to immutable `x`"). So
`let x: T;` may be set once later even without `mut`: check rejects other
assignments to a variable without `mut` only when its `let` has a value. Only a
`let` without a value can be unset, so the check is skipped for a function
that has none (decided by the owner, 2026-10-17).

The unreachable-code check (W0001, `lower::path_checks::warn_unreachable_code`)
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
`scopes` by the same ids, and the LLVM backend makes a `DILexicalBlock` of
each that declares a variable, under its nearest such ancestor.

`peeper -dump [thir | mir | ll] <path>` writes the THIR, MIR and LLVM IR of every function
of a program without errors, or only the kind named, one file per module under
build/debug/dumps in the project folder or beside the file
(`myapp:deep/x` to `myapp/deep/x.mir`), like the old `-keep-gen` and rustc's
`-Zdump-mir`. The printers are `thir::Body::text` and `mir::Body::text`, beside
the data, and `llvm::Module::text` for `ll`. MIR tests are `x_test`
folders with `mode = "mir"` and an `expected.mir` file, as in rustc's
`tests/mir-opt`; `PEEPER_BLESS=1` rewrites the expected files.

## LLVM

`crates/llvm` writes one module's LLVM text from its functions' MIR, one walk
per function (`peeper -dump ll` shows it):

- Each variable is an `alloca` in an `entry` block, with `llvm.dbg.declare`;
  each instruction gets a `!dbg` line from its span; each function a
  `DISubprogram`. A debugger shows variables by name.
- Symbols: a function's `#[extern("name")]`, else `module_path.name`, quoted.
- Integer division, remainder and shifts by a value not known at compile time
  check first and continue in a new block (`bb3.1`); a `phi` names the block
  where its incoming MIR block ends. Division by zero and a shift count
  outside 0 to width - 1 panic; signed `MIN / -1` is `MIN` and `MIN % -1` is
  0, as check folds them.
- A panic writes one string constant made at compile time (`panic: division
  by zero` and `  --> file:line:column`) through the prelude's `write` to the
  standard error and calls its `exit` with 101: no runtime function of its own.
- Output goes through the prelude's `write` with a `cstr` (`c"..."`); there
  is no `print` yet (owner, 2026-10-19).

`peeper build [-o program] [path]` and `peeper run [path] [arguments...]`
work as the Go compiler's did. The entry file needs `fn main()` or `fn main()
-> i32` (M0006, checked only by these two commands); the entry module gets a C
`main` that calls it and returns its result, or 0. `llvm::link::link_executable`
writes each module's text to a temporary folder, compiles each with `clang -c`
and links the objects with the runtime library (`<installation>/runtime/
libpeeper_rt_v1.a`, built as `runtime/runtime.toml` says (read by `llvm`'s build.rs) by `llvm::runtime`,
which `scripts/build.sh` runs)
and `-lm`; the program is linked beside its output and renamed into place.
Clang is the one on the PATH until peeper ships its toolchain. `build` names
the program after the project or file in the current folder; `run` links to a
temporary file, runs it and exits with its code (a signal gives 128 plus its
number). Not brought back yet: target flags (`-target-os`, `-m32`), managed
toolchain profiles, linker response files, `-keep-gen` (`-dump ll` covers
it), and the manifest's `build = "program"`.

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
