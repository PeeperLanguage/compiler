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

## 2026-11-02 Flat arrays (branch `fix/flat-arrays`)

- **Problem:** a nested array was a chain of types, and each part of the compiler
  walked it again: `layout`, `is_copy`, `printable`, `real_text`, `hash_type`,
  `debug_type`, `type_text`, and the printers. LLVM got the same nesting, and
  clang crashes parsing a nested array type at 50,000 levels (nested debug
  info is fine; tested by hand).
- **Fix:** `Types::array` is the one writer of `Type::Array`, which also keeps
  `base` and `count`, a derived fact stored on purpose (RULES 5.1, owner's
  approval): recomputing it was the fault. LLVM gets `[count x base]`. An index
  into an array of arrays starts its own `getelementptr` on the element; debug
  info has one node with a range per level, as C's. `array_lengths` is the one
  walk of the levels. A pointer breaks a flat array (`[1]*[1]*...i32`), so
  `real_text`, `hash_type` and `debug_type` keep a `grow` on the pointer, which
  covers plain `***...i32` too. Printing keeps one, as each level has its own
  printer. Clang runs out of its 8 MB stack on deep debug types (16 MB passes)
  and `unsafe` is forbidden, so peeper cannot raise it: `debug_type` follows
  5,000 pointers, then writes an untyped pointer (`shortcut:` in the code).
  Only debug info stops there; the language has no limit.
- `hash_type` hashes the number of levels first: without it a length read as
  the next type's kind, and `[2][11]i32` and `[2]range[i32]` hashed the same (found in
  review, tested by `an_array_length_does_not_read_as_a_type`).
- **Evidence:** 50,000 levels: check 6 s to 0.06 s, debug build 0.39 s (it
  was a clang crash), with print 7.8 s and 45 s in release (one printer per
  level). 200,000 levels: check 0.19 s, debug build with print 27 s. A
  2,000-level print runs natively and on the interpreter. Output is the same
  as the old compiler's on the mixed program, on the alias program (x86_64,
  i686, interpreter) and on 6,000 fuzz programs (no crashes). Everyday compile:
  3,768 M instructions against 3,769.4 M, within noise. Extern signatures
  reject arrays, so no C call depends on the type's shape, and a flat array has
  the same bytes.
- **Not done:** Deep print still grows with depth, as each level has its own
  printer: at 200,000 levels a release build passes the 60 s limit and a debug
  build uses 1.7 GB. `hash_type` hashes each declaration's whole chain, so 50,000
  aliases each one level deeper than the last take about 26 s to check (39 s before).
  `[18446744073709551615]Empty` fails in a debug build, old and new: clang's
  debug info limits a count to `i64`.

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

## 2026-11-02 Struct chains (branch `fix/struct-chain`)

- **Problem:** structs in structs had the fault arrays had. `layout` walked
  every struct inside a struct, so check was quadratic in a chain and
  exponential when a struct held the last one twice: 40 such structs, a short
  program, hung the compiler. A debug build overflowed the stack at 6,000.
  LLVM got the whole shape at each use, `{ { { i32 } } }`.
- **Fix:** `Type::Struct` keeps its `size` and `align`, a derived fact stored on
  purpose (RULES 5.1, with the owner's yes), written by `define_struct` from
  what the declaration already computed. LLVM no longer sees structs or arrays: a value kept in
  memory is `[N x i8], align A` (`memory_text`, `alloca_text`), a field a byte
  offset (`layout::fields_placed`, which the interpreter shares), an index a
  `getelementptr` on the element's size. Layout had been worked out twice, by
  us and by LLVM, and had to match. `printable` is a list with each struct
  looked at once. A printed field's name is numbered: a name built from the
  path passed LLVM's 1,024 bytes at about 340 levels, and clang refused two as
  one. `debug_type`'s depth stop covers structs too, past which a struct has
  its size and no fields.
- **Tried first:** naming each struct once (`%struct.N.M`), which worked but
  needed definitions in every module, ordered outermost first as
  `StructType::checkBody` walks the structs already read. Bytes need neither.
- **Not done:** the step after `flow` that turns places into addresses and
  `Check`s, which would also end the interpreter's copy of the walk
  (`places.rs`). `browser smoke` crashes node now
  and then: 1 in 40 at `728f799e`, before flat arrays, and also seen on this
  branch; older than this work, cause not found.
- **Review:** comments, the plan and two fixture headers still said LLVM saw
  struct or array types; they now say bytes. `run_struct_through_imports` went:
  it guarded struct types reaching a module's LLVM text, and there are none.
  `fields_placed` rebuilt a struct's list per access; fixed below (Field offsets).
- **Printers:** a struct printed its fields inline at each `print`, so a struct
  holding the last one twice wrote 2^n prints: 24 levels passed 2 GB at build.
  Each struct or array type now has one `@print.N`, which calls the ones of what
  it holds; 24 levels builds in 0.05 s. `printed_fields` and the name-length
  guard went: names no longer grow with the path. Printers are `noinline
  optnone`: clang's `-O2` on 20,000 of them in a chain took 21 s, 4.5 s with
  this, against 0.4 s inline. Only such a chain pays; output is unchanged on
  x86_64 and i686, debug and release, and on the interpreter. A run now recurses
  one call per level. Printing 2 million structs in a loop went from 3,004 M to
  3,934 M instructions, 0.79 s to 0.94 s: about 60% is `strlen` and `memmove` of
  text split at each printer, 13% is `optnone`.

## 2026-11-02 Fixtures on Windows and macOS (branch `fix/windows-fixtures`)

- **Problem:** CI failed on both after the push of the struct work. The
  interpreter's `write_printed` had no stack guard on a struct's fields, and
  overflowed a debug build's 1 MB main thread at 400 levels (Windows only).
  `negative_struct_pointers` expected the text `x86_64-linux`, the host's name,
  and had failed on both before this work.
- **Fix:** `source::grow` on the field, as on an array's element. The fixture
  expects the text around the name, so it still runs on every host: a `target`
  would skip it off x86-64 Linux.

## 2026-11-02 Holder counts (branch `fix/holder-count`)

- **Problem:** two walks over a struct's fields were left: `holders_in` in the
  loan check, and `Types::holds_loans`, which `check` calls on results and
  stores. Structs that each hold the last twice took 2^n steps: 40 levels hung
  on `fn f(s: S39) -> S39`, with no reference anywhere. Without a stack guard, a
  300,000-level chain overflowed the stack, 20,000 with a 1 MB stack. The fix for
  such chains in `layout` and `printable` had not reached them.
- **Fix:** `Type::Struct` keeps `holders` and `mutable_holder`, written by
  `define_struct` from its fields, like `size` and `align`. `holders_in` moves to
  `Types`, beside `holds_loans`. These and `size` and `align` are derived facts
  stored on purpose, with the owner's yes (RULES 5.1).
- **Limit:** when the innermost struct holds a real reference, the count itself
  is 2^n, and each holder is kept apart, so storing it only moved the cost. A
  struct now holds at most `MOST_HOLDERS`, 4,096, or P0011. Measured for one
  function: 4,096 holders 0.06 s, 8,192 0.21 s, 16,384 0.50 s, 65,536 11.9 s.
  A language rule, agreed by the owner.
- **Evidence:** the ten stress programs finish; 300,000 levels check with a 1 MB
  stack. Each of four bugs put back fails a test: the walk in `holds_loans`
  hangs, no limit runs out of memory, a false `mutable_holder` fails
  `run_struct_references`, a wrong count fails the limit's test.

## 2026-11-02 Internals of a type (branch `fix/holder-count`)

- **Problem:** `Type::Struct` and `Type::Array` mixed what the source says
  (`definition`, `fields`, `element`, `length`) with what the compiler works out
  (`size`, `align`, `holders`, `mutable_holder`, `base`, `count`).
- **Fix:** the worked-out values move into `StructInternals` and
  `ArrayInternals`, inside the type: every phase reads them, so a side table
  would be a parallel list and an extra lookup. Output does not change. The name
  is the owner's call: they are only for the compiler's own low-level work.

## 2026-11-02 Playground projects (branch `feature/playground-project`)

- **Problem:** the browser build invented the package name `playground`, so a
  page could not be a real project: no `peeper.toml`, and a name that differed
  from what the command line would use.
- **Fix:** `manifest::parse` reads manifest text, `project::project_in` makes a
  `Project` from a parsed manifest, and both `project::find` and the browser's
  `load` use it, so the two cannot disagree. The browser takes `peeper.toml` and
  `src/...` in its JSON. A missing or bad manifest is one entry on `peeper.toml`
  at 1:1 with an empty code. The website side lives in the Website repository on
  the same branch name.
- **Evidence:** `scripts/check.sh` passes all ten steps; a Chrome run of the
  playground covers `playground:helper`, a rename to `myapp`, and a bad
  manifest. Four mutations put back in the compiler each fail a test.

## 2026-11-03 Field offsets (branch `fix/field-offsets`)

- **Problem:** `layout::fields_placed` laid out a struct's fields again at each
  access and built a list of all of them: a 10,000-field struct read field by
  field cost about 100 million field layouts. The interpreter kept its own cache
  of the lists, with a wrapper over it (`field_place`).
- **Fix:** `StructInternals` keeps `offsets`, from the `struct_layout` the
  declaration already ran; `define_struct` takes that layout whole.
  `layout::field_placed` gives one field in O(1) for a struct and lays out a
  range's two or three fields when asked. The cache, its wrapper and
  `fields_placed` went; the struct printer reads the offsets directly.
- **After:** that program, `check -dump ll`, 4.0 s before the recent struct work,
  now 1.2 s (check alone 1.0 s). Benchmark 3,788 M and 3,790 M, inside the noise
  seen before (3,785 to 3,795 M). Output unchanged: every dump matched.
- **Evidence:** with every stored offset 0, `ll_struct` and
  `ll_struct_parameters` differ and the interpreter fails `run_struct_references`.

## 2026-11-03 Index checks in lowering (branch `feature/place-addresses`)

- **Problem:** the bounds rule for `a[i]` and `s[i]` had two owners. The LLVM
  writer (`write_place_address`, with `renamed`, the extension, the compare and
  the `traps` place arm) and the interpreter (`locate`) each checked the index
  while finding the address, though `&s[i..j]` was already checked once, in
  lowering. All indexes ran before any check: in `a[g()][h()] = f()` with `g`
  outside, Peeper ran `f`, `g`, `h`; Rust runs `f`, `g` and stops. And
  `grid[pick()].len` never checked its index.
- **Fix:** lowering checks each index right after computing it
  (`lower_index`, through `checked_usize`, split from `lower_slice_end`), and
  the step keeps a `usize`; a constant index into an array stays unchecked, by
  the checker's own test (`known_position`, shared with slice ends). A new
  `length` instruction reads a slice's length for these checks and for slice
  ends (it replaced `load` + `field`); the loan check treats it as Rust's
  shallow read (`Access::own_storage`, which was `replaces`), so a loan of the
  elements does not stop it. `Check` keeps its message as `Box<str>`, built from
  the array's length. The backends only turn a place into an address: the LLVM
  walker lost its checks, `traps` is a `bool` with no place arm, and the store
  shortcut lost its trap condition; the interpreter lost its bound filters.
- **Behavior:** the order above is Rust's (`run_panic_index_order`);
  `grid[pick()].len` checks its index (`run_panic_index_length`). Two borrow
  messages read better: `&s[1..2]` while a `&mut` of an element is in use says
  "borrowed", not "read"; "slice used later here" points at `left[0]`, not the
  whole assignment (`negative_slicing`). Nothing else changed: old and new
  compilers compared on every fixture and example in check, run, release,
  interpreter and `i686-linux` (1,140 cases), and on probes of wide, negative,
  32-bit, compile-time, field, reference and pointer indexes.
- **Speed:** benchmark 3,786.5 M and 3,786.4 M (3,790 to 3,798 M before; it has
  no index). Old against new, user-space instructions (`perf stat`), medians:
  3,000 functions of six indexes, `check` 216.5 to 228.7 M (+5.7%: more MIR
  through the path checks), `check -dump ll` 1,189 to 1,147 M, `build` 11,100 to
  10,835 M, `build -release` 17,767 to 17,733 M, LLVM text 20.89 to 19.47 MB: the
  walker now loads a slice's length only for a borrow of all its elements, as
  nothing else reads it, and writes no zero step before a load. A program of 128 M index
  steps: the debug binary 10,196 to 9,812 M (-3.8%), the release binary the
  same 1,870 M; `run -interpret` 6,332 to 7,258 M (+14.6%), each check now
  three or four MIR instructions run one by one (dispatch, `run_instruction`,
  `run_from` and `operand`, is a third of its time).
- **Evidence:** with `length` a deep read, `negative_array_loops` and
  `negative_slicing` (a new case, `&s[1..2]` while `&mut s[0]` is in use) fail.
  With the array check off by one, `run_panic_index_length` fails (no run
  fixture had an array index equal to the length before); with the slice check
  off by one, `run_panic_index_order` and `run_panic_slice_bounds` fail. An index
  compared after it is cut to 32 bits, or a wide negative one not refused, fails
  `run_i686_panic_index_wide`. `scripts/check.sh` passes all ten steps.
- **Review:** the open finding on `write_place_address`, to extract its index
  check, is gone from the plan: it has no index check now, and is 126 lines, not
  175. `slice_length` had one caller left, so `.len` writes its `field` itself.
- **Step limit:** a compile-time run counts each MIR instruction, so checks
  use up its 1,000,000 steps: a constant of 48,000 passes over `a[i]` fits on
  `main` and not here (about 16% fewer passes). Left as it is; the owner has
  not decided.
- **Not done:** the loan check does not refuse changing a slice inside its own
  index (Rust's E0510); nothing can today (T0043 on every way to write a slice
  through a reference, pointer or array), and that rule must come with the
  first. Division, shift and array-part checks are still written by each
  backend from the operation.

## 2026-11-03 Machine division up to 64 bits (`bigint`)

- **Problem:** `bigint::divide` and `remainder` walked a value bit by bit, with
  allocations, even for one limb. A loop of `%` spent 22% of its interpreted
  time in `divide_magnitudes`.
- **Fix:** a value of one limb uses the machine's division; wider ones still
  walk. The magnitudes, signs and wrapping stay where they were.
- **After:** that loop under `run -interpret`, 3,000 passes of 64 steps: 7,245 to
  4,882 M instructions (-33%); `main` was 6,332 M. A loop with no division: no
  change. A special path with no `Vec`s measured 4,717 M: not worth a second
  copy of the sign handling.
- **Evidence:** a new test compares `divide` and `remainder` with `i128`
  arithmetic: every pair of values at 1 to 8 bits, signed and unsigned, and edge
  values at 31 to 126 bits, wrapping at the signed minimum. It passed on the old
  code first. A wrong quotient or remainder in the new path fails it.

## 2026-11-03 A borrow of a whole slice reads the slice

- **Problem:** after index checks moved to lowering, only a borrow of all a
  slice's elements (`s.*`, as in passing `s` to a call) needed a slice's length
  from the address walk. The LLVM writer worked out which run held it in two
  places, took the slice apart and built it again; the interpreter read the
  length on every step through a slice for this one case.
- **Fix:** `Place::slice_of_elements` gives the slice such a borrow is all of,
  and both backends read it whole. Neither walk touches the length now, and
  `Located` lost its `length`.
- **After:** 3,000 functions passing two `&mut []` slices four times: LLVM text
  12.85 to 8.94 MB, `check -dump ll` 727 to 608 M instructions, `build` 6,812 to
  5,916 M. A loop of `s[i]` under `run -interpret`: 3,186 to 3,148 M. Output the
  same on mixed programs in debug, release, interpreter and `i686-linux`.
- **Evidence:** a wrong slice from `slice_of_elements`, the LLVM writer or the
  interpreter fails run fixtures; `ll_slices` pins the text, with `lend`.

## 2026-11-03 CI on ARM Linux

- **Change (`6f0d0a93`, branch `feature/more-machines`):** `ci.yml` has an
  `ubuntu-24.04-arm` row. The i686 Rust target and `gcc-multilib` install only on
  x86-64 Linux; `wasm32-wasip1` on all Linux.
- **Evidence:** CI run 41613278877 passed on all four machines. The ARM job ran on
  image `ubuntu-24.04-arm` with Rust `aarch64-unknown-linux-gnu` and
  `toolchain-linux-arm64-llvm-23.1.4-rddfb5ccb28fa`, in 2 min 17 s. No compiler
  source needed a change.
- **Not done:** no ARM Linux release archive or installer yet. `target = "i686-linux"`
  fixtures are skipped there by design (`runs_i686`).

## 2026-11-03 Pack and CI on ARM Windows

- **Change (`4a46a92b`, merge `f7d8979f`):** `windows-11-arm` rows in `ci.yml` and
  `build-toolchains.yml`; the runtime's Rust target follows `runner.arch`. Pack
  `toolchain-windows-arm64-llvm-mingw-20261020-rb20404288da9` replaces the old zip,
  193.9 to 180.4 MB. `setup.ps1` drops its remark on zip packs.
- **Evidence:** pack run 41614574297 published only this pack; the other four came
  out identical and were "already published". CI run 41615233104 passed on all five
  machines; the ARM Windows job took 4 min 52 s, with Rust `aarch64-pc-windows-msvc`.
  No compiler source needed a change.
- **Found:** after the owner's approval on PR #179, `auto-merge-toolchain.yml` stopped
  at "GitHub does not report this PR as clean: unstable", seemingly because its own
  job was still running. Its manual trigger with `pr_number=179` merged it. Fixed below.
- **Not done:** no release archive or installer for this machine yet. The lock's
  `format` field has no reader now (`pack.rs` writes it, `main.rs` parses it).

## 2026-11-03 LLVM for Intel Macs, and the toolchain auto-merge

- **Done:** `build-llvm-intel-mac.yml` builds LLVM on `macos-15-intel` and publishes
  it as our own release; the bot pinned it as `llvm-darwin-amd64` (PR #180,
  `9d040210`).
- **Evidence:** run 41617288179 passed: compile 4 h 31 min of the job's 6 hours, file
  `LLVM-23.1.4-macOS-X64.tar.xz` 58.4 MB, `clang-23`, `lld`, `llvm-ar` and `lli` each
  `minos 13.0` and needing only what a Mac comes with, and a C program linked and ran.
  Listing the file showed every path `pack.rs` reads, with `libclang_rt.osx.a`.
- **Fixed:** `auto-merge-toolchain.yml` had never merged a pull request on an approval
  in the runs read (`f4a2af8c`). It asked GitHub for `mergeable_state`, "unstable"
  while its own job runs, so that check is gone. `check_suite` does not fire for
  suites GitHub Actions made, so a too-early approval was never retried: it is now
  `workflow_run` on CI, finding the pull request by commit because the run's own list
  was empty on the merged bot pull requests read. Closed ones are refused by state.
- **Evidence:** the check script, run locally against real pull requests, stopped or
  went on as it should; not yet run live: the next bot pull request is its test.
- **Evidence, live:** bot PR #181 merged by itself 5 s after approval (`94ab8066`): the
  `workflow_run` run read the approval, and the review run then stopped on the closed
  pull request. Approving well after CI has finished is not yet seen live.
- **Pack:** `toolchain pack` needed no change. Run 41661204845 published
  `toolchain-darwin-amd64-llvm-23.1.4-refeaa4f1aed4`, 82.8 MB against the old 670 MB;
  the Intel job took 2 min 13 s. The ARM Mac pack is 137 MB; ours is likely smaller for
  its two LLVM targets (not compared file by file).
- **CI:** run 41663551454 (`aafaf013`) passed on all six machines; the Intel Mac job took
  8 min 51 s from a cold cache, on the new pack. No compiler source needed a change.
- **Not done:** no Intel Mac release archive or installer. The old pack's release stays
  until the owner's cleanup.
- **Found:** `main` has no branch protection and no rulesets, though `RULES.md` 10.1
  says it does. Owner's call.

## 2026-11-03 Release archives and installers for the new machines

- **Done:** `release.yml` builds archives for `linux-arm64`, `darwin-amd64` and
  `windows-arm64`, with its Rust targets chosen as in `ci.yml`. `install.sh` no longer
  has a list of allowed machines: all four it maps have archives, and the Rosetta check
  stays. `install.ps1` maps AMD64 and ARM64 as `setup.ps1` does. `install-check.yml` has
  rows for the three machines.
- **Evidence:** `install.sh` run with stand-in `uname`, `sysctl` and `curl` names the right
  archive for Linux and macOS on both processors and under Rosetta, and refuses FreeBSD and
  riscv64. The two workflow files parse.
- **Evidence, test release:** tag `v0.3.0-rc1` at `ba3dfe77`, run 41670753822, passed on all
  six machines, fixtures on each unpacked archive. Sizes 85.8 MB (`darwin-amd64`),
  221.7 MB (`linux-arm64`), 183.0 MB (`windows-arm64`). Each new archive's `peeper` is for
  its processor, its checksum matches, and the ARM Windows `peeper.exe` names no Visual C++
  runtime. The draft and the tag were then deleted.
- **Not done:** `install.ps1`, `install.sh` against a real release and `install-check.yml`
  have not run: PowerShell is not installed here, and the install check needs a published
  release, so the next release is their first run.
- **Fixed:** `install.ps1` ran `tar` from the PATH, where `setup.ps1` runs
  `System32\tar.exe` because a GNU tar takes `C:` for another machine. It now does too.

## 2026-11-04 Methods undone

- **Decided (owner):** the first methods try is undone. It treated a method as
  a feature of its own (its own scope table, lookups, call path and errors)
  instead of a function that lives and is found as any function is.
- **Done:** `feature/methods` reset to `dc7b2247`; the four commits are kept in
  `backup/methods-first-try`, the uncommitted work in the stash "methods first
  try: uncommitted".
- **Next:** a one-page design reviewed by the owner before any code.
- **Evidence:** `bash scripts/check.sh` passed on the reset branch; the bundle
  rejects `fn P::new()` ("expected `(`, found `::`").

## 2026-11-04 Duplicate cleanup and a code database

- **Done:** a code database in `.claude/codedb/` (one file per crate, and
  `similar.md` for duplicates across crates), written by low-effort agents and
  refreshed after the cleanup; it is git-ignored. On `fix/duplicate-cleanup`:
  - `through_pointer` replaces four copies of "go through a reference or
    pointer" (field, index, slice, reborrow);
  - assigning to a module constant now points at its declaration, as a local
    constant's error did; one branch builds both;
  - `project::declaration` replaces the declaration-by-id lookup, and
    `llvm` `symbol`, `debug_info` and `lsp` `checked.rs` reuse
    `declared_text` / `declared_name`;
  - `SourcesLock::position` replaces two lock lookups in the toolchain.
- **Not done:** one owner for a type's text in messages (waits on the owner);
  sharing the print text of structs and arrays (a marked shortcut, already
  compared by the run fixtures).
- **Evidence:** `bash scripts/check.sh` passed; the new label test fails
  without the change.

## 2026-11-04 One owner for a type's text in messages

- **Decided:** `project::type_text` names a type with each declaration's own
  name; `Checker::type_text` stays as the checker's one-line call (27 uses
  would otherwise repeat `self.modules, &self.types`).
- **Done:** the LSP's own `type_text` is deleted, and the LLVM debug info's
  inline copy uses the shared function. Behavior is unchanged.
- **Kept:** the three `check/declaration.rs` sites still index their own
  module's tree, which they keep using.
- **Evidence:** `bash scripts/check.sh` passed.

## 2026-11-04 One way to run a tool and to name a file error

- **Done:** `llvm::link::run_tool` runs a tool and shows what it printed as
  errors only when it fails; `call_tool` adds what the run was for. The
  toolchain's own `run` is deleted. `llvm::link::file_error` writes "cannot
  {action} {path}: {error}" for both crates; the toolchain's copy and nine
  hand-written texts are gone. `llvm/build.rs` keeps its own panic text, as
  a build script cannot use the crate.
- **Checked, no change:** the other groups of `.claude/codedb/similar.md`
  keep one rule in the interpreter and the backend, whose texts and layouts
  are already shared, or only look alike.
- **Evidence:** `bash scripts/check.sh` passed; the new test
  `a_failing_tool_is_named_with_what_it_printed` fails when `call_tool`
  drops its prefix.

## 2026-11-04 Names that say what they do

- **Done:** 34 items across every crate renamed after a review against
  RULES.md section 5 (`.claude/codedb/renames/`). Examples: `cannot` →
  `file_error`, `Scanner::fail` → `report`,
  `source::grow` → `grow_stack`, `Body::calls` → `has_call`,
  `eval::interpreter::run` → `run_function`, `Interpreter::charge`/`spend` →
  `count_copy`/`count_steps`, `Checker::global` → `resolve_global`, the lsp
  `handle_notification` → `apply_notification`. Comments and docs follow.
- **Kept on purpose:** `call_tool`, `Parser::tok`/`stmt`/`end` (the owner
  kept them), `compile::run` (always read with its crate),
  `fix::chosen`/`fix::write`, `Interpreter::operand`/`allocated`, the
  private `Types::types`/`ids`, llvm `shift_count` (the interpreter's twin
  has the same name), `ThirBuilder::names`.
- **Decided, no change:** the `types` field and parameter (the semantic
  type table) keeps its name; the owner: "types already mean the semantic
  type".
- **Evidence:** `bash scripts/check.sh` passed.

## 2026-11-04 Methods: one name table keyed by (owner, name)

- **Decided (owner):** a method is a top-level declaration with an owner
  (`ast::Function.owner`), found in the module's one name table under
  (owner's `DefId`, name), so interface methods later use the same lookup.
  The single-level key `(Option<DefId>, &'p str)`, with names borrowed from
  the program's text, was chosen over two levels
  (`Option<DefId>` -> `Box<str>` -> symbol).
- **Benchmark** (`scripts/bench-instructions.py`, instructions per compile,
  a program with no type functions):

  | Version | Runs | Mean | Range | vs `main` |
  | --- | --- | --- | --- | --- |
  | `main` (`22b9728b`) | 5 | 3,787.7 M | 3,780.4 - 3,797.1 | |
  | single level (chosen) | 6 | 3,784.8 M | 3,775.6 - 3,792.7 | -0.08% (noise) |
  | two levels | 5 | 3,815.2 M | 3,801.6 - 3,821.9 | +0.73% |

  One run varies by up to 0.45%, so each version ran five or six times; the
  two-level range does not overlap the others.

## 2026-11-04 Methods step 1: a type's functions, called by path

- `fn Point::name(...)` is a top-level declaration with an owner
  (`ast::Function.owner`). `scope::collect` declares it under the struct's
  `DefId`; the owner must be a struct of the same module
  (`` `X` is not a struct declared in this module ``). `resolve_path` reads
  `Type::name` and `module::Type::name` in one loop. Nothing below check
  changed.
- `project::declared_path` gives `Point::name` for the link name
  (`module.Point::name`) and the debug info name.
- Fixed on the way: `fn App::main` was taken as the entry point; moving a
  function to another type did not check importers again; `i32::f()` said
  "`i32` is not a module or a type".
- Tests: parser dump, `scope` (duplicate "in this type"), two `compile` tests
  (each fails without its fix), fixtures `run_type_functions`,
  `run_i686_type_functions`, `negative_type_functions`.
- `bash scripts/check.sh` passed.
- After review: `-dump thir`/`-dump mir` and cycle errors name a type's
  function `Point::Sum` too (`mir::Body::text`/`thir::Body::text` take any
  printable name, as `Types::text` does). Tests: fixture
  `mir_type_functions`, a `Point::E` cycle in `a_cycle_is_reported_once_with_its_path`.
  `check.sh` passed again.
- Second review: `Status::Ready` on an enum said "unknown name `Ready` in
  type `Status`" (it said "not supported yet" before). `scope::has_functions`
  now holds the one rule, only a struct has functions, for `collect` and
  `resolve_path`; the enum gets "`Status` has no functions"
  (`negative_type_functions`). `check.sh` passed.
- Not done: LSP hover and rename for a type's function were not looked at.

## 2026-11-05 Methods step 2: `self`

- `self`, `mut self`, `&self`, `&mut self` are the first ordinary parameter,
  of the new syntax node `ast::Type::Owner` (the owner's choice over pointing
  at the owner's name). The Go receiver `fn (p: T) name()` and
  `Signature.receiver` are gone. Binding `self`, `in self`, the function's
  type, MIR, the interpreter and LLVM did not change.
- Check resolves `Owner` from the declaration being checked
  (`checking.last()`, as `current_module`), through `Scope::struct_named`,
  which `collect` uses too. `check_function` holds the rules: `self` first,
  and only in a type's function. First built in the parser; moved to check
  after the owner's correction ("Parsers main job is to parse").
- A panic found by a probe: `fn plain(&self)` reached `Owner` with no owner.
  Now `check_function` refuses it before any type is resolved.
- Benchmark (`scripts/bench-instructions.py`, one compile, no methods in the
  program):

  | Version | Runs | Mean | Range |
  | --- | --- | --- | --- |
  | `1bffb550` (step 1) | 5 | 3,795.3 M | 3,790.2 - 3,798.5 |
  | step 2 | 5 | 3,808.5 M | 3,805.9 - 3,810.6 |
  | step 2 without the parser's `self` lookahead | 3 | 3,799.9 M | 3,793.9 - 3,805.2 |

  +0.35%; about two thirds of it goes with the parser's lookahead (a text
  compare with `self` on each of the 40,000 parameters). A sampled
  `perf record` per function was too noisy to use.
- The two designs for `self`'s type, run back to back (5 runs each):
  owner type node (kept) 3,802.4 M (3,799.7 - 3,805.7); pointing at the
  owner's name 3,806.1 M (3,798.8 - 3,820.9, one high run). The same within
  noise: the program has no `self`, and both share the parser's lookahead.
  Pointing at the name needs the parser to refuse `self` without an owner,
  cannot type an interface's `self`, reports `fn Missing::f(&self)` twice,
  and check must find `self` by its text. Patches:
  `target/self-owner-node.patch`, `target/self-owner-name.patch`.
- `self` is a special name (owner, 2026-11-05): "like true, false. binding
  but a special one", not a keyword and not colored as one. `token` has a
  `#[names]` section (`false`, `self`, `true`) with its own `NAMES` list;
  `KEYWORDS` no longer holds `true`/`false`, so error output stops coloring
  them like `fn`. The parser compares token kinds only (`TokenKind::SelfValue`
  in `parameter`, `starts_parameter`, an `origin` reader for `in self`, and
  a prefix rule for `self` in expressions); `let self = 1` is now a parse
  error.
- Speed of the token (5 runs each, against `1bffb550`):

  | Version | Mean | Range | vs `1bffb550` |
  | --- | --- | --- | --- |
  | text compare in the parser | 3,808.5 M | 3,805.9 - 3,810.6 | +0.35% |
  | `self` token, second lexer table for names | 3,838.6 M | 3,828.7 - 3,849.2 | +1.0% |
  | `self` token, one merged table (kept) | 3,777.5 M | 3,772.7 - 3,783.3 | +0.02% (noise) |

  The second table cost a lookup on every word that is not a keyword; the
  lexer now merges `KEYWORDS` and `NAMES` by first byte at compile time
  (`WORDS`), so each word costs one lookup as before.
- Not done: an interface method must take `self` first. The parser rule was
  removed (parsers only parse); check refuses interfaces as not supported,
  so the rule is added to check when interfaces are checked.
- Tests: parser (four forms, the dump, the Go receiver is a parse error),
  check (`errors_are_located`: both `self` rules), fixtures
  `run_type_functions`, `run_i686_type_functions` (all four forms and
  `in self`; LLVM and the interpreter agree), `negative_type_functions`.
  `bash scripts/check.sh` passed.

## 2026-11-05 Methods step 3: calls with a dot

- `p.sum()` calls the function `sum` of `p`'s struct and gives `p` as its
  `self`. Taken as recommended when the owner said "continue": `Point::sum(p)`
  does not borrow `p` by itself (as in Rust); a function may share a field's
  name (`c.value` and `c.value()`); the call stays
  `Call { callee: Selector }`, with no new node.
- How: `check_call`'s dot branch (`ThirBuilder::method`) checks `p`, goes
  through a reference (`through_pointer`), and looks the name up with the
  struct's `DefId` as owner through `Checker::member`, taken out of
  `resolve_path` so a path and a dot share one lookup, suggestion and
  export rule. `takes_self` refuses a function without `self` ("call it as
  `Point::new(...)`"); `is_self` is shared with `check_function`. `&self`
  and `&mut self` borrow the place with `check_borrow`; `self` takes the
  place itself, so moves and loans come from the existing flow checks.
  Nothing below check changed: the MIR of `line.Sum()` is the MIR of
  `Line::Sum(&line)`.
- Differs from the plan's table: `&mut self` on a temporary
  (`P::New(1).Bump()`) is accepted, the same as writing `&mut P::New(1)`.
- Speed (5 runs each, against `2aff6689`): 3,777.3 M (3,769.1 - 3,788.1)
  against 3,778.4 M (3,775.9 - 3,781.9), noise. The benchmark program has
  no dot calls. Log: `target/bench-dot-calls.log`.
- Tests: fixtures `run_type_functions`, `run_i686_type_functions` (through
  `&`, `&mut`, a temporary, a by-value chain; LLVM and the interpreter
  agree), `negative_type_functions` (no `self`, unknown and unexported
  names, wrong count, `&mut` of an immutable place),
  `negative_method_moves` (moving out of a reference, use after a move),
  `mir_type_functions`. The `.` hover doc now names dot calls.
  `bash scripts/check.sh` passed.
- Not done: LSP hover and rename on a dot call are not checked.

## 2026-11-05 Methods step 4: docs, `self` hover, example

- Hover on `self`, on the parameter and on each use, shows its binding with
  the owner's type, `self: &P` or `mut self: P`, then the doc of `self`
  (owner, 2026-11-05). `token_doc_at` finds the variable fact at the token,
  or the declaration a use points at, as it finds a literal's type; the
  binding's text (`binding`) is shared with `declaration_of`.
- `self` takes no type (owner, 2026-11-05): `self: i32` stays a parse
  error, the grammar's, not a meaning rule. Its doc says none is written.
- The `fn` doc names a type's functions and both ways to call them; the
  `.` doc names dot calls (step 3).
- Editor: hover and go to definition on `p.Sum` and on `P::Sum` reach the
  declaration. The LSP has no rename.
- Hover on a struct lists its functions under it, each up to its body
  (owner, 2026-11-05); from another module only the exported ones, as only
  those can be called there. `write_functions` walks the struct's module
  once: a type's functions are declared there, where its name names one
  declaration. `is_exported` moved from check's `scope` to `project`, so
  check and the editor share one export rule.
- `examples/18_type_functions.peep`: by path, `&self`, `&mut self`, `self`,
  through a reference, on a temporary; the same output on x86-64 and i686.
- Tests: `answers_on_a_types_functions` (both `self` hovers, `mut self`,
  hover and definition by dot and by path, a struct with a private
  function); `names_across_modules` (another module's struct hides its
  private function). Each fails without its change.

## Slices written `&[]T` (2026-11-05, branch `feature/slice-syntax`, uncommitted)

- Owner decision: a slice is `&[]T` / `&mut []T`, so it reads like a
  reference to an array (`&[4]T`) without the length, and every view shows
  its `&` in front (text `&str` and interfaces `&Shape` to follow). `[&]T` is
  gone with no old-syntax path; bare `[]T` keeps P0011, whose label now
  names `&[]T`.
- Parser only: `reference_type` reads `&`/`&mut` and then `[]T` as the same
  `Type::Slice` node; `&&` in a type is two references (the lexer reads one
  token), so `&&[]T` reaches check's "references to slices are not
  supported yet". Messages and `real_text` print `&[]T` / `&mut []T`.
- All `.peep`, fixture, doc and comment spellings rewritten; test
  `parses_two_references_written_as_one_token`. `scripts/check.sh` green.
- Side repos: tree-sitter-peeper already reads `&[]T` (its old `[]T` rule);
  VSCodeExt `tm-test.mjs` case updated (73 pass); Website
  `content/docs/language/arrays.mdx` on its own `feature/slice-syntax`.
- Step 2 of text (owned `string`) is in `git stash` on `feature/text`.

## Text written `&str` and `&cstr` (2026-11-06, `feature/text`)

- Owner decisions (by questions, 2026-11-05/06): text alone has no fixed
  size, so `str` and `cstr` are refused and only `&str` / `&cstr` are
  values; text views are read only (no `&mut str`); no UTF-8 promise;
  `&cstr` is one address; a `c"..."` literal may not hold a zero byte.
- Check: `&str` / `&cstr` resolve to `Type::Str` / `Type::CString`, which
  print as `&str` / `&cstr`; a plain one is P0011 "`str` is text of any
  length, so it has no fixed size"; `&mut str` is P0011 too. Lexer: L0001
  "C text cannot hold a zero byte". Parser: a plain `[]T` is read whole and
  named in P0011, and parsing goes on.
- Not done: text is not a loan holder (`Type::borrows`, `shortcut:`);
  making it one refused a literal returned from a function with no
  parameters and arrays of text (tried, undone).
- Fixture `negative_text_views`; lexer test
  `a_c_literal_holds_no_zero_byte`; `mir_comptime_calls` keeps its zero
  byte in a `&str`. `scripts/check.sh` green.

## Borrowed interfaces (2026-11-06, `feature/interfaces`, not committed)

- Owner decisions (by questions, 2026-11-06): a struct implements an
  interface when it has every method by name and exact type (`self` the
  struct); nothing is declared. `&T` becomes `&Shape` by itself where a
  `&Shape` is expected (`&mut T` also to `&mut Shape`, `&mut Shape` to
  `&Shape`). `&Shape` calls `&self` methods, `&mut Shape` also `&mut self`
  ones (T0007 otherwise); a `self` method may be declared, not called
  through a reference. Generic methods: the existing "generic functions"
  refusal.
- Model: `Type::Interface { definition, methods }` (method function types,
  `self` an `InterfaceReference`) made by `Types::define_interface` over
  the `Named` id; `Type::InterfaceReference { mutable, interface }`, two
  words, a loan holder like a reference; `Conversion::ToInterface`;
  `Callee { Function, Method(slot) }` in THIR's and MIR's `Call`;
  `ToInterface { reference, methods }` in both; `Place::split_last`
  (replaces `slice_of_elements`; one borrow branch per backend reads a
  slice or interface reference lent again whole).
- Check: `check_interface`, `signature_type` (split from `check_function`,
  shared), `implementation` (lookup by `Scope::get`, compare with
  `method_type`), `value_type` refuses a bare interface ("owned interface
  values"), `held_type` refuses interfaces and interface references held
  in arrays, pointers and references; calls through `interface_method`.
- Flow: `ToInterface` copies the reference's loans; a method call's result
  borrows by the interface's origins.
- Interpreter: `Register::Interface { address, table }`, the table being
  the `ImplementationId`'s index. LLVM: `{ ptr, ptr }` inline (no header
  change, so no other dump moved), `@.table.N` private constants per
  module, call loads the slot. Compile-time runs reach table methods.
- Module interface hash covers interface methods.
- Fixtures: `run_interfaces`, `run_i686_interfaces` (two modules),
  `negative_interfaces`, `negative_interface_loans`, `ll_interfaces`; test
  `interface_references`. `scripts/check.sh`: tests and fixtures green;
  clippy fixed after.
- Review fixes: a `&mut T` made `&I` is lent as `&T`, so `f(r, r)` with two
  `&I` parameters passes (it was T0037); an interface with a method
  declared twice is invalid, so conversions to it add no second error;
  `interface_method` lists method names only for the "did you mean" hint.
  `run_interfaces` prints `both`, `negative_interfaces` has 15 errors.
  `scripts/check.sh` all ok.
- Implementations numbered: the checker finds a struct and interface
  pair's methods once (`Checker::implementations`, successes only, so a
  failing pair is still reported at each conversion) and stores them in
  `Types` by `ImplementationId`. THIR and MIR `ToInterface` carry the id;
  the interpreter's `tables`/`table_ids` are gone (the id is the table
  word); LLVM writes `@.table.<id>`, the same number in every module, and
  `Written::tables` is a set of ids. Reuse: MIR is never kept between
  compiles (`compile::run` makes a new `Checker` and `Types` each time;
  only the LSP passes `previous`, and keeps diagnostics), so ids need not
  survive. `negative_interfaces` checks a failing pair twice (16 errors).
  `scripts/check.sh` all ok; `ll_interfaces` unchanged.
- Not done: export rules for methods matched from another module (a
  private method can satisfy an interface); `&Alias` of an interface is
  refused as an owned value; `s.*` read as a value says "cannot move out
  of a reference" rather than naming owned interfaces.

## Match borrowed values instead of `ref` (2026-11-06)

- Every `ref`/`ref mut` binding (155, 31 files) is gone: the matched value
  is borrowed (`match &x`, `let P = &x else`), or its `*` dropped when it
  was already a reference; uses of Copy fields read `*field`.
- `method_type` clones the matched `Type::Function` whole (the same
  allocations as cloning its parameters and origins).
- `clippy::ref_patterns` is on in the workspace lints, and `rust-style.md`
  states the rule.
- `scripts/check.sh` all ok; no fixture or dump changed.

## Manual freeing stays; free hook dropped (2026-11-06, `feature/free-hook`)

- Owner: memory taken by hand is freed by hand; memory the compiler takes
  itself, the compiler frees. No free hook now; `defer` or destructors
  later, if a real program needs them.
- Why: a probe of a `#[free]` method on real programs (a file handle, a
  byte buffer, a list of buffers, a consuming method) found the hook needs
  more language first: moving a value out of raw memory, pointer offsets,
  and ending a value without its hook (else `File::Finish(self)` closes
  twice). None of that is needed while freeing stays manual.
- Kept: `core:allocator`'s `Malloc` is `Allocate` now, and `Free` keeps
  its name (owner; runtime symbols unchanged). The `free` keyword is gone (token, parser rule,
  `ast::Expr::Free`, the checker's refusal): it existed only for freeing
  by hand. The parser's `builtin` reads only print now and is named
  `print`.
- `x_test/run_allocator`: `Allocate`/`Free`, and `free` as a function
  name (fails with `P0002` when the keyword is put back).

## Browser smoke: Node's WASI fast-call crash (2026-11-06, `feature/allocator-interface`)

- `smoke.mjs` segfaulted Node 22.23.1 after the deep-recursion request once
  `allocator.peep` grew: gdb shows a full GC inside `uvwasi_fd_read`, called
  as a V8 fast API call, failing in `IterateTurbofanOptimizedFrame`. Same
  crash with the committed compiler and the new library; a Node bug
  (nodejs/node#46777, #53087, both closed stale and unfixed; reports say
  Node 24 stops crashing without fixing it).
- `smoke.mjs` turns fast API calls off (`setFlagsFromString`) before
  compiling the module, with a `shortcut:` comment to drop it once no
  supported Node needs it. It covers `check.sh` and `release.yml`. The
  Website runs the wasm with its own WASI functions in the browser, so it
  is not affected.

## Pointer offsets: `rawptr + usize` (2026-11-06, `feature/allocator-interface`)

- Owner: real allocators need an address some bytes on; only `rawptr +
  usize`, giving a `rawptr`. `*T + n`, `n + p`, `p - n`, `p + p` and
  `p + i32` stay refused.
- Check (`check_binary`): the right operand is checked as `usize`; an
  offset is never folded. A raw-pointer constant that is not `none` is
  refused as "an address is known only when the program runs".
- LLVM: `getelementptr i8, ptr, iN` (not `inbounds`). Interpreter: the
  address plus the offset, wrapped at the pointer's bits; refused in a
  constant's run ("a pointer offset cannot run at compile time, ...").
- Fixtures: `run_pointer_offsets` (+ i686), `ll_pointer_offsets`,
  `negative_pointer_offsets` (proven to fail when `*T + n` is let
  through).
- Not done: messages name `usize` as `u64` (`u32` on i686): the type
  table holds one type for both, as before.

## Allocator interface (2026-11-06, `feature/allocator-interface`)

- Owner: the allocator comes before `String`; `String` will take a
  `&mut Allocator` on each call that allocates or frees, since a struct
  cannot hold a reference yet.
- `core:allocator` gains `iface Allocator { Allocate(&mut self, size) ->
  rawptr, Free(&mut self, memory) }` and `struct Heap {}`, whose methods
  call the raw `Allocate`/`Free`. No compiler change: borrowed interfaces
  already do it.
- Left out until a program needs it: alignment, the size on free, resize.
  An arena cannot be written yet (no pointer offsets).
- Fixtures `run_allocator_interface` and `run_i686_allocator_interface`:
  the heap and a counting allocator through one `&mut Allocator`
  function; the interpreter matches. A struct missing `Free` gets T0001
  "`Half` does not implement `Allocator`: it has no method `Free`".
- Owner: the raw functions are private (`allocate`, `free`); every caller
  goes through `Heap` (fixtures `run_allocator`, `run_struct_pointers`,
  `run_i686_struct_pointers`, `run_outside_sizes`, `examples/modules`).
- Website: not touched until the allocator work is complete (owner);
  `modules.mdx` still calls `allocator::Allocate`/`Free` and must move to
  `Heap` then.

## Allocators in `core:allocator` (2026-11-06, `feature/allocator-interface`)

- Owner: keep `align`. `Allocate(&mut self, size, align)`; `align` must be a
  power of two up to 16, else `none`: without `rawptr as usize` an
  allocator can only round offsets, which is right up to the 16 its heap
  block starts at.
- `Heap`; `Arena` (pieces one after another, `Free` gives nothing back,
  `Reset`, `Release`); `Pool` (chunks of one size, a free list kept in the
  free chunks, overflow of `chunk * count` checked); `Stack` (a private
  `mark` of the stack before each piece; `Free` of any piece but the
  newest panics; it is an `Arena` plus the newest piece and its mark).
  Each gets its block from a `Heap` and gives it back with `Release` (no
  destructors).
- Review fixes: a chunk size near the top of `usize` wrapped to 0 when
  rounded, so a pool handed one address to every caller; now it has no
  chunks (`run_allocators` fails without the fix). The i686 offsets test
  checks that an offset wraps at 32 bits.
- Fixtures: `run_allocators` (+ i686): each allocator through one
  `&mut Allocator` function, alignment, reuse, room, wrong `align`, and the
  out-of-order panic; every caller passes an alignment now.
- Website: still to do when the allocator work is complete.

## `size_of` and `align_of` (2026-11-06, `feature/size-of`)

- Owner: a type can be an argument (step 1 of type parameters); any type
  works. `size_of(T)` and `align_of(T)` give a `usize` the checker works
  out from `model::layout::layout` for the target, so they are constants
  (`const B = size_of(Node);`). They are built-in names, which no
  declaration hides, like the built-in types.
- Parser: a spelling that can only be a type is read as `ast::Expr::Type`
  where a value could go: `*T`, `rawptr`, `[N]T` with no `{` after it,
  `&[]T`, `&mut []T`. `[_]T` still needs its values. Shared spellings (a
  name, a path, `&T`, `range[T]`, parentheses) parse as values; the
  checker reads them as a type where the argument takes one
  (`Checker::expression_type`). `?T`, `fn(..)` and inline `struct` types
  are not read there: the checker does not support them yet.
- A type used as a value: T0004 "`*Node` is a type, not a value"; for `*p`
  of a variable `p`, help "write `p.*` to use the value `p` points to".
- Refused: `str`, `cstr` (no fixed size), an interface (not supported), a
  value ("`1 + 2` is a value, not a type", and "`x` is a value, not a
  type" for a variable no type is named as), a wrong argument count. `*p =
  5;` gets the `p.*` help too. `&[]i32{ 1, 2 }`, which used to get "`[]i32`
  is any number of `i32`s", now gets "`&[]i32` is a slice type, so it
  takes no values" with a fix to `&[_]i32{ 1, 2 }`.
- Users: `Stack::Allocate` in `core:allocator` sizes its `mark` with
  `size_of(mark)` (was a guessed 24 bytes, 12 on i686), and the struct
  allocations in `run_allocators`, `run_allocator_interface` and
  `run_struct_pointers` (+ i686) use `size_of(Node)`. On i686 a node is 8
  bytes, so more fit: `run_i686_allocators` asks for more nodes (8 in the
  arena, 20 in the stack) so running out of room is still tested (sums
  21, 28 after reset, 55).
- Fixtures: `run_size_of` (+ i686: `Node` 16/8 and 8/4, references,
  slices, `&Shape`, arrays, `range[i32]`, a later struct, a constant;
  compiled and interpreted agree), `ll_size_of` (only the number is left:
  `mul i64 %i1, 16`), `negative_size_of` (14 errors). Parser tests
  `type_expressions`, `slice_type_with_values`; `[_]i32;` in
  `expression_errors` (fails without the `[_]` guard, checked).
- Review fix: `value_type` keeps sending a struct's `self` taken whole
  through `declared_type`, as before; `reference_to` is the one place a
  `&T` target becomes a value's type (`resolve_type`, `expression_type`).
- tree-sitter-peeper (`feature/size-of`): the four type spellings are
  expressions; an array literal wins when `{` follows. Every `.peep` file
  in the repo parses as before or better (3 more parse).
- `scripts/check.sh` passed.

## `const` parameters, `core:memory` (2026-11-07, `feature/size-of`)

- Owner: allocators move to `core:memory`, which gains `Realloc` (a method
  of `Allocator`), `Replace` and `New`; `const` parameters take types and,
  later the same day, values known when compiling.
- `core:allocator` is now `core:memory`. `Realloc(&mut self, memory,
  old_size, new_size, align) -> rawptr` on `Heap` (C `realloc`, size at
  least 1, so 0 does not free), `Arena` (the newest piece in place, else a
  new piece and a copy), `Pool` (the same chunk, else `none`), `Stack`
  (newest piece only, else a panic). Runtime `peeper_rt_v1_realloc`;
  interpreter `realloc`. Fixtures `run_realloc` (+ i686).
- `New(const T: type, allocator) -> *T` and `Replace(const T: type, place:
  &mut T, value: T) -> T` (the bytes of `value` and `place` traded).
- `ast::Param.constant`, `ast::Type::Type` (`type` as a parameter's type).
- A call names a `FunctionCopy` (`DefId` and `ConstArguments`, an interned
  list of `ConstArgument::Type` or `::Value`); a plain function is its copy
  with `NONE`. MIR, the interpreter (`mir_of`), LLVM (`copy_symbol`:
  `path.Name<module.Type>`, `Name<3>`), dumps and the queue (by the module
  whose compile built it) all take copies. Debug info points a copy at its
  own file (`Metadata::file_node`).
- Checker: `Checking.arguments`; `resolve_global` reads a `const T: type`
  from the innermost entry. A copy's signature is checked on its first
  call (`copy_type`) and its body by the compile after the module
  (`next_copy`). A `const` value is a constant of the body. Errors in a
  copy are reported in the calling module with "in `Name<T>`, the copy
  made for this call" at the call; the same error in two copies once. A
  copy made in a copy's body reports where the outer one does; its
  signature's errors go to the call's module first, so the outer copy
  counts them.
- A function with `const` parameters gets its own signature checked, with
  invalid types for `T`; an error there makes it invalid. Its body is
  checked only through copies (open QUESTION 3 default).
- Refused: `const mut`, `const ...`, `type` on a plain parameter,
  `const x: T` (not supported yet), `extern`, interface methods, a struct
  method with `const` parameters as an interface's, a `main` with them, a
  non-constant argument (the `const` statement's messages). Copies nested
  64 deep: one error, and no more copies are built.
- The interface hash includes the text of a function with `const`
  parameters: an importer's copy is built from its body (test
  `a_type_parameter_body_edit_checks_its_importers`, fails without it).
- Fixtures: `run_const_parameters` (+ i686: two modules calling the same
  copies, a copy calling a copy, a method, a constant running a copy,
  values, `New`, `Replace`), `ll_const_parameters`,
  `negative_const_parameters` (16 errors), `negative_const_parameters_main`,
  `negative_endless_copies`. Panic lines in the allocator and realloc
  fixtures moved down 23 lines.
- tree-sitter-peeper (`feature/size-of`): `const` before a parameter (the
  old `comptime` there removed), `type` as its type; corpus test.
- Benchmark (`perf stat`, one compile = (3 runs - 1 run) / 2): 3,824.6 M
  at the step-1 checkpoint, 3,854.6 M now, +0.8%. Removed: a per-name scan
  in `resolve_global` (now only for entries given arguments), a per
  variable file lookup in LLVM. The sampled profile misses about half the
  instructions there and does not show where the rest goes; switching
  single checks off moved the count by 10-15 M either way.
- `scripts/check.sh`: all pass but `whitespace`, which flags the blank
  last line the compiler writes after a module's `declare` lines in
  `ll_size_of/expected.ll` (marked `git add -N`); committed `ll` fixtures
  end the same way.

### Review fixes for `const` parameters (2026-11-07)

- A copy no longer checks code its constants rule out: an `if` on a
  constant `bool` keeps only the way it takes, and when that way always
  leaves the block (`leaves`), the rest of the block is not checked. Code
  after a written `return`, or after an `if` decided at run time, is still
  checked, as in a plain function. `Count(const n: u64)` recursing to
  `Count(n - 1)` stops at 0 instead of nesting 64 deep or failing on
  `0 - 1`.
- LLVM names a float argument by its bits (`copy_symbol`): `Same(NAN)` and
  `Same(-NAN)` printed alike and made one name twice, which clang refused.
- `copy_called`: a missing `const` argument hands back the function's own
  type, so `check_call` reports the one argument count (counting the
  `const` parameters); a failed argument sets a `failed` flag instead of an
  invalid-type marker in the list.
- `record`: skips only a real copy (one in `copies`), so a function's own
  signature records its facts; `const_value_type` turns facts off while it
  reads a value parameter's type again for a call. Test
  `a_function_with_const_parameters_records_its_signature_once` (fails with
  either half undone).
- Fixtures: `run_const_parameters` and its i686 copy gain `Count` (run and
  in a constant), `Sign` (a decided `else`) and the NaN copies;
  `negative_const_parameters` gains `Scaled()` and `Dead`, a type error
  after a written `return` in a copy (18 errors; without the written-`if`
  rule the `Dead` error is gone).
- Speed: `scripts/bench-instructions.py`, three runs each: checkpoint
  3,815.9 to 3,823.7 M, now 3,856.0 to 3,861.5 M, +0.9%. A profile with
  enough samples (period 200,000, 11 compiles against 1) puts +23 M in
  `check`, spread over about ten places of 1 to 4 M each (the new checks
  per function and call, the `FunctionCopy` table). Tried, each within
  noise (about 5 M): sizing the `bodies` table up front, `#[inline]` on
  `record`, switching off the `const` scans in `check_call`,
  `compile_module` and the interface hash. Not fixed: no single cause.
- `scripts/check.sh`: all pass but `whitespace` (the `ll_size_of` blank
  last line, as above).

### Libraries as projects (`feature/library-manifest`, 2026-11-07)

Branched from `9920cf40` in its own worktree, to merge into
`feature/size-of` later.
- The browser embeds every file under `_builtin_library/` through a
  build.rs list (`LIBRARY_FILES`), not a hand-written `CORE_FILES`; its
  libraries come from their embedded `peeper.toml`. Test
  `every_core_module_is_built_in` (fails when the list misses a file).
- `Manifest::build` (`Build::Program | Build::Library`): `build = "program"`,
  `build = "lib"`, or none for a program; anything else is an error. Not
  required, so existing projects and the website's playground manifest keep
  working.
- `Project::entry` is `None` for a library: `load` starts from each `.peep`
  file of its `src/`, folders too; `Program::entry` is `None`.
- A bundled library is a folder of `libs/` whose `peeper.toml` says
  `build = "lib"`, named by that file, not by the folder. A library project
  of a bundled library's name takes its place (working on `core` checks
  that `core`); a program of that name is still refused.
- `_builtin_library/core/peeper.toml`: `name = "core"`, `build = "lib"`, so
  `peeper check _builtin_library/core` checks every core file, and the
  editor checks a core file as part of `core`, not alone.
- `peeper build`, `run` and `run -interpret` refuse a library: "`name` is a
  library (`build = "lib"`), so it has no `main` to build or run".
- Editor: a check is kept by the entry file, or a library's folder
  (`checked_key`); `project_of` also finds the project of that folder. Its
  tests wrote `build = "bin"`, which was never read; now `"program"`.
- Tests: `reads_what_the_project_builds`, `a_library_loads_each_of_its_files`,
  `a_bundled_library_is_named_by_its_manifest`,
  `checks_a_library_by_its_folder` (fails with a wrong key). Fixtures:
  `negative_library_files` (an error in a file nothing imports),
  `run_library`; the fixture runner also checks each bundled library alone
  (fails on a broken core file). The 13 `build = "lib"` fixtures pass as
  libraries.
- Review fixes: the browser's run refuses a library with the same message
  as `peeper run` (`manifest::LIBRARY_HAS_NO_MAIN`, one text for both),
  where it showed nothing before (test `a_library_does_not_run`); a link to
  a folder inside a library, or a bundled library, is not followed, as one
  back up loaded each file again on each way round (in
  `a_library_loads_each_of_its_files`).
- Merged into `feature/size-of` as uncommitted changes (nothing is
  committed on either branch): a three-way merge of each file against
  `9920cf40`; two conflicts, the browser's file list (the generated list
  kept) and this log's end (both sections kept). The core test now looks
  for `core:memory`. `scripts/check.sh` after the merge: all pass but
  `whitespace` (the `ll_size_of` blank last line, as before); `core`, with
  `New` and `Replace`, checks clean alone. Codedb rows updated. Backup of
  the size-of state before the merge: `playground/backup-size-of-2026-11-07/`.
- Not done: the website (its docs on `peeper.toml`), once the feature is
  complete.

### Review of the `size_of` step and the merged tree (2026-11-07)

- A `const` type parameter given `&str` or `&cstr` was taken for bare
  text: `Replace(&str, ...)` and `v: T` were refused ("`T` is text of any
  length"), and `size_of(&T)` gave 16 for `&&str` (8 is right). The text
  rules in `named_type_at` and `text_view` now look at the written name
  (`Types::builtin`), not at what the name resolves to. Fixtures:
  `run_const_parameters`, `run_i686_const_parameters` (`Referred`,
  `Replace(&str, ...)`); each half of the fix, undone, fails them.
- A top-level function named `size_of` or `align_of` was accepted, and a
  call of it went to the built-in with a far-off error. `scope::collect`
  now reports "`size_of` is a built-in function" at the declaration
  (`negative_size_of`, 15 errors; fails without the check). This gives
  `layout_function` a second caller, so it stays.
- `scripts/check.sh`: all pass but `whitespace` (the `ll_size_of` blank
  last line, gone once committed).
- Decided: a `const T: type` argument is always written at the call, even
  where a later argument fixes it (`Replace(i64, &mut n, 9)`); working it
  out from the arguments is left to `<T>` generics.

### Committed (2026-11-07, `feature/size-of`)

The work was split into four signed commits, each passing
`scripts/check.sh` on its own tree:

- 8560886a `size_of` and `align_of`. It includes the refusal of a function
  named `size_of`. `core:allocator`'s `Stack` uses `size_of(mark)`, and
  the allocator fixtures use `size_of(Node)`.
- 5f548a56 `core:memory` and `Realloc`. It also takes the browser's core
  file list in name order and the `REALLOC` import in order, which the
  next step had fixed: without them this commit failed its tests and
  format.
- ee867ac0 `const` parameters, `New` and `Replace`, with the `&str` type
  argument fix.
- 6f56683d Libraries as projects. This log's entries written after the
  merge are in this commit.

Each commit's tree was built in a separate index file, and the working
files were not touched. Checking was done in a temporary worktree, removed
after. Backups: `playground/backup-commit-2026-11-07/`.

### Review fixes after the PR (2026-11-07, `feature/size-of`, 95c9e838, eb1b80ad)

From PR #182's Windows CI run and two reviews of the branch:

- CI (`windows-11-arm`): `run_realloc` printed `heap: 0 3` for `heap: 10 3`.
  The fixture read `grown` after `heap.Realloc(grown, 4096, 2, 8)`, which
  on Windows moves the piece and frees `grown`; glibc and macOS shrink in
  place. The sum is now read before the shrink, in `run_realloc` and
  `run_i686_realloc`. Proved by reading only: Linux cannot show it. The
  interpreter printed 10 for the freed read: it does not notice a read of
  memory a move freed (not changed).
- The browser build (`build.rs`) embedded every file in `_builtin_library`;
  a file that is not UTF-8 (`.DS_Store`, a swap file) stopped the crate
  compiling. Now only peeper.toml and `.peep` files. Checked both ways
  with a binary `.DS_Store` in `core`: the old script fails, the new one
  builds.
- A library whose `src` was missing, unreadable or empty checked clean.
  `load` now gives M0001 "read library: {folder}: {error}" or "no source
  files in {folder}" when no file of the library is open in an editor or on
  a page (the browser's library files are never on disk), and the program
  is not checked. Test: `a_library_without_files_is_an_error`.
- `bundled_libraries` dropped a folder with no or a bad peeper.toml, or a
  program, and let two libraries of one name replace each other: losing
  `core` lost the prelude with no word. Each is now an error from `find`
  and `create`; a file beside the folders (a README) is skipped; no
  libraries folder still means none. The editor logs the error and shows
  nothing for the file. Test: `a_bad_bundled_library_is_an_error`; the old
  test no longer has bad folders in its libraries. The browser still
  reads its embedded manifests on its own (it panics on a bad one).
- The editor kept a project's last check by its entry, or a library's
  folder; when `build` changed, the old entry and its diagnostics stayed.
  `Server::check` now takes over the files the check under the other key
  published, so they are cleared (`project::entry` made public for it).
  Test: `checks_a_library_by_its_folder` switches the library to a
  program; it fails without the fix.
- The browser's `run` loaded a library before refusing it; `load` is split
  into `project_of` and `load`, and `run` refuses in between, as
  `peeper run` does.
- Vault: the 01-product declaration-check line (copies), its "Not done"
  and "Planned order" lines (allocator done, interfaces borrowed only),
  the "Since then" dates; 06-plan's header and item 3; 0.9% for the
  slowdown; 03-vocabulary fixture counts (check 91, run 89, mir 19, ll
  13).
- `scripts/check.sh`: all pass. The new library error first failed
  `checks_a_project`, whose `libs/core` had no peeper.toml; it has one now.
