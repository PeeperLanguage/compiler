# Work log

Newest last. Each entry: commit, what changed, evidence, what was not done.

## 2026-11-01 Loan check memory (`0c03928`)

- **Problem:** `flow::loans::report` kept a copy of the live holder set after
  every instruction of a block: instructions × holders bits. Measured with
  `peeper check` (release) on one straight function of N groups of
  `let aI = 0; let rI = &aI; total = total + rI.*;` (unique names; reusing a
  name in one block is T0003): 2,500 → 23 MB, 5,000 → 68 MB, 10,000 → 240 MB,
  20,000 → 905 MB / 10.0 s. The MIR has about 9 instructions and 2 holders
  per group; 180k × 40k bits ≈ 900 MB matched.
- **Fix:** `step_live` reports each holder whose bit flips; the backward
  walk stores `(position, holder)` flips, the forward walk toggles them on one
  set (`Bits::toggle`). `activation_error` takes the set from before the call
  (no clone). `step_store` skips its `retain` when no loan is through the
  variable. The report walk drops pairs of holders not live after each step.
- **After:** 20,000 groups 44 MB / 3.8 s; loan check went from 56% to 1.5% of
  instructions (callgrind, 5,000 groups). All-live shape (10,000): 240 MB /
  4.3 s → 25 MB / 3.5 s.
- **Evidence:** `check` output identical old vs new on every fixture and
  example and 400 random loan programs (3,605 T0037), both targets. Test
  `crates/driver/compile/tests/loan_memory.rs` (now `path_check_memory.rs`) adds the straight shape: 13 MB
  now, 32 MB on the old code (fails its 20 MB bound).
- **Not done there:** `check.sh` passed except the five `run_i686_*` fixtures
  (no 32-bit C library where it ran); all pass in the review below.

## 2026-11-01 Checker name lookup (`ebbbb81`)

- **Problem:** after the loan fix, 97% of the long-function time was
  `ThirBuilder::declare` and `resolve`: a linear search of every local name
  per declaration and per use.
- **Fix:** `innermost: FxHashMap<&str, usize>` maps each name to its
  innermost entry in `names`; each `Declared` keeps the entry it hides,
  restored at its block's end. Used by `declare` (redeclaration),
  `resolve`, and the hidden-`mut` warning.
- **After:** 20,000 groups 3.8 s → 0.12 s, linear. Compile benchmark
  +1.07% instructions (3.832 G → 3.873 G, callgrind): hashing costs more than
  a short search in small bodies. Accepted for linear scaling.
- **Evidence:** test `hidden_names_come_back_level_by_level` fails (with
  `names_shadow_until_their_block_ends`) when the restore is broken; `check`
  output identical on 1,174 runs.

## 2026-11-01 Variable state check (`333e327`, `2253c35`)

- **Problem:** three sets of every variable at every block's start. One
  `let late: i32;` in 20,000 groups of a branch and a new variable: 347 MB,
  1.62 s (without `late` the check is skipped: 61 MB, 0.08 s).
- **Fix:** follow only variables whose `let` has no value or that a `move`
  takes whole (`Id<Tracked>`, `tracked_variables`). The function-wide early
  exit stays first.
- **After:** 64 MB, 0.12 s. Benchmark unchanged (3.8732 G). A first version
  without the early exit cost +0.5%; rejected.
- **Evidence:** new random generator (`let x: T;`, moves, non-`mut`
  assignment, loops): 3,793 T0039, 993 T0034, 398 T0007; output identical.
  `path_check_memory.rs` (renamed from `loan_memory.rs`) resets the kernel's
  peak (`/proc/self/clear_refs`) before each function: `late` 25 MB, 70 MB
  before.

## 2026-11-01 Loan check block-end sets (`a318b22`)

- **Problem:** `ends` kept a set of every holder at every block, and seeded
  every block with one: 10,000 branchy groups 217 MB, about 3× per doubling.
- **Fix:** each block's set is a sorted list of live holders; a block is
  walked on a dense set made from it (`live_set`, `Bits::ids`).
  `sorted_union` joins these lists and the `Held` pairs.
- **After:** 53 MB, 0.10 s (was 0.25 s). Benchmark +0.06% (noise).
- **Test:** `branching` at 4,000 groups: 21 MB, 47 MB with dense sets; the
  cubic version before `757544c` grew 407 MB at 400 groups.

## 2026-11-01 Cleanups (`d51cf18`, `0a901b5`, `7928223`)

- `Loans::report` (125 lines) split into `live_at_ends`, `held_at_starts`,
  `label_later_use`; `loan_count` (one caller) inlined. `report` is 60 lines.
- LLVM debug info: slices and structs each wrote the `DW_TAG_member` and
  `DICompositeType` text; now `add_member` and `define_struct`, same node
  order. LLVM text byte-identical on 187 programs, both targets.
- Parser test dump: the `names` closure written twice is `Dumper::names`.

## 2026-11-01 Compile-time calls (`184e64a`)

- `evaluate_call` checked reached functions with `Vec::contains`: R² per
  call. 200 constants × chain of 4,000 functions: 0.61 s → 0.17 s with an
  `FxHashSet`. The rest is F3 in the plan.

## 2026-11-01 Deep expressions crash (`029622f`)

- `peeper check` aborted with a stack overflow at 4,000 terms of
  `x + x + ...`: `check_expr` and `lower_expr` recursed on the thread's
  stack. Both now run through their crate's `grow` (stacker), as the parser
  does. Test `checks_deeply_nested_expressions` (50,000 deep: operators,
  calls, parentheses, `!`) aborted before. Cost +0.45% on the benchmark.

## 2026-11-01 Deep statements and types (`59dc4cc`)

- After `029622f`, nested blocks, long `else if` chains (`check_statement`,
  `lower_statement`) and `& & ... i32` (`resolve_type`) still overflowed: the
  parser's own deep inputs. Same `grow`. Test `checks_deeply_nested_statements`
  (50,000 each; the types case asserts its one T0043). Cost +0.39%; all stack
  checks together +0.84% (F12).
- The parser's `orelse` error-recovery input showed F11 (printer).

## 2026-11-02 Debug builds of long lines ("Keep debug columns within LLVM's limit")

- **Problem:** clang refused a debug build with `value for 'column' too large,
  limit is 65535`: LLVM keeps a column in 16 bits, in `DILocation` and
  `DILexicalBlock`. `-release` has no locations and passed. Already so at
  `1fa2750`.
- **Fix:** `debug_position` gives both their line and column, a column past 16
  bits as 65,535. Panics report the true column; they do not go through it.
- **Evidence:** test `columns_past_16_bits_stop_at_the_last_one` fails without
  the limit. An 88 KB line builds and runs in debug on both targets.

## Measured and rejected

- Reserving `llvm` `Function::locations` to its bound: +0.6% instructions
  (allocating the bound costs more than rehashing, and release builds have
  no debug info). Reverted.
- Switching `compile::interface` from SipHash to FxHasher: not done; the
  hash decides whether a module is reused, so a weaker 64-bit hash risks
  stale results.

## Scans run

- Single-expression functions (about 90): no pass-through wrappers.
- Identical 6-line windows across source files: only the two duplicates
  fixed above; the rest are `use` lists.
- Longest functions: most are one `match` over every kind (allowed by
  `rust-style.md`); `write_place_address` is F5.
- Growth probes (`peeper check`, release): long straight block, branches,
  late-set variables, all-live references, reassigned reference, many
  modules (1,000: 0.02 s), large array literal (100k: 0.03 s), long
  expression (crash, fixed), many struct fields (F4), compile-time call
  chains (fixed, F3).
- Review findings from the first pass still open: `Loans::reserved` is
  built as a placeholder and filled after construction (documented); the
  return check steps the block twice on purpose (error precedence).

## Validation of the branch (2026-11-01, after `029622f`; `59dc4cc` reran the affected tests)

`bash scripts/check.sh`: format, clippy, runtime format and clippy, bundle,
browser build and smoke, whitespace pass; 42 test binaries pass. `tests` and
`fixtures on the bundle` fail only in the `fixtures` test, on the five
`run_i686_*` programs, which cannot link without a 32-bit C library
(`Scrt1.o`, `-lc`). Their `check -target i686-linux` output was compared old
against new and is unchanged. On a machine with the library, the review below
ran `check.sh` fully green.

## 2026-11-02 Review and fixes (`0b94289`, `e567d09`, `715cc88`, `505b592`)

- **History:** the fourteen commits moved from local `main` to
  `fix/loans-memory-growth`; author set to the owner, `Co-Authored-By` and
  `Claude-Session` lines removed, both dates kept, re-signed; trees unchanged.
  The hashes in this vault are the new ones.
- **Evidence for the branch:** `check.sh` green at `d381733`. `check` output
  byte-identical to `1fa2750` on 336 fixture runs (both targets) and 12,000
  random programs (15,845 T0037; 577 T0039, 1,363 T0034, 477 T0035). Putting
  back a set per instruction fails `path_check_memory`.
- **Stack guard (`0b94289`):** the four copies are one `source::grow`.
- **Deep arrays (`e567d09`):** `layout` crashed at 20,000 levels of `[1]`. It now loops over
  array levels: same sizes (zero lengths and overflow compared with the base,
  both targets). `real_text` crashed printing such a type and `compile`'s
  `hash_type` on a test thread; both have `grow` on their array step. `check`
  takes 50,000 levels; `build` still overflows in the backend (F14). Test:
  `checks_deeply_nested_statements`, 10,000 levels; it aborts with the
  recursion back.
- **Printer (`715cc88`):** `S.{ a = ` nested 50,000 times on one line asked for 8 GB once
  the stack no longer overflowed. `print` writes at most 200 diagnostics, says
  how many more, and the summary counts all: 2.1 s, 240 MB.
- **Comments (`505b592`):** `live_at_ends` states its cost (F13); `path_check_memory` no
  longer says a reading is independent of the order, and its numbers are
  corrected.
- **After the fixes:** `check.sh` green; output identical to `1fa2750` on 336
  fixture runs and 6,000 random programs. Benchmark 3,768.7 M and 3,783.1 M in
  two runs, against 3,769.4 M before them: no cost above the noise.
