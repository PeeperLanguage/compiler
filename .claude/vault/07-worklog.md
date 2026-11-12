
- Review fixes (Fuad: "fix all"), all from step 11's lowering, found by
  the review of the speed work: (1) a constant of the class `Wide` was
  built in nine places (`-x`'s zero, the zero test before `/`, range
  counts and counters; a `u128` range counts in `Wide(129)`); the
  interpreter read it as an index into `wides`, so `const N = neg(5)` on
  `i256` gave 0 and `div(100, 100)` a false division by zero. Now
  `class_constant` (core lowering) keeps every wide constant as
  read-only data, and `Operand::integer` asserts no `Wide` class. (2) `~x`
  flipped only 128 bits (`u256` `~0` was 2^128-1). (3) A spread part
  after one counted at run time truncated its `usize` position as if it
  were the wide count (`[4]u128{(0..n)..., 9}` put 204 for 9). New
  fixture `run_wide_integers` (const and run, interpreter = native);
  lowering test `a_wide_constant_is_loaded_from_read_only_data`, seen to
  fail on the assert with the old negation. `check.sh` exit 0;
  `compare.sh` the same 11; CPU counts unchanged.

## Middle cleanup, step 13: the LLVM writer on the MIR of core concepts (2026-11-11, not committed)

- `crates/backend/llvm/src/body.rs` (new) writes a function's `define`
  from `model::core_mir`: slots are `alloca [size x i8]`, a place's steps
  fold into one `getelementptr`, a `Check` branches to a panic block and a
  new block, no `phi`. `function.rs` and `function/*` are deleted;
  `text.rs` keeps only `symbol`, `copy_symbol`, `quoted`.
- C calls: lowering computes how C passes each value
  (`lower/core/c_abi.rs`, by `Machine::c_abi`, 32-bit included) into the
  call's `CSignature`; the writer only follows it.
- Debug info from `DebugTypes`, written from a work list
  (`Metadata::debug_type`); `DEEPEST_DEBUG_TYPE` back to 5,000.
- `print` is what lowering emits; structs print inline, so a struct that
  holds the one before it twice doubles per level: test
  `structs_holding_structs_twice_print_in_one_function_each` ignored,
  waiting on Fuad.
- Gate: compare.sh 981 identical, 139 differ; `.native`, `.interpret`,
  `.check` all identical except the two fixtures step 12 changed
  (negative_comptime_calls, run_print); the rest is `.ll` and step-12
  `.mir`. Workspace tests pass except the 13 `ll_` fixtures (new text,
  awaiting review before PEEPER_BLESS). `llvm_parity` ported; it fails
  when the MIN / -1 guard is broken on purpose.
- Cost: debug build of big.peep 17.4 s to 19.2-20.1 s; `-dump ll` 2.37 s
  to 2.69 s; release build 58.4 s to 61.1 s; LLVM text 9% bigger (allocas
  +40%, stores +57%), from the MIR's shape: slots for `&&`/`||`, copied
  temporaries, byte-array allocas.
- Removed `mir::Body::constant_text`: its only caller was the old print
  writer.

### Step 13 review fixes (2026-11-11, Fuad: "fix all", not committed)

- One operator mapping: `lower/src/core/binary.rs` (`binary_operation`),
  used by `lower_binary` and eval's `llvm_parity` test (its own copy
  removed; `compare_integers` removed). Breaking `>` in lowering on purpose
  now fails the parity test.
- `core_mir::Body::named_functions`: the one list of functions a body may
  run, for `check` (`evaluate_call`) and the LLVM queue.
- `add_body` takes its parameter types from `peeper_types`.
- No more needless copies: a parameter passed in memory is the caller's
  copy (`Body::parameter_in_memory`; writer names it `%a<n>`, no alloca;
  interpreter uses the passed address), and `lower_into` builds a literal,
  slice, range, `&str`, interface value or call result right where it goes
  when the place is new (`let`, argument copy, result, a field of one).
  Assignments still build apart. New test
  `a_struct_parameter_is_the_callers_copy` (fails with the old copy).
  `negative_comptime_calls`: the million-step limit now trips one line
  earlier (`let held = values;`), as fewer instructions run; expectation
  moved.
- Gate: compare.sh 981 identical, 139 differ, the same set as before; no
  `.native`, `.interpret` or `.check` change beyond step 12's two fixtures.
  check.sh: only the 13 `ll_` fixtures fail (not blessed).
- Measured on big.peep (no struct copies in it, so fix A does not show):
  debug build 15.6 s base vs 19.3 s; release 44.0 s vs 49.1 s; LLVM text
  +16% lines, allocas +40%, stores +57%. Cause, found by comparing `f0`:
  each `&&`/`||` join is a 1-byte slot with two stores and a load where the
  old writer had a `phi` (exactly +2 allocas, +4 stores per function), and
  an unused `zext` after each comparison. Not fixed; for Fuad.
- Phi vs slot measured (2026-11-11, `target/step12-probes/phi/`): the new
  writer's `.ll` with each hidden `&&`/`||` slot rewritten to a `phi` by
  script (`to_phi.py`), all else equal. Loop program (`main.peep`, 300M
  iterations): -O0 slot 1992-2013 ms, phi 2263-2309 ms (phi slower, 85 vs
  79 instructions); -O2 slot 444-447 ms, phi 391-431 ms (code differs,
  both vectorized). big.peep clang time: -O0 17.2 s vs 16.7 s, -O2 84.7 s
  vs 84.0 s (text -4%). Phi is not the cause of the debug build slowdown.
- Branch lowering of `&&`/`||` measured (2026-11-11, same probes,
  `to_branch.py`: joins become direct branches, no slot, no `zext`; dead
  blocks kept). Loop program: -O0 1913 ms vs slot 2266-2289 ms, phi
  2646-2678 ms; -O2 1218-1235 ms vs slot 524-548 ms, phi 477-522 ms: with
  branches LLVM does not vectorize the loop (46 vs 190 instructions).
  The same loop in C through clang -O2 (its front end also branches):
  1131 ms. big.peep clang time: -O0 17.0 s slot vs 14.8 s branch (-13%),
  -O2 84.9 s vs 82.3 s (-3%); text -12%.
- Trims measured (2026-11-11, `trim.py`: unused `zext` lines; jump-only
  blocks threaded and unreachable blocks dropped). big.peep: 48,000 `zext`
  and 112,001 blocks removed; text 2.46M -> 2.19M lines (old writer:
  2.13M). clang -O0: slot 16.8-16.9 s, zext 16.4, blocks 16.2, both
  16.0 s; old writer's text 16.4 s. clang -O2: all 84.8-85.5 s, old 82.9 s
  (no gain). Loop -O2 machine code identical with and without the trims;
  -O0 runs within noise (73 vs 79 instructions).
- Full debug build re-timed, alternating, 3 runs each: old 18.6-19.1 s,
  new 20.2-20.4 s (+9%, not the +24% of the first, noisy run); dump ll
  2.53 -> 3.07 s. So about 0.5 s is peeper's own work, 0.4 s clang.
- Trims done in the LLVM writer (2026-11-11, Fuad: "go ahead with the
  trims", not committed): `jump_targets` (a jump goes past blocks that
  write nothing and only jump), `reached_blocks` (unreached blocks not
  written), `values_read` (a comparison's `zext` only when read as a
  byte). In the writer, not lowering: the MIR keeps those blocks for
  W0001 and the path checks (step 14), and `Dead` for the interpreter.
  big.peep text 2,192,035 lines, as `trim.py` gave. Tests
  `jumps_skip_empty_blocks_and_unreached_ones_are_left_out`,
  `comparisons_widen_only_when_kept` (each fails with its trim off).
  check.sh: only the 13 `ll_` fixtures fail. Timed (3 runs, alternating):
  debug build old 17.5-17.7 s, new 18.3-18.6 s (+5%, was +9%); dump ll
  2.32-2.38 vs 2.74-2.92 s; release build 60.5 vs 63.5 s. Loop program
  built by peeper: debug 2261 vs 2297 ms, release old 1178 ms, new 520 ms.
- `ll_struct` reviewed by Fuad and blessed (2026-11-11): comment rewritten
  for the bytes layout and in-place literals; expected.ll written as the
  fixture test writes it (dump, folder path as `<fixture>`), not with
  PEEPER_BLESS. 12 `ll_` fixtures left to review.
- `ll_struct_parameters` reviewed by Fuad and blessed (2026-11-11), same
  way; comment unchanged (still true). 11 `ll_` fixtures left.
- `ll_slices` reviewed by Fuad and blessed (2026-11-11): comment rewritten
  (two words in memory, passed by the address of the caller's copy). 10
  `ll_` fixtures left.
- Nine more `ll_` fixtures blessed (2026-11-11, Fuad: "do all in once"):
  const_parameters, debug_scopes, extern_integers, interfaces,
  pointer_offsets, print_arrays, release, size_of, slicing. Comments
  rewritten: print_arrays (inline loops), slicing (count is an `i128`).
  extern_integers re-run against a rewritten C checker
  (`target/step12-probes/extern/checks.c`): exit 0 at -O0 and -O2; a broken
  check exits 40. Narrow integers now pass as `i8`/`i64` (clang: `i1`,
  `i5`, `i64`), the same register bits on x86-64.
- `ll_array_ranges` held: `range_count`'s constant path never runs (its
  one caller, `part_count`, passes loads of the stored range), so constant
  ranges get a run-time count and check. Lowering bug from step 11.
- Seen, not fixed (debug-only cost): an interface reference copied to a
  temporary before each method call; nested array literals built apart and
  copied; a spread copies each element by `memcpy`; an unused position add
  after a spread.
- `ll_array_ranges` bug fixed (2026-11-11, Fuad: "yes fix it", not
  committed): an array literal's range spread is lowered to its walk
  (`walk_range`, an `enum Part { Value, Range }` per part), not stored and
  read back, so `range_count` sees constants and no length check is
  emitted; `fill_range` takes the walk. Test
  `a_constant_range_spread_needs_no_check` (fails with the reload put
  back). `known()` in the fixture: ~100 -> ~50 lines, no check, no udiv.
  check.sh: only `ll_array_ranges` fails, waiting on review.
- `ll_array_ranges` blessed (2026-11-11), comment rewritten. All 13 `ll_`
  fixtures blessed; `bash scripts/check.sh` exits 0, every gate ok.
- Step 14 start (2026-11-11), commit c6bba520: the core MIR gets the facts
  the path checks read from types. Origins move onto every `Signature`
  (`Body::origins` gone). `read()` emits `Moved` for register values (a
  moved `&mut T` gave none; test fails with the old `read()`). `Moved`
  carries `lent_again` and `size`; `Step::Index` carries `count`; `Borrow`
  and `Address` carry `size`. Fuad chose byte ranges for the loan check:
  a slice of an array covers start..array end, so `a[0] = 9` while
  `&mut a[1..]` is live becomes accepted (old: refused, probe
  `target/step12-probes/step14/slicing.peep`). check.sh ok after fmt.
  Nothing reads the new fields until the checks are ported.
- Struct and array printing removed (2026-11-11, Fuad: "keep only scalar
  type printing"). Inline printing of a struct that holds another twice
  grew 2^depth (`peeper build` at depth 18: 1.4 s, 400 MB). `check`
  `printable` takes numbers, `bool`, `&cstr`, `&str`, raw pointers and
  references to them; core `print.rs` lost its struct and array arms.
  20 fixtures print element by element, values checked against the old
  output (`target/step12-probes/step14/verify_prints.py`);
  `run_print_arrays`, `run_print_deep_struct`, `ll_print_arrays` and two
  tests that printed a whole aggregate deleted; `negative_print` refuses a
  struct, an array and `&Point`. Design page section 11 records it.
  Later (Fuad): `print` a library function, a type prints by an interface.
- Step 14 (2026-11-11): the path checks read the core MIR. `loans.rs`
  rewritten on byte ranges (`report_loan_errors(body, pointer_bytes, ..)`);
  `variable_state.rs` on core events; `flow::mir_predecessors` gone. Fuad
  accepted four checks that now pass where the old refused: `.len` keeps
  no loan, disjoint slice parts, two-phase `&mut` through memory
  arguments, a spread of known length is exact (`x_test/loans`). New
  `loans_i686` and `negative_loans_i686` (a 4-byte pointer; the negative
  one fails with 8). Found while removing print: core lowering's
  `add_holders` walked every field of a struct with no references, so
  `check` hung on `checks_structs_holding_structs_twice` (`same` alone:
  20 s+; now 0.00 s); it returns on `holders_in == 0`. Register parts of
  a literal now carry the literal's span: 4 `ll_` dumps re-blessed (debug
  locations only). `path_check_memory` bound 45 MB while `check` builds
  both MIRs (back to 30 at step 16).
- Steps 15 and 16 together (2026-11-11, this commit): one MIR. The MIR of
  core concepts is `model::mir` (decision 9.3, the name stays) and
  `lower::core` is the `lower` crate root; the old MIR, its lowering
  (`ranges.rs`, `slices.rs`, old `expression.rs`/`statement.rs`),
  `flow::cleanup` and `semantic_types::slice_field` are deleted, and
  `compile` no longer depends on `flow`. Check lowers each body once, into
  throwaway debug types, for the path checks and compile-time runs; a
  backend or a dump lowers it again into the program's debug types through
  `CheckedFunction::mir`, which gives none when `has_errors`. `lower_call`
  returns on an argument of an invalid type (a reused-module test
  panicked). The MIR printer lists each body's globals after its blocks;
  blocks no path reaches stay in dumps and the LLVM writer skips them. The
  19 `mir_` dumps re-blessed after review. `path_check_memory` `branching`
  bound back to 30 MB (25 MB measured). Docs: `ir-design.md` MIR section
  rewritten for slots, places and `dead`; design page status done.
  New test `a_function_whose_path_checks_fail_gives_no_mir`: with the
  `has_errors` check removed no other test failed; this one does
  (`[true, true]`). check.sh ok.
- Old vs new MIR, measured 2026-11-11 (`a77e760c`, before the MIR work,
  vs `784d4831`; `target/step12-probes/perf/`, fastest of 5 runs, taking
  turns). 54k-line `mixed` program (structs, arrays, slices, loops, `&&`):
  `check` 0.27 -> 0.40 s; `check -dump mir` 0.43 -> 0.66 s; `build`
  7.41 -> 6.73 s; `build -release` 23.5 -> 24.3 s; memory the same.
  `run -interpret` of a 2M-round loop 6.79 -> 3.15 s. Built loop of 200M
  rounds: debug 4.49 -> 3.92 s, release 1.19 -> 1.17 s. Benchmark (no
  clang) 3,890 -> 4,697 M instructions (+21%). Profile of `check`
  (samples): loan check 251 -> 539, lowering 94 -> 204, malloc 345 -> 430.
  MIR is 41% longer (3,640 -> 5,151 lines for 30 functions: hidden slots
  in place of `phi`, `dead` lines, a literal stored field by field); LLVM
  text 8% longer (37 -> 39 MB).
- Cause of the slower `check` (2026-11-11, `perf stat` instructions of
  `check mixed`): old 1,016 M, new 1,378 M. Loan check 444 -> 642 M: it
  walks 29% more lines (291,360 -> 375,567, `dead`s included in both) at
  12% more per line (1,524 -> 1,709). The rest 572 -> 736 M (variable
  state only 10 M). A quarter of the MIR is lowering's hidden slots (301
  `dead`, 871 loads and stores, most of them `for` loop state). Prototype
  (`target/step12-probes/perf/skip-lines.patch`, not applied): the loan
  check walks only lines that can give, use or break a loan (20% of
  them), found once from its own `defines`/`uses`/`accesses`: 1,196 M,
  0.33 s (was 0.40); building that set costs 172 M. A line is 96 bytes
  (old 48): `Operand` 24 (old 8), constants inline.
- Fix 1 applied (2026-11-11, Fuad: "yes apply fix 1"): `Loans::matters`,
  found once by `instructions_that_matter` from `defines`, `uses`,
  `accesses` and the places written; every walk skips the rest. `check
  mixed` 1,378 -> 1,194 M instructions, 0.40 -> 0.34 s (old 0.27).
  Mutation: without the borrowed-slot access part `negative_array_flow`
  fails; without the uses part `negative_loans`(`_i686`); the defines and
  holder-write parts are caught by no test (with today's lowering each
  such line also uses a holder or holds no loan), kept as the walks'
  mirror. check.sh ok.
- Untangle (2026-11-11, Fuad: "use untangled to fix all issues including
  past commits", "ok go on"); decision log
  `target/step12-probes/untangle/decisions.md`, fixes 1-11, then
  comments, then tests.
- Untangle fix 1, lower once: `Checker::debug_types` is the program's one
  table, which every lowering writes (the path checks' and a run at
  compile time's too). `CheckedFunction` carries the path-checked
  `mir: Option<mir::Body>` and `&DebugTypes`; `ty`, `has_errors` and
  `CheckedFunction::mir` (which lowered again) are gone, with the four
  throwaway tables and their `shortcut:` comments. `DebugTypes::types` is
  private (`get`); `FunctionQueue::add` takes the table and, in a debug
  build, copies the types made since its last copy (`copy_new`): the last
  batch goes after the checker is dropped. The backend now writes the
  MIR the path checks read; a build lowers each function once (twice
  when a constant ran it: the run's build is not reused). Mutation: with
  `copy_new` copying nothing, 8+ llvm tests fail. check.sh ok. Speed not
  measured.
- Untangle fix 2, one table of what each instruction touches:
  `InstructionKind::accesses` gives each place with its `AccessKind`
  (read, write, borrow, address, move) and its width
  (`PlaceAccess::width`), `InstructionKind::operands` each value operand,
  `Place::operands` a place's address and indexes. Readers moved onto
  them: variable_state's `accesses` (gone) and one `whole_slot_change`
  for `Facts::step`, `move_sites` and `tracked_slots`; loans `uses`,
  `defines`, `accesses` and the lines-that-matter scan; llvm
  `values_read`. Not moved: loans `step_held` (a transfer per kind),
  `holds`, `move_of_read` (still relies on lowering's order), mir
  `own_class` (another question). Removed the wrong "events" comment over
  `Borrow`. Speed (`check mixed`, instructions): an iterator table cost
  +23% (1,165 -> 1,433 M; not inlined across crates, then arrays and
  chains built per line); as `#[inline]` functions taking a closure it is
  1,193 M (+2%: `uses` now dispatches on the kind three times). Mutation:
  a move read as a read, a store as a read, a copy's write dropped, a
  store's value dropped each fail the fixtures; call arguments dropped
  failed nothing, though clang refuses `f(a == 2)` then: added that case
  to `comparisons_widen_only_when_kept`, which fails on it (2 vs 3).
  check.sh ok.
  Then (Fuad: "yes do a and b"): `Dead` is in the table as
  `AccessKind::End` of all of its slot; `PlaceAccess` holds a
  `PlaceRef` (root and steps, borrowed; `Place::as_place_ref`), which
  now owns `whole_slot`, `is_behind_pointer` and `operands`.
  variable_state's `SlotChange`/`whole_slot_change` and the loan check's
  hand-made "gone" access and `Dead` arm went; the loan check's `Access`
  holds a `PlaceRef`. `AccessKind::Borrow` lost `words` (message text):
  `Loans::accesses` reads them from the `Borrow` until fix 3's per-loan
  lookup. A `Dead`'s width is now `None` (all there is) for `Some(size)`:
  the same bytes. Mutation: `Dead` giving no access fails the fixtures.
  `check mixed` 1,207 M (was 1,193). check.sh ok.
- Untangle fix 3, the loan check built in one step (Fuad: "Yes"):
  `Holders` (body, pointer bytes, borrows, holder starts, loans through)
  owns every per-place and per-line query and the steps (`uses`,
  `defines`, `step_live`, `step_held`, `step_write`); `reserved_loans`
  and `lines_that_matter` are its methods returning values; `Loans {
  holders, reserved, lines }` is one literal, no placeholder. The lines
  that matter are stored by block and are what every walk walks (no
  filter); `next_use` walks them too (a line not among them uses and sets
  no holder). `Holders::lent` gives a loan's `Borrowed` (instruction,
  place, size, mutable, words) or `Parameter(position)`: five hand
  destructures and four numbering decodes gone; `loan_count`,
  `first_level`, `value_holder`, `call_arguments` (destination and
  written arguments, the hidden count decided once; `origin_holders`
  takes them) each replace two or three copies; `holder_words` indexes
  the instruction instead of `ids().nth`; `returned` is
  `returned_holders`; `step_write` finds the result kept in memory by
  its holders. Left: the literal `Root::At(Parameter(0))` (lowering's,
  next fix), `sorted_union` beside `loans_of` (different jobs),
  `mir::Holder` (same idea, not named here). Mutation: a parameter
  loan's position, the hidden split, the result write each fail the
  fixtures; `holder_words`' `Borrow` branch cannot be seen (a slice
  borrow is stored to a slot before use; a reference's words are the
  fallback's). `check mixed` 1,207 -> 1,178 M. check.sh ok.
- Untangle fix 4, first part: three MIR facts stored twice removed.
  `Signature::variadic` was written by lowering and never read (the C
  signature carries a variadic call's extra arguments); `Body.parameters`
  was always `signature.parameters.len()`; `Slot.hidden` was always
  `debug_type.is_none()` (lowering gives a debug type to each written
  variable and none to a slot made for a value). Readers: loans
  (`loan_count`, `held_at_starts`, `slot_name`, `broken_loan`), llvm
  debug info, the MIR dump. check.sh ok. Left for fix 4: the result kept
  in memory and a call's hidden first argument, constants' decode, panic
  messages, runtime callees' signatures, extern-ness.
- Untangle fix 4, second part (Fuad: "go with Root::Result"): a result
  kept in memory is the place `Root::Result`, not `Root::At(Parameter(0))`,
  and `Operand::Parameter(i)` is always written parameter `i`. Gone: the
  `hidden` shift in lowering's parameter stores, `parameter_in_memory`,
  `class_of`, the loan check's parameter loans and its literal
  `Root::At(Parameter(0))` match. Only the machine level counts the
  hidden argument now: LLVM names it `%result` (written parameters keep
  `%a0`..), the interpreter reads it at the frame's first register. A call
  still passes the destination as its first argument (`call_arguments`).
  Six dumps re-blessed: only `%a0` -> `%result`, `at arg0` -> `result`,
  and the written parameters renumbered from 0. Mutation: the loan
  check's `Root::Result` giving no holders, and the interpreter without
  the shift, each fail the fixtures. check.sh ok.
- Untangle fix 4, third part (Fuad: option 1): a call names the place it
  writes a result kept in memory into, `Call { .., result: Option<(Place,
  u64)> }`, instead of an `Address` instruction passed as its first
  argument. The access table gives it as a `Write` of that place, so the
  loan check's `call_arguments` (split off the first argument, then find
  the `Address` that made it, two `unreachable!`s) and the `Call` arm of
  `defines` are gone; `uses`, `reserved_loans` and `step_held` read the
  arguments and the place directly. LLVM writes the place's address and
  passes it first (`write_call`'s `result_at`); the interpreter pushes it
  as the first register. The write now happens at the call, not before
  the arguments: every place lowering writes a call result into
  (`lower_into`'s four callers) is a new variable, `Root::Result`, part
  of a value being built or an argument's copy, which no reference can
  reach; `x = grow(&x)` already goes through a temporary (probe in
  `target/step12-probes/alias`). Dump: `call make() into 16 p.0`. Four
  dumps re-blessed, numbering only; LLVM writes the same instructions.
  Mutation: the table giving no write for the call passed every test, so
  `x_test/run_call_result_each_pass` was added (a slice result in a loop:
  without the write the next pass keeps the last pass's slice live and
  `values[0] = ..` is refused); the loan check skipping the place fails
  the fixtures. `check mixed` 1,174-1,180 M (was 1,178). check.sh ok.
- Untangle fix 4, fourth part: one rule and one reader for constants.
  Every producer already kept the bits above a constant's class zero
  (`bigint`'s results clear them; each `Operand::integer` caller passes a
  value that fits), but the readers each guessed: the interpreter masked
  to the class, LLVM's division check masked, the dump and LLVM joined
  the two words by hand. The rule is now written on `Operand::Constant`
  and asserted in `Operand::integer`; `mir::constant_value` joins the
  words for the dump, `constant_text`, `divides_by_other_than_minus_one`
  and the interpreter (no mask). Low-word reads (`bits[0]`) of classes up
  to 64 bits stay: the rule makes them exact. Mutation: `constant_value`
  dropping the high word fails the browser run fixtures. check.sh ok.
  Uncommitted together with the third part; that part is saved as
  `target/step12-probes/untangle/call-result*.patch` to commit alone.
- Untangle fix 4, fifth part: a `panic`'s message is on its terminator,
  `Terminator::Panic { message: Box<[u8]>, span }`, as a `Check`'s is on
  its instruction, instead of an index into the body's globals. The
  globals entry was read back by LLVM and the interpreter through an
  `unreachable!` that it was bytes, and LLVM wrote it as an `@.str` that
  nothing used (the panic writes its own "panic: ... -->" text). The
  two terminator matches now match on a reference. `mir_panic` dump:
  `panic "too big"`, no `global 0`. Mutation: the interpreter writing
  the message short fails the browser run tests. check.sh ok.
- Untangle fix 4, sixth part (Fuad: "go with the enum"): a runtime call
  names its function by `runtime_abi::symbols::Runtime` (PanicWrite,
  PanicExit, PrintText, PrintBytes, PrintInt, PrintFloat, PrintPointer,
  Flush; `Runtime::signature` holds the old eight constants' table), not
  by its symbol string: `Callee::Runtime(Runtime)`. The interpreter
  `match`es it (was six string compares and an `unreachable!` for any
  other name; the two panic arms now say lowering makes none). How a
  carrier is passed is decided once, `mir::Signature::of_runtime`, for
  lowering and for LLVM's panic calls, which now declare through the same
  `declaration`/`peeper_types` as every other call (was a second
  `Carrier` map in `write_runtime_call`). `model` depends on
  `runtime_abi`. No dump changes. Mutation: the interpreter printing a
  pointer off by one fails the browser run tests; `of_runtime` giving
  `usize` as `i32` fails the 64-bit LLVM dumps. check.sh ok.
- Untangle fix 4, last part: a C call's C signature names the C function,
  `CSignature::symbol`, which lowering already looked up to decide the
  call is a C call. The interpreter reads it there instead of asking the
  driver through `ProgramFacts` (deleted: the struct, `Host`'s field and
  lifetime, `program::run`'s parameter, the closure in `compile`); LLVM's
  `write_c_call` reads it there instead of its symbol map, and no longer
  needs the `Callee::Function` pattern or a too-many-arguments allowance.
  Left: `llvm/text.rs::symbol` still names an `extern` function by its C
  name for the symbol map, which only tables read; no `extern` function
  can be in a table today (a probe of `#[extern] fn P::size(self)` is
  refused), so it is naming, not a second decision. Mutation: a wrong C
  name fails the browser run tests. check.sh ok.
- Untangle fix 5, one owner for class and passing decisions:
  `Passing::register_class` (a value in memory travels as its address)
  replaces three copies of that match: `Body::class_of`, the
  interpreter's `fits`, LLVM's `peeper_types`. `Comparison::integer_less`
  picks signed or unsigned `<`/`<=` once for `binary_operation` and the
  two for-range comparisons. Lowering's `class` reuses `float_class`, as
  float negate now does. `Lowering::signature` turns a function type into
  a MIR signature once, for a body's own signature and for each interface
  table entry. The table entry's receiver override (`self` as a pointer
  register) is dropped: the entry is built from the interface's method
  types, where `self` is `&Shape` (a pointer register) or an owned `Shape`
  (memory, passed by address), and the only reader (LLVM's `declare`)
  writes `ptr` for both. No dump
  changes. Mutation: `register_class` giving `I64` for memory and
  `integer_less` swapping signed both fail the browser run tests; dropping
  the override failed nothing, which is how it was found dead. Skipped:
  layout's size bands (alignment, not a class), `Class::bits` on Pointer,
  `class` vs `in_memory`, the call-site signature (it carries C variadic
  arguments the type does not), "void means none". check.sh ok.
- Untangle fix 11, `flow` needs no semantic types:
  `report_missing_return` asks the MIR whether the function gives a
  result (`signature.result`, as LLVM's `FallOff` already does) instead of
  comparing the result `TypeId` with `VOID`, and takes the result type's
  name as `result_text`, a closure the checker passes, called only when
  the error is reported. `flow` no longer imports `Types`, `TypeId` or
  `DefId`. Same answer: `void` cannot be written as a type (a probe of
  `type Nothing = void;` is refused, T0002), so no alias of it exists.
  No message changes. Mutation: a fixed label fails
  `negative_missing_return_causes`. check.sh ok.
- Untangle, reachability on `mir::Body`: `Body::reachable` and
  `Body::mark_reachable` (moved from `flow`, unchanged) are the one walk
  of which blocks the entry reaches. LLVM's `reached_blocks` (deleted, a
  second walk over jumps already sent past empty blocks) is now
  `reachable[b] && targets[b] == b`: the same set, since `jump_targets`
  is stable (a target targets itself, and an endless chain of empty
  blocks stops on one that does). A probe of two endless `for {}` loops
  writes the same `.ll` before and after. Mutation: dropping either half
  of the filter fails `jumps_skip_empty_blocks_and_unreached_ones_are_left_out`.
  check.sh ok.
- Untangle, `flow` split into modules (Fuad: "yes, modules, in that
  order"): `lib.rs` keeps only the engine (`dataflow`, `predecessors`,
  `Bits`); `missing_return.rs` and `unreachable.rs` hold the two small
  checks; `loans.rs` became `loans/` with `mod.rs` (`Holder`, `Loan`,
  `Holders`, `Loans`, `report_loan_errors`, `holder_ids`, the walk
  `Loans::report`), `places.rs`, `holders.rs`, `two_phase.rs`,
  `passes.rs` (with `sorted_union`, used only there) and `errors.rs`.
  Each file adds its job's methods in its own `impl` block; items a
  sibling uses are `pub(super)`, nothing wider. The checker calls
  `flow::missing_return::report_missing_return` and
  `flow::unreachable::warn_unreachable_code`; no re-exports. A pure move:
  the old and new files hold the same lines, apart from imports, `impl`
  wrappers, `pub(super)` and two signatures rustfmt wrapped. `check
  mixed`: 1,181-1,185 M instructions after, 1,180-1,193 M before. The
  code database's flow rows were regenerated from the source (they named
  items deleted before this, such as `Loans::variable_name`). check.sh ok.
- Untangle fix 6, the interpreter has one run mode (Fuad: "go with
  Memory owning the mode"): `Memory::run`, an `enum Run { Constant(open
  blocks), Program }`, is the one record of which run this is; it
  replaces `Memory.constant_blocks: Option` and `Interpreter.host:
  Option`, which had to agree. The host is always there (empty for a
  constant), `Memory::is_constant` answers the ten branches, and
  `Memory::leave_call` ends a constant's frame where `close_block` used
  to be called as a silent no-op in a program. A slot not started is
  `None` (`Option<NonZeroU64>`), not 0. The two `STEP_LIMIT`s are
  `CONSTANT_STEP_LIMIT` and `PROGRAM_STEP_LIMIT`. `Failure` lost
  `Exited`: an internal `Halt { Failed(Failure), Exited }` carries it,
  and only `program::run` turns it into an exit code, so the checker's
  `unreachable!` arm is gone and compile's names `NoMir`. `float_class`
  moved from lowering to `Class::float` in `model::mir`, which the
  interpreter now shares with lowering and the parity test; the
  interpreter's float constants reuse `float_bits`/`float_value`.
  Skipped: dropping `body` from `run_function` (the checker looks the
  body up first to tell a cycle from a run). Mutations: `usable` always
  true and a slot block never closed fail the browser check tests; a
  constant's missing-MIR call sent to the C path failed nothing, as the
  only fixture called the `extern` straight from the constant, which
  never runs; `negative_comptime_calls` now also calls one from a
  function the constant runs (`const WRITES`), which catches it.
  `check mixed` unchanged (1,183-1,184 M). check.sh ok.
- Untangle fix 7, one meaning for each operator (Fuad: "go with one
  core"): the interpreter's register operations moved from
  `interpreter/instructions.rs` to `eval::operations` (integer, float,
  compare, convert, with `RegisterValue { Narrow(u128), Wide(Cow<Limbs>) }`
  so one entry covers both widths). The interpreter calls them through
  `register`/`hold`; its `convert` method's wide and float-to-integer
  paths are now `operations::convert`. The constant folder
  (`operators`, `conversions`) no longer has its own arithmetic: it takes
  the MIR operation lowering emits (`binary_operation`, moved from
  `lower/src/binary.rs` to `model::mir`, so eval drops its `lower`
  dev-dependency) and runs it through `operations` at the type's own
  width. Unary is lowering's forms: `0 - x`, `-0.0 - x` (the old sign
  flip differed only in which NaN), `x xor ones`. A shift count is read
  unsigned and kept out of range, as lowering's check does. So the
  llvm_parity test, which runs the folder, now checks the interpreter's
  arithmetic too, and eval/lib.rs's "cannot disagree" is true. Not done:
  which `Conversion` an `as` is, and the unary forms, are still chosen in
  both lowering and the folder (the meaning of each is one). Mutations
  of the core (signed divide, signed less, float subtract, sign extend,
  `swapped` ignored, shift count not clamped) each fail a folder test or
  the interpreter's run fixtures. `check mixed` 1,176-1,179 M (was
  1,183-1,184 M); `run -interpret loop_2000000` 16,465 M, was 16,413 M
  (+0.3%, the register value built per operation). check.sh ok.
- Untangle fix 8, `compile::run`'s switches have names: `require_main`
  and `record_facts`, two bare bools in a row at 23 calls (five driver
  calls, eighteen in tests), become `compile::Options { require_main,
  record_facts }` with `Default`; a caller names only the one it turns on
  (`Options { record_facts: true, ..Options::default() }` in the
  language server), and the two `// names are for editors` comments go.
  `previous` stays an argument: it is a value, not a switch. In the
  checker, `build_function` and `check_body` took `to_run` and used it
  only as `records_facts: !to_run`; the parameter is now `records_facts`
  (the compile passes `true`, `evaluate_call` `false`; four test calls
  flipped with it). Mutations: the compile's build or the run's build
  recording the wrong way, `require_main` ignored and `record_facts`
  ignored each fail compile tests. No hot path changed, so no
  measurement.
- Untangle fix 9, lowering's own copies, in seven steps (patches
  fix9a..g under target/step12-probes/untangle):
  - surplus: `through_slice` (a `.then(Step::Through)` wrapper, also used
    for an interface's struct), `field_offset` and `read_for_print` (one
    caller each) and `runtime_call`'s unused result are gone; the dead
    `| Type::Str` borrow arm too (`borrows()` is `None` for `&str`).
    `types[types.real(x)]` is `types[x]`: indexing looks through aliases.
    The slice debug type uses `add` like the others (reserve and define
    with nothing recursive between); no dump changes, as the writer
    numbers metadata itself.
  - two words: four hand-written `.then(Step::Offset(pointer_bytes))`
    use `second_word`; an interface's two stores are `interface_words`,
    beside `slice_words`.
  - constants and converts: a `usize` constant is
    `Operand::integer(self.usize, n)` everywhere (its assert holds: the
    checker refuses an array length past `usize`, probed on i686);
    `integer_constant` takes an `Integer`, so zeros and the shift width
    use it; all six hand-built `Convert`s go through `convert`.
  - `RangeWalk` is a struct of five named fields, built by name, moved to
    and from a range value by `range_field` name (`RangeWalk::fields`);
    `range_integers` unpacks a range type at three sites.
  - `lower_expr` builds the seven in-memory kinds through `lower_into`
    (`in_temporary`), so each is built in one place.
  - `copy_elements` and `fill_range` share `for_each_index`, which
    replaces `counter_slot` and `step_counter`.
  - special cases: `checked_usize` takes the `Comparison` (no `BinaryOp`
    and no `unreachable!`); the method receiver is lowered before the
    argument loop, not found by `target == None` in it; `return`, `panic`
    and `break`/`continue` end with `finish_and_leave`. `return` alone
    truncates `declared` because only it has an expression, whose
    temporaries it ended; that is now said where it happens.
  Not done: the slice debug type's `len` still makes its own `usize`
  basic type per slice type, as the language `usize` is named `u64` in
  debug info (asked). No MIR or LLVM dump changed. `check mixed`
  1,172-1,175 M (was 1,176-1,179 M). 247 lines in, 345 out.
- Untangle fix 10, the LLVM writer's own copies (patch fix10 under
  target/step12-probes/untangle):
  - a `FallOff` reached in a function with a result wrote `unreachable`;
    it cannot happen (the missing-return check gives the function an
    error, and `build_function` gives a function with an error no MIR),
    so it is `unreachable!`.
  - `Module::declare_function` notes a Peeper-convention `declare` and
    gives the global name; it replaces `declaration` and the four
    `declare(..declaration..)` + `global` copies (`add_table`,
    `write_call`'s identical function and runtime arms,
    `write_runtime_call`).
  - `folded` gives `None` for a step that moves nowhere, so
    `write_operand` and `write_place` no longer each test `Offset(0)`;
    `Writer::root` names a place's root for both.
  - `global` and `comma_separated` move from body.rs to text.rs (used
    by lib.rs and debug_info.rs too); `slot_name`'s doc, which sat on
    `jump_targets`, is back on `slot_name`.
  The `.ll` of every x_test project, host and i686, is byte-identical
  to the last commit's (260 non-empty dumps). Mutations (an offset of 0
  kept, `%result` renamed, the declare under the wrong symbol, the
  comma written without its space) each fail tests. check.sh ok. No
  hot path changed, so no measurement.
  Not done, asked: two `#[extern("abs")]` functions with different C
  types (`i32`, `i64`) both compile, and the second call is written
  against the first `declare i32 @"abs"(i32)`, unreported
  (probe untangle/declare-clash); and the seven `pub` items in
  `llvm::body` (and `pub mod body`) exist only for eval's
  llvm_parity test.
