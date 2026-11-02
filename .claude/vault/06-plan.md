# Plan

Branch `fix/loans-memory-growth`, from `main` at `1fa2750`. The
task (owner, 2026-11-01): fix the loan check's memory growth, fix the
checker's name lookup, and review the codebase for design mistakes,
unnecessary work, duplicate logic, overengineering and pass-through
wrappers, going as deep as possible; keep this vault so no work is
forgotten or invented.

## Steps

1. [x] Loan check: one live set per block, not per instruction (`0c03928`).
2. [x] Checker: names through a map (`ebbbb81`).
3. [x] Context vault (this folder).
4. [x] Variable state check: follow only variables that can be unset
   (`333e327`).
5. [x] Loan check: live sets at block ends as lists (`a318b22`).
6. [x] Split `Loans::report` by job; inline `loan_count` (`d51cf18`).
7. [x] LLVM debug info: one writer of a struct's debug type (`0a901b5`).
8. [x] Parser test dump: one `names` method (`7928223`).
9. [x] Variable state: `tracked_variables` split out (`2253c35`).
10. [x] Compile-time calls: reached functions in a set (`184e64a`).
11. [x] Deep expressions: `grow` in check and lower (`029622f`).
12. [x] Deep statements and types: `grow` in `check_statement`,
    `lower_statement`, `resolve_type` (`59dc4cc`).
14. [x] Review fixes: one stack guard (`0b94289`), deep array types
    (`e567d09`), a limit on printed diagnostics (`715cc88`), comments
    (`505b592`).
15. [x] Debug builds of lines past column 65,535 ("Keep debug columns within
    LLVM's limit").
16. [x] Arrays in arrays are flat: each array type keeps its base and count
    (branch `fix/flat-arrays`).
17. [x] Structs keep their size and alignment, and LLVM sees every value kept
    in memory as bytes (branch `fix/struct-chain`).
18. [x] Playground projects: the browser takes a real project with
    `peeper.toml`, through `project::project_in` (branch
    `feature/playground-project`).
19. [ ] Owner: decide the open findings below. Benchmark for the fourteen commits: +1.69% instructions,
    accepted in review.

## Open findings (not changed; each needs an owner decision or is small)

| # | Where | Finding | Cost now | Proposed fix and its cost |
| --- | --- | --- | --- | --- |
| F1 | `check/src/lib.rs` `ModuleChecks` | Three tables indexed alike per declaration (`declared_types`, `constants`, `bodies`); each slot is used in one table only. RULES §5.1 discourages parallel lists. | 3 slots per declaration, 2 unused | One `Vec` of a per-declaration enum. `docs/ir-design.md` decided "one table per language category", so this is the owner's call. |
| F3 | `check/src/function.rs` `evaluate_call` | Each compile-time call walks every function it may reach, even when all are built. | C × R visits (200 calls × 4,000 functions: 0.17 s) | Remember per function that its reach is built; adds state to `Built`. |
| F4 | `check` struct declaration and `field_index` | Field names searched linearly (comment: "a struct has few fields"). | Quadratic in fields: 25k fields 2.75 s; 2k fields ~20 ms | A name index per struct; only worth it if generated code has thousands of fields. |
| F5 | `llvm/src/function/memory.rs` `write_place_address` | 175 lines, three jobs: steps with index checks, the `getelementptr`, loading the next run's pointer. | Readability | Extract the index check; needs about eight inputs, so a context struct or a wide signature. |
| F6 | `model/src/thir.rs` `write_expr` | `-dump thir` recurses on the thread's stack like `check_expr` did. | Crash on a very deep expression, dump only | `grow` there too; `model` would need `stacker`. |
| F7 | `compile/src/lib.rs:289` `others_unchanged` | For each file a cached diagnostic points into, scans every module's results. | O(labels × modules), small today | A set of reused paths built once per compile. |
| F8 | `llvm` `Function::location` | A new `locations` map per function rehashes as it grows (~1.4% of a compile). | 1.4% | Reserving the bound measured +0.6% worse (rejected, see the log); reusing one map across functions is the remaining option. |
| F9 | `flow/src/cleanup.rs:43` `skip_empty_blocks` | Follows each jump chain up to the block count from every block. | Quadratic only on long chains of empty blocks | Not measured as a problem; leave. |
| F11 | `diagnostics` printer | Every diagnostic prints its whole source line, and at most `MOST_WRITTEN` (200) are printed. | At most 200 × the line's length | Trim a long line around the span, as rustc does; changes the visible format, which `docs/architecture.md` fixed. |
| F12 | `source::grow` in `check`, `lower` | A stack check per expression, statement and type costs 0.84% of a compile. | 0.84% | Check only every N levels of depth (a counter in the builder), if the owner wants the time back. |
| F13 | `flow/src/loans.rs` `live_at_ends` | Lists at block ends cost more than sets where many holders stay live across many blocks. | 3,000 references across 3,000 branches: 154 MB, 1.9 s; 124 MB, 1.0 s with sets (1.3 s at the base) | Accepted for the gain when few are live (217 MB to 53 MB). |
| F10 | `flow::variable_state` | A function that moves many variables still keeps a set of them at every block. | blocks × moved variables | Liveness of moved variables, as the loan check does; only if a real program needs it. |

## Rules check for this branch

- Wrappers: none added. `source::grow` is the one stack guard, used by the
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
