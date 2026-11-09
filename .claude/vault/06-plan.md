# Plan

`main` is at `2c498440` (2026-11-06): text views (`&str`, `&cstr`) and
slices (`&[]T`) merged. Open branch: `feature/size-of`, holding the
interface, allocator and type parameter work below, in PR #182. Order of work
(owner, 2026-11-06, `allocator.localplan.md` ORDER OF WORK):

1. [x] Borrowed interfaces: `&Shape`, `&mut Shape`, method tables
   (60a3dbed, then b435b7ff for the `ref` style).
2. [-] A free hook: dropped for now (owner, 2026-11-06). Memory taken by
   hand with `Allocate` is freed by hand with `Free`; memory the compiler
   takes itself, the compiler frees. `defer` or destructors come later, if
   a real program needs them. What was kept: the `free` keyword removed,
   `Malloc` renamed `Allocate` (9bb5d681).
3. [x] An `Allocator` interface with `Heap`, `Arena`, `Pool` and `Stack`
   (owner, 2026-11-06: before `String`, which takes its memory from one),
   and `rawptr + usize` for them (d91d5b0f, 9920cf40). Named
   `core:allocator` then, `core:memory` since 5f548a56. The Website
   follows with item 6's.
4. [-] `String`: skipped (owner, 2026-11-06). When it comes back: it
   takes a `&mut Allocator` on each call that allocates or frees, since
   structs cannot hold references yet.
5. [~] Owned `Shape` (owner, 2026-11-08, `owned-interfaces.localplan.md`):
   made by a plain move, `let s: Shape = square;`; it lives as long as the
   struct it took. No heap `Shape`: like an integer, it lives on the
   stack (owner, 2026-11-08).
   - [x] Step 1: stack data, on `feature/owned-interfaces` (stacked on PR
     #185) (31eb13d3) (worklog, "Owned interfaces, step 1").
   - [-] Functions that make a `Shape` (one struct per function): built,
     then removed, not committed (owner, 2026-11-09; worklog, "Owned
     interfaces, step 2").
   - [x] Every temporary of a `let`'s or an assignment's value kept as
     long as the variable it sets (1193d2c7) (worklog, "Temporaries kept as
     long as their holder").
   - [x] Step 3: structs, arrays and slices holding a `Shape` (worklog,
     "Owned interfaces, step 3").
   - [ ] The Website, once the feature is complete.
6. [~] Type parameters (owner, 2026-11-06, `type-parameters.localplan.md`):
   a `const T: type` parameter takes a type known when compiling, and each
   type gets its own copy of the function; a plain `T: type` is refused for
   now; `<T>` generics stay. Any type works as a type argument.
   - [x] Step 1: `size_of(T)` and `align_of(T)`, `usize` constants, on
     `feature/size-of` (stacked on `feature/allocator-interface`)
     (8560886a).
   - [x] Step 2: `core:allocator` renamed `core:memory`, with `Realloc`
     (5f548a56).
   - [x] Step 3: `const` parameters and their copies: a type
     (`const T: type`) or a value known when compiling (`const n: i64`,
     owner 2026-11-07), one copy per type or value; `New` and `Replace`
     in `core:memory` (ee867ac0). A `const T: type` argument is always
     written at the call; working it out from the arguments waits for
     `<T>` generics (owner, 2026-11-07).
   - [x] Step 4: bundled libraries are projects, `build = "lib"` in
     `peeper.toml` (6f56683d; built on `feature/library-manifest`).
   - [x] Fixes from the two reviews of the branch and its Windows CI run
     (95c9e838, eb1b80ad; worklog, "Review fixes after the PR").
   - [x] Untangling pass on `refactor/untangle` (0e1edf53; worklog,
     "Untangling the codebase").
   - [ ] The Website (`peeper.toml`'s `build`, `core:memory`, `size_of`,
     `const` parameters, a new wasm build), once the feature is complete.

7. [~] References in arrays and slices, and loans per holder for structs
   (owner, 2026-11-08, `arrays-of-references.localplan.md`), on
   `feature/reference-arrays`. Accept what Peeper proves safe
   though Rust refuses it; refuse only real hazards (owner, 2026-11-08).
   - [x] Step 1: a struct field read uses only its own loans; a store to a
     field replaces them (worklog, "Loans per holder").
   - [x] Step 2: `[N]&T`, `&[]&T`, `[2]&Shape`, slices of slices,
     references to references and to slices; one set of loans per element
     (worklog, "References in arrays and slices").
   - [ ] The Website, once the feature is complete.

Later: lifetimes of references held in structs; enums and `match` with
`?T` (put off by the owner 2026-10-30); generics.

Left for later from methods (`methods.localplan.md`): `Self` written as a
type, functions of types other than structs.

## Waiting for the owner

- How a compile-time run counts steps: it counts each MIR instruction, so
  index checks use up its 1,000,000 steps sooner (a constant of 48,000
  passes over `a[i]` no longer fits; worklog, "Index checks in lowering").
- The open findings below: each is left as it is until the owner decides.
- Type parameters: whether `size_of` keeps its name, and whether a
  function with `const` parameters gets its body checked when nothing
  calls it (today only copies are checked, so a library's `New` or
  `Replace` is checked only once a program calls it).
- The `feature/library-manifest` worktree: its work is in 6f56683d; remove
  it only when the owner says so.
- Compile time: the `const` parameter step is about 0.9% slower on the
  benchmark, spread over many small places (worklog).

## Open findings (from `fix/loans-memory-growth`, 2026-11-01; not rechecked since)

| # | Where | Finding | Cost now | Proposed fix and its cost |
| --- | --- | --- | --- | --- |
| F1 | `check/src/lib.rs` `ModuleChecks` | Three tables indexed alike per declaration (`declared_types`, `constants`, `bodies`); each slot is used in one table only. RULES §5.1 discourages parallel lists. | 3 slots per declaration, 2 unused | One `Vec` of a per-declaration enum. `docs/ir-design.md` decided "one table per language category", so this is the owner's call. |
| F3 | `check/src/function.rs` `evaluate_call` | Each compile-time call walks every function it may reach, even when all are built. | C × R visits (200 calls × 4,000 functions: 0.17 s) | Remember per function that its reach is built; adds state to `Built`. |
| F4 | `check` struct declaration and `field_index` | Field names searched linearly (comment: "a struct has few fields"). | Quadratic in fields: 25k fields 2.75 s; 2k fields ~20 ms | A name index per struct; only worth it if generated code has thousands of fields. |
| F6 | `model/src/thir.rs` `write_expr` | `-dump thir` recurses on the thread's stack like `check_expr` did. | Crash on a very deep expression, dump only | `source::grow_stack` there too; `model` would need `stacker`. |
| F7 | `compile/src/lib.rs:289` `others_unchanged` | For each file a cached diagnostic points into, scans every module's results. | O(labels × modules), small today | A set of reused paths built once per compile. |
| F8 | `llvm` `Function::location` | A new `locations` map per function rehashes as it grows (~1.4% of a compile). | 1.4% | Reserving the bound measured +0.6% worse (rejected, see the log); reusing one map across functions is the remaining option. |
| F9 | `flow/src/cleanup.rs:43` `skip_empty_blocks` | Follows each jump chain up to the block count from every block. | Quadratic only on long chains of empty blocks | Not measured as a problem; leave. |
| F11 | `diagnostics` printer | Every diagnostic prints its whole source line, and at most `MOST_WRITTEN` (200) are printed. | At most 200 × the line's length | Trim a long line around the span, as rustc does; changes the visible format, which `docs/architecture.md` fixed. |
| F12 | `source::grow_stack` in `check`, `lower` | A stack check per expression, statement and type costs 0.84% of a compile. | 0.84% | Check only every N levels of depth (a counter in the builder), if the owner wants the time back. |
| F13 | `flow/src/loans.rs` `live_at_ends` | Lists at block ends cost more than sets where many holders stay live across many blocks. | 3,000 references across 3,000 branches: 154 MB, 1.9 s; 124 MB, 1.0 s with sets (1.3 s at the base) | Accepted for the gain when few are live (217 MB to 53 MB). |
| F10 | `flow::variable_state` | A function that moves many variables still keeps a set of them at every block. | blocks × moved variables | Liveness of moved variables, as the loan check does; only if a real program needs it. |

## Rules check for `fix/loans-memory-growth`

- Wrappers: none added. `source::grow_stack` is the one stack guard, used by the
  parser, `project`, `check`, `lower` and `model`.
- Aliases and ignored parameters: none. `step_live`'s `flipped` callback is
  used by the report walk; the dataflow pass passes `|_| {}`.
- New helpers: `sorted_union` (2 uses), `live_set` (2), `Bits::ids`,
  `Bits::toggle`, `add_member` (2), `define_struct` (2), `Dumper::names` (3);
  `live_at_ends`, `held_at_starts`, `label_later_use`, `tracked_variables`
  each have one caller and split a function over 60 lines by job (RULES §2).
- Behavior: no diagnostic changed (old and new `check` output compared on
  every fixture, example and 800 random programs, both targets; LLVM text
  byte-identical on 187 programs). The one new behavior: deep expressions,
  statements and types no longer crash.
- Benchmark (`scripts/bench-instructions.py`, one compile): 3,706.6 M at
  `1fa2750` to 3,769.4 M at `59dc4cc`, +1.69%. The cloud run counted +1.96%
  under callgrind: +1.1% names map, +0.84% stack checks, the rest within 0.1%.
