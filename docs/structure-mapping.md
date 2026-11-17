# Structure mapping: from today's crates to `kernel/` and `features/`

Status: final, agreed by Fuad, GPT and Claude. Nothing has moved yet.

Each line says what kind of change it is:

- **move**: a file or folder changes path; its content does not change.
- **extract**: code leaves a file for another; behavior does not change.
- **API**: a type, node or trait changes shape; callers change with it.
- **later**: not part of this restructuring; listed so its place is known.

## Rules

1. No feature depends on another; a feature depends only on the kernel.
2. A concept used by many features lives in the kernel once, never in the
   kernel and in a feature.
3. The kernel owns shared machinery, the operations on the closed checked
   type shapes, and anything that only wraps an operation the core already
   owns. A feature parses and validates its own syntax and builds a core
   shape.
4. Phase crates stay separate. Cargo package names do not change.
5. Production dependencies follow the phase direction. A test may use a
   higher crate (through `[dev-dependencies]`) to build a real program.
6. No new feature hooks, iteration-plan services, extra crates or
   duplicated machinery come with this restructuring.

## Step 1: crates (move only)

| Today | New | Package |
| --- | --- | --- |
| `crates/frontend/token` | `crates/kernel/syntax/token` | `token` |
| `crates/frontend/lexer` | `crates/kernel/syntax/lexer` | `lexer` |
| `crates/core/syntax` | `crates/kernel/syntax/tree` | `syntax` |
| `crates/frontend/parser` | `crates/kernel/syntax/parser` | `parser` |
| `crates/middle/check` | `crates/kernel/semantic` | `check` |
| `crates/middle/model` | `crates/kernel/model` | `model` |
| `crates/middle/lower` | `crates/kernel/lower` | `lower` |
| `crates/middle/flow` | `crates/kernel/flow` | `flow` |
| `crates/middle/eval` | `crates/kernel/eval` | `eval` |
| `crates/frontend/project` | `crates/driver/project` | `project` |

Unchanged: `crates/base/*`, `crates/features`, `crates/backend/llvm`,
`crates/driver/{compile, lsp, browser, peeper, test_programs}`,
`crates/tools/toolchain`. `crates/frontend`, `crates/core` and
`crates/middle` are empty afterwards and go.

What step 1 edits besides `git mv` (found by searching every file for the
old paths):

- `Cargo.toml`: the `members` list and the 11 `path =` entries of
  `[workspace.dependencies]`. Crates name each other with
  `workspace = true`, so no crate manifest changes.
- `scripts/check.sh` lines 39-55: the model, eval, flow and feature-boundary
  checks name `crates/middle/...`.
- `docs/feature-front.md` (lines 466-481, the crate list) and
  `docs/architecture.md`: current architecture, updated.
- `docs/feature-free-mir.md`, `docs/flow-crate.md`, `docs/ir-design.md`:
  design notes of their time; their old paths stay, with one line saying
  where the crates are now.
- No Rust file names these paths. Every `CARGO_MANIFEST_DIR` and `#[path]`
  that climbs folders is in a crate that does not move or moves at the same
  depth (`check` and `project` stay three deep), so none changes.
- CI workflows name only `crates/tools/toolchain` and
  `crates/driver/browser`, which do not move.

Done when: the build and the full `scripts/check.sh` pass, with no change in
behavior or in who owns what.

## Step 2: four features into the kernel (API + extract)

| From | Syntax becomes (API) | Parser rule goes to | Checking goes to |
| --- | --- | --- | --- |
| `features/src/bools.rs` | `ast::Expr::Bool(bool)` | `parser/src/literals.rs` | `check/src/expression/literals.rs` |
| `features/src/text.rs` | `ast::Expr::String(StringKind)` | `parser/src/literals.rs` | `check/src/expression/literals.rs` |
| `features/src/casts.rs` | `ast::Expr::Cast { value, ty }` | `parser/src/expr.rs` (infix `as`) | `check/src/expression/operators.rs` |
| `features/src/panics.rs` | `ast::Stmt::Panic(Id<Expr>)` | `parser/src/stmt.rs` | `check/src/statement.rs` (THIR `Stmt::Panic` exists) |

Each feature's `tests.rs` moves with its code. Each feature's node, its
registration and its `ExprNode`/`StmtNode` impl leave `features/src/lib.rs`.

API: `Language::string_kind` is removed. Its places:
`syntax/src/ast.rs:56` (the trait method), `features/src/lib.rs:52` and
`:197` (the impls), `check/src/testing.rs:58` and `parser/src/tests.rs:42`
(test stubs). Its two users match `ast::Expr::String` instead:
`model/src/program.rs:116` (an `extern`'s name) and the panic message check.

## Step 3: inside the checker (`kernel/semantic/src`)

| Today | Kind | New | What |
| --- | --- | --- | --- |
| `lib.rs` | stays | `lib.rs` | `Checker`, `check_once`, name resolution, reporting |
| `lib.rs`: `record_copy` | extract | `generics/copies.rs` | records a copy in the copy table |
| `builder.rs`, `scope.rs`, `fold.rs`, `fold/*`, `testing.rs` | stay | same | |
| `function.rs` | move | `functions/bodies.rs` | a body to checked MIR, `evaluate_call` |
| `builtin.rs` | move | `functions/builtin.rs` | `#[builtin]` functions |
| `builtin.rs`: `unsupported_type` | extract | `declaration.rs` | built-in types not handled yet, read only where type names resolve |
| `declaration.rs`: `FunctionKind`, `check_function`, `is_builtin_declared`, `signature_type`, `result_origins`, `resolve_signature_type` | extract | `functions.rs` | a function's type |
| `function.rs`: `function_declaration` | extract | `model::program`, beside `declaration` | a function as written, which `functions`, `generics`, `conformance` and `statement` look up |
| `declaration.rs`: `implementation`, `method_type` | extract | `conformance.rs` | whether a type implements an interface; receiver substitution; the cached `ImplementationId` |
| `declaration.rs`: `resolve_type_argument`, `type_argument`, `first_copy_parameter`, `written_inside`, `find_type_argument`, `names_type_parameter` | extract | `generics.rs` | finding and writing type arguments |
| `declaration.rs`: the rest (`check_attributes` for every declaration, type declarations, constants, resolving written types, `is_interface`, `reference_type`, `array_type`, `range_type`, ...) | stays | `declaration.rs` | about 670 of its 1382 lines leave |
| `copies.rs` | move | `generics/copies.rs` | the one copy table |
| `constraints.rs` | move | `generics/constraints.rs` | `where`: both interface constraints (through `conformance.rs`) and predicate functions run at compile time; neither form changes |
| `expression.rs`, `expression/{borrows, literals, operators, places}.rs` | stay | same | |
| `expression/calls.rs`: `check_call`, `call_found`, `function_called`, `method`, `takes_self`, `interface_method` | move (file renamed) | `functions/calls.rs` | function, method and interface method calls |
| `expression/calls.rs`: `check_symbol_value`, `constant_expr` | extract | `expression/names.rs` | a name or path used as a value |
| `expression/calls.rs`: `written_type_argument`, `copy_called` | extract | `generics.rs` | type arguments at a call, the copy a call makes |
| `statement.rs`, `statement/assignments.rs`, `statement/loops.rs` | stay | same | blocks, `if`, `for`, `return`, `break`, `continue`, `panic` |
| `statement/declarations.rs` | move | `locals.rs` | `let`, `const` in a body, `let .{ }`; `statement.rs` declares the module and calls it as before |

`interface_method`, interface conversions (`expression.rs`) and `where` stay
in the kernel and call `conformance.rs`; none imports `features`.
`conformance.rs` still reads some source declarations as well as checked
types; that stays as it is for this restructuring.

The signatures and the type arguments sit in the module files
`functions.rs` and `generics.rs`, the way `expression.rs` and
`statement.rs` hold their main code beside their submodules. That keeps
each free function two levels deep for its callers
(`generics::first_copy_parameter`, `model::program::function_declaration`).
`argument_count_message` sits in `lib.rs`, since calls and copies both
report counts with it; with `function_declaration` in `model`, `generics`
needs nothing from `functions`, and `functions` depends on `generics`
only.

Done when: the full `scripts/check.sh` passes and no fixture output
changes. Conformance is covered by the marked test programs (a missing
method and a wrong signature in `interfaces/negative_interfaces.peep`, a
`const` method in `negative_const_parameters.peep`, a false `where` in
`negative_where_conditions.peep`, the passing cases in `run_interfaces`);
the checker's own tests run in `Bare`, which has no interfaces.

## Step 4: the feature-boundary check (`scripts/check.sh`)

The check that a feature's file names no other feature also refuses
`super::<other feature>` and paths into another feature's nested module.

## Step 5 (later, with enums): patterns

| Today | Kind | New |
| --- | --- | --- |
| `features/src/enums.rs`: `match_statement`, `match_arm` | later | `parser/src/stmt.rs`; checked in `check/src/statement/` |
| `features/src/enums.rs`: `case_test` (`is`) | later | `parser/src/expr.rs`; checked in `check/src/expression/` |
| `features/src/enums.rs`: `case_pattern`, `payload_pattern` | later | stay in `features/enums` |
| `parser/src/pattern.rs`: `struct_pattern` | later | a general `parse_pattern` in the parser; the `.{ }` field pattern moves to `features/structs` through a small parser API, not by opening parser internals |
| (new) | later | `check/src/patterns/{checked, coverage, decision}.rs`: the pattern engine |

## Step 6 (separate job)

`TypeId::INVALID` meant "error reported", "no type for `T` yet" and
"copy being checked". The copy being checked is now its own state:
`Copied::ty` is `None` while the copy is checked. "No type for `T` yet"
stays `INVALID`: every place that meets it stays silent, as for a reported
error, so a second marker would have no reader. Its real split, an abstract
type that checks a generic body once against its `T`, changes what an
uncalled generic function reports, and waits for a language decision.

## Features after the restructuring

`features/src`: `arrays`, `chars`, `enums`, `interfaces`, `optionals`,
`pipes`, `printing`, `ranges`, `raw_pointers`, `structs`, each one
`name.rs` and `name/tests.rs`, plus `lib.rs` (the list) and `testing.rs`.
