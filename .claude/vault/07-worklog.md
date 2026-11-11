
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
