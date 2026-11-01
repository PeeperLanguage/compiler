# Plan

Branch `claude/loans-memory-growth-9rsgeg`, from `main` at `1fa2750`. The
task (owner, 2026-11-01): fix the loan check's memory growth, fix the
checker's name lookup, and review the codebase for design mistakes,
unnecessary work, duplicate logic, overengineering and pass-through
wrappers, going as deep as possible; keep this vault so no work is
forgotten or invented.

## Steps

1. [x] Loan check: one live set per block, not per instruction (`a8a2cd8`).
2. [x] Checker: names through a map (`a3f72e9`).
3. [x] Context vault (this folder).
4. [x] Variable state check: follow only variables that can be unset
   (`dd36835`).
5. [x] Loan check: live sets at block ends as lists (`5f4ef6f`).
6. [x] Split `Loans::report` by job; inline `loan_count` (`b0a610a`).
7. [x] LLVM debug info: one writer of a struct's debug type (`9b1190b`).
8. [x] Parser test dump: one `names` method (`20a39e1`).
9. [x] Variable state: `tracked_variables` split out (`b905231`).
10. [x] Compile-time calls: reached functions in a set (`c297801`).
11. [x] Deep expressions: `grow` in check and lower (`eec0324`).
12. [x] Deep statements and types: `grow` in `check_statement`,
    `lower_statement`, `resolve_type` (`395afc6`).
14. [ ] Owner: decide the open findings below and the benchmark trade-off (+1.96% instructions in all).

## Open findings (not changed; each needs an owner decision or is small)

| # | Where | Finding | Cost now | Proposed fix and its cost |
| --- | --- | --- | --- | --- |
| F1 | `check/src/lib.rs` `ModuleChecks` | Three tables indexed alike per declaration (`declared_types`, `constants`, `bodies`); each slot is used in one table only. RULES §5.1 discourages parallel lists. | 3 slots per declaration, 2 unused | One `Vec` of a per-declaration enum. `docs/ir-design.md` decided "one table per language category", so this is the owner's call. |
| F2 | `parser`, `project/order.rs`, `check`, `lower` | The stack guard (`RED_ZONE`, `SEGMENT`, `stacker::maybe_grow`) is written in four crates. | Values could drift | One function in a crate all four use; none fits today (`source` is files and spans). Owner's call. |
| F3 | `check/src/function.rs` `evaluate_call` | Each compile-time call walks every function it may reach, even when all are built. | C × R visits (200 calls × 4,000 functions: 0.17 s) | Remember per function that its reach is built; adds state to `Built`. |
| F4 | `check` struct declaration and `field_index` | Field names searched linearly (comment: "a struct has few fields"). | Quadratic in fields: 25k fields 2.75 s; 2k fields ~20 ms | A name index per struct; only worth it if generated code has thousands of fields. |
| F5 | `llvm/src/function/memory.rs` `write_place_address` | 175 lines, three jobs: steps with index checks, the `getelementptr`, loading the next run's pointer. | Readability | Extract the index check; needs about eight inputs, so a context struct or a wide signature. |
| F6 | `model/src/thir.rs` `write_expr` | `-dump thir` recurses on the thread's stack like `check_expr` did. | Crash on a very deep expression, dump only | `grow` there too; `model` would need `stacker`. |
| F7 | `compile/src/lib.rs:289` `others_unchanged` | For each file a cached diagnostic points into, scans every module's results. | O(labels × modules), small today | A set of reused paths built once per compile. |
| F8 | `llvm` `Function::location` | A new `locations` map per function rehashes as it grows (~1.4% of a compile). | 1.4% | Reserving the bound measured +0.6% worse (rejected, see the log); reusing one map across functions is the remaining option. |
| F9 | `flow/src/cleanup.rs:43` `skip_empty_blocks` | Follows each jump chain up to the block count from every block. | Quadratic only on long chains of empty blocks | Not measured as a problem; leave. |
| F11 | `diagnostics` printer | Every diagnostic prints its whole source line. One missing token in a 26 KB one-line program gave 2,000 errors and 100 MB of output; memory and time grow with errors × line length. | Quadratic on long lines | Trim a long line around the span, as rustc does; changes the visible format, which `docs/architecture.md` fixed. |
| F12 | `check`, `lower` `grow` | A stack check per expression, statement and type costs 0.84% of a compile. | 0.84% | Check only every N levels of depth (a counter in the builder), if the owner wants the time back. |
| F10 | `flow::variable_state` | A function that moves many variables still keeps a set of them at every block. | blocks × moved variables | Liveness of moved variables, as the loan check does; only if a real program needs it. |

## Rules check for this branch

- Wrappers: none added. `grow` (check, lower) is called from two places in
  `check` and is the stack guard the parser already has.
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
- Benchmark (callgrind, one compile of the compile benchmark): 3.832 G at
  `1fa2750` to 3.907 G: +1.1% names map, +0.84% stack checks, the rest
  within 0.1%.
