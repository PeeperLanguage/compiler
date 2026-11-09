# Feature interface demo

This is a small subset of Peeper built on the feature interface, to test
whether the design is worth taking. It runs the real compiler's phases in
the real order: parse, check into THIR, lower to MIR, path checks, clean
up, then interpret or write LLVM and build with clang. The interpreter runs
MIR on a byte array, so a wrong size or offset gives a wrong answer; every
program a test runs is also built natively, and both must agree. It is its
own cargo workspace, not part of the compiler.

```text
cargo test                                         28 tests, each run on an 8- and a 4-byte target; 11 paused
cargo run -p pipeline -- file.peep                 prints what `main` returns, run on the interpreter
            ... file.peep --native                 the same, built by clang and run
            ... file.peep --mir | --ll             the MIR after cleanup, or the LLVM text
cargo run --release --example parse_speed -p pipeline
```

## The pipeline

```text
parse      gateway/src/syntax.rs     one engine; each feature brings rules
check      gateway/src/check.rs      names, scopes, types, conversions -> THIR
           gateway/src/control.rs    if/else, for, break, continue, return
lower      gateway/src/lower.rs      THIR -> MIR: places, loads, moves, scope ends
path       flow/src/lib.rs           missing return (T0017), unreachable code (W0001)
checks     flow/src/variable_state.rs  unset, moved, set twice (T0039, T0034, T0035, T0007)
           flow/src/loans.rs         references still in use (T0037, T0036)
cleanup    flow/src/cleanup.rs       drops scope ends, jump-only and unreached blocks
run        eval/src/lib.rs           the MIR interpreter
native     llvm/src/lib.rs           MIR -> LLVM text; clang builds and runs it
dump       pipeline/src/dump.rs      MIR as text, in the real compiler's format
```

Not built yet: constants, imports and parallel parsing (later steps).

## The shape

```text
model         Span, TypeId, Facts, the MIR: the data every phase after checking reads
   ^
gateway       the core: parsing engine, checker, THIR, control flow, lowering
   ^
features/*    one crate each: bools, integers, operators, bindings, references,
              functions, members, structs
   ^
language      the one list of features: keyword and parse table, type names, code check
   ^
pipeline      the driver, the MIR dump, the tests

flow, eval,   depend on `model` only
llvm
```

`flow`, `eval` and `llvm` depend on `model` only, so they cannot name a
feature: the build proves it. They know a type only by
its facts (`model/src/facts.rs`: size, alignment, carrier, whether it
copies, how many loan holders it is).

No phase matches on which feature made a node or a type:

- **A syntax node holds its check**, a plain `fn` that turns it into THIR
  (`Kind` in `gateway/src/tree.rs`).
- **A THIR node holds its kind** (`ThirKind` in `gateway/src/thir.rs`): how
  to lower its value and, if it names storage, how to lower that place and
  whether it may be set through it.
- **A type holds its kind** (`TypeKind` in `gateway/src/types.rs`): its
  facts, text, conversions into it, operators, fields, and what it points
  at.
- Every field of a kind is required. A kind that leaves one out does not
  build (`error[E0063]: missing field ... in initializer of TypeKind`).

Also checked when the compiler is built (real output):

```text
error[E0080]: evaluation panicked: two features claim the same tokens in the same position
error[E0080]: evaluation panicked: two features claim the same keyword
error[E0080]: evaluation panicked: a rule starts with a word no feature lists as a keyword
error[E0080]: evaluation panicked: two features own the same diagnostic code
```

## What the core owns, and why

- **Control flow** (`if`, `for`, `break`, `continue`, `return`). Every phase
  after checking follows the blocks these make, and a jump must end the
  scopes it leaves, so they are part of the pipeline. This reverses the
  earlier plan, which made `for` and `return` features.
- **Places and reading them.** A read of a place is a `load` when its type
  copies and a `move` when it does not, decided once in
  `Lowering::read` from the type's facts.
- **The dereference** (`thir::DEREF`). Both `.*` (references) and a field
  through a reference (members) need it, so neither feature owns it; the
  reference's type kind says what it points at (`TypeKind::pointee`).
- **Scope ends.** A block's `}`, a `break`, a `continue` and a `return`
  each end the scopes they leave, for the path checks to read.
- **The path checks.** They read only MIR and facts, so no feature has
  code in them.

Each step the source leaves out is a THIR node of its own: a conversion, the
dereference before a field, the `&` borrow of a method's receiver, and a new
borrow of a `&mut` argument held in a variable (so the variable is not
moved).

## Checked against the real compiler

`mir_matches_the_real_compiler` compiles the real fixture
`x_test/mir_references/src/main.peep` and compares the dump with its
`expected.mir`. They are the same, byte for byte: every instruction id,
`borrow mut r.1.*` for the reborrows, `load point.0.*[1]` through a
reference, `borrow p.0[1]` of a field.

`path_checks_match_the_real_compiler` gives 19 programs to both
compilers: each gets the same code, message, line and column as
`peeper check`. The real errors also have labels, notes and help, which the
demo leaves out.

## What the tests show

| Test | What it shows |
| --- | --- |
| `integer_arithmetic` | integers, operators, bindings, functions |
| `comparison_gives_bool` | `<` gives the core's `bool`; mismatch refused |
| `method_on_a_struct_value` | a method's receiver is borrowed in THIR |
| `if_else_chooses_one_block` | `if`, `else if`, `else`; each branch runs on to the join |
| `loops_with_break_and_continue` | `for cond {}`, `for {}`, `break`, `continue` |
| `jump_and_condition_refusals` | `break` outside a loop, `return;` in an `i32` function, a non-`bool` condition |
| `set_through_a_mutable_reference` | `&mut n`, `n.* = 3` |
| `mutable_only_through_mut` | `&mut` of a non-`mut` variable, setting through `&`, `.*` of a non-reference |
| `let_without_a_value` | `let x: i32; x = 4;`, and `let x;` refused |
| `struct_moves_into_a_call` | a struct passed by value; literal fields in any order |
| `path_checks_match_the_real_compiler` | 19 refused programs, each with the real compiler's error |
| `path_checks_accept_what_the_real_compiler_accepts` | two-phase `&mut` arguments, a repointed reference, a reference's last use, a loop's `let`, a variable set on each path |
| `unreachable_code_is_a_warning` | W0001, and the program still runs |
| `wide_constants_and_wrapping` | constants past a byte, `i64`, `i32` wrapping, the same natively |
| `void_main_and_a_function_value` | a `main` giving nothing, a function's name as a value |
| `empty_struct_argument` | a struct with no fields, passed between other arguments |

Every test that runs a program (`value` in `pipeline/src/tests.rs`) runs it
four times: the interpreter and a native build, each with 8-byte pointers
(this machine) and 4-byte ones (i686-linux).
| `mutable_reference_and_struct_move` | reading a `&mut` or a struct is a `move`; aggregate parts in field order |
| `mir_matches_the_real_compiler` | the dump above, and the run gives 5 |
| `loop_mir_after_cleanup` | the whole MIR of a loop after cleanup |
| `if_else_mir_block_order` | blocks numbered as the real compiler does: then, else, join |
| `self_only_first_in_a_method`, `method_without_self_is_not_a_method` | receivers |
| `every_field_given_once`, `struct_cannot_hold_itself` | structs |
| `later_binding_hides_earlier`, `only_a_place_is_set`, `keyword_is_not_a_name` | bindings, keywords |
| `refused_value_is_refused_once` | the error type adds no second error |

The tests can fail. Each of these breaks at least one test: no reborrow,
no scope-end removal, a dereference that is ignored, a `&mut` that copies,
reads that always `load`, an `if` branch that jumps to the wrong block,
aggregate parts left in written order, and no jump skipping in cleanup.
In the path checks: "set on every path" joined with "or", no "gone" at a
scope end, no two-phase arguments, a store that ends no loan, a field's
holders taken from the struct's first, a `&mut` call argument reserved
although the result holds it, a call result holding no loans, a scope end
that does not forget a loop's `let`, a move that does not count, and the
`return` errors checked after the walk. In the LLVM writer: `add` written
as `sub`, field offsets left out (in a literal or a place), half of each
copy, the 4-byte target built as 8-byte, a comparison sign-extended,
constants cut to a byte, arguments in the wrong order, and an empty value
kept in a function's parameters or in a call's arguments but not both.

Two rules no test can break, as no demo program shows them: the scope ends
a `return` emits (a "gone" there is always reported as T0036 first) and the
returned value's holders being in use at the `return` (the value is always
made in the `return`'s block). The real check has both; they are kept.

## Paused, not deleted

Arrays, slices, ranges, interfaces, `for ... in` loops and enums are still on
disk under `features/`, written against the old interface. They are left out
of the workspace (`exclude` in `Cargo.toml`) and of `language::FEATURES`.
Their 11 tests are `#[ignore = "waits for ..."]`.

Removed from the interface with them, to come back when they are ported:

- `TypeKind::iterate` (how `for ... in` walks a value);
- `Member::Dynamic` (an interface's method, called through its table);
- the `Answer` store: THIR nodes now hold what checking found;
- method tables in the MIR program.

## Speed

`parse_speed` builds the real compiler's `parser` and `source` crates as
dev-dependencies, parses one generated text that both accept, and keeps
the best of seven runs:

```text
text         6.6 MB
demo parser  84.6 ms
real parser  100.2 ms
```

This shows that splitting the parser by feature is not slower. It does not
show it is faster: the real parser also recovers from errors and builds
richer nodes. Checking, lowering and the other phases are not timed yet.

## Shortcuts

Each is marked `shortcut:` in the code where it is:

- a function is called only by its name, not through a variable;
- a method always takes `&self`; values in memory are not returned;
- the interpreter keeps each value it copies until the function returns;
- a `&mut T` is not accepted where a `&T` is expected;
- what a call returns holds the loans of all its arguments, as a function
  does not yet say which it may point into (`in`);
- `pipeline` prints `main`'s bits as an unsigned number;
- a native build takes 8-byte pointers to mean this machine and 4-byte ones
  to mean i686-linux, which holds on the x86-64 Linux the demo is built on.

## What "worth taking" will be judged on

- `flow`, `eval` and the LLVM writer name no feature (their crates depend
  on `model` only);
- copy or move is decided once, from `Facts::copies`;
- the core emits the scope ends; the path checks need no code per feature;
- the interpreter and native runs agree (they do, on both targets, for
  every program the tests run);
- every phase is timed against the real compiler;
- a list of the MIR instructions that exist for one feature only (today:
  `Aggregate`, made by struct literals alone);
- what the path checks needed beyond facts. So far: which loan holders a
  field is (now on `Projection::Field`, filled by the struct kind); the
  borrow's noun, "reference" or "slice" (always "reference" until slices
  return: a type will have to give the word); and which arguments a
  returned reference may point into (a shortcut until `in` exists);
- what the LLVM writer needed beyond facts. So far: how many parameters a
  function has (now `Body::parameters`, as in the real MIR). Facts do not
  tell a reference from an `isize`: both are one register of pointer size.
  The writer carries both as integers, turns a borrowed address into one
  (`ptrtoint`) and loads it back as a pointer at a dereference. That is
  valid and right, but LLVM's optimizer sees less than with `ptr`; a fact
  saying "this register is an address" would remove it. And `<` is always
  `icmp slt`, as every demo integer is signed: unsigned integers will need
  the operator or a fact to say which.
