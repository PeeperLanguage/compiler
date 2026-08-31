# Quickstart: for loop validation

Run commands from compiler repository root. Prerequisite: bundled compiler exists at `build/bin/peeper`.

## Runtime scenarios

1. Range loop

   ```peep
   fn main() -> i32 {
       let mut total: i32 = 0;
       for i in 0..5 { total = total + i; }
       println(total);
       return 0;
   }
   ```

   Expected: fixture succeeds and stdout contains `10`.

2. Fixed, dynamic, and slice iteration

   ```peep
   for value in fixed { ... }
   for index, value in dynamic { ... }
   for value in dynamic[1..3] { ... }
   ```

   Expected: elements are copied in order; exposed sequence index is target `usize`; dynamic and slice paths produce expected sums.

3. Break, continue, and cleanup

   ```peep
   for i in 0..10 {
       let first = alloc(i);
       if i == 3 { continue; }
       let second = alloc(i);
       if i == 6 { break; }
   }
   ```

   Expected: `continue` executes latch before next header check, `break` exits loop, and loop-body owners are cleaned on both paths. Source-driven pipeline tests verify exact drop order; runtime fixture is a smoke check.

4. Nested loops

   Expected: `break` and `continue` target innermost active loop.

## Negative scenarios

- `for x in 5 {}` → cannot-iterate diagnostic.
- `break;` at function top level → `break outside loop`.
- `continue;` outside loop → `continue outside loop`.
- `for x in 0..=5 {}` → exclusive-range diagnostic.
- `for x in "abc" {}` → explicit `as_bytes()`/`as_chars()` view diagnostic.
- iterating temporary fixed/dynamic owner array → addressable-array-storage diagnostic.
- mutating sequence owner in loop body → shared-borrow conflict.
- using iterable after it was moved → use-after-move diagnostic.
- iterating move-only sequence elements → copyable-element diagnostic.

## Commands

Fixture argument is fixture project root, not an individual source file:

```sh
build/bin/peeper run x_test/for_range_loop
build/bin/peeper run x_test/for_array_loop
build/bin/peeper run x_test/for_break_continue

go test ./internal/...
go test ./x_test/
```

Expected fixture wording follows each `peeper.toml`: successful runtime fixtures report `outcome = "success"` and required `stdout_contains` values; negative fixtures report `outcome = "failure"` and required `stderr_contains` diagnostics.
