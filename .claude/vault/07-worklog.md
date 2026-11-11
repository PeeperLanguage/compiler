
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
