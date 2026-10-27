# Rust style

How the compiler's Rust is written: clean, idiomatic code that reads top to
bottom. These are general rules; none of them names one function. Names,
imports, helpers and data rules are in [`RULES.md`](./RULES.md) sections 1,
2 and 5. When code you touch breaks a rule here, fix it in the same change,
even if you did not write it. `Cargo.toml` turns on the clippy lints that
match these rules, so `cargo clippy -- -D warnings` holds them.

## Keep functions flat

- **Handle the odd case first and leave.** Return, `continue` or `break` on
  the error, empty or invalid case, so the main path stays at the left edge.
  Use `?`, `let ... else` and early `return` instead of wrapping the main
  path in `if`.

  ```rust
  // not this
  if let Some(value) = found {
      if value.is_ready() {
          use_it(value);
      }
  }
  // this
  let Some(value) = found else { return };
  if !value.is_ready() {
      return;
  }
  use_it(value);
  ```

- **Two levels of nesting is a lot; three is a sign to restructure.** Flatten
  with early exits, a `match` on a tuple, or a function split along a job
  (RULES.md section 2), not along a line count.
- **One job per function.** If its name needs "and", or a reader must hold
  several modes in mind, split it by job.
- **Decide a choice once.** Match a mode or kind in one place and let each arm
  do its whole job. Checking the same `matches!(x, ..)` in several places of
  one function hides each case's flow.
- **List every variant when a new one must be thought about.** Use `_` only
  when all the remaining variants truly share one answer.

## Loops and iterators

- **Use an iterator method when the loop computes a value.** `any`, `all`,
  `find`, `position`, `map`, `filter`, `sum`, `max`, `collect`, `extend`,
  `retain`. Use a `for` loop when the body does work with effects or exits
  early in ways that read better as statements.
- **Loop over items, not indexes.** Use `iter()`, `enumerate()`, `zip()`,
  `windows()`, `chunks()`, not `for i in 0..v.len()` with `v[i]`, unless the
  index itself is what the loop is about.
- **Don't collect just to loop again.** Pass the iterator on; build a `Vec`
  only when the values are kept or needed twice.
- **No loop where one call does it.** `contains`, `starts_with`, `split_once`,
  `iter().rev()` and friends already exist.

## Values

- **Return the value you just checked, not a constant equal to it.** After
  `if element == TypeId::INVALID`, return `element`. Repeating the constant
  says the same thing twice and hides that the input passes through.
- **Absence is an `Option`, failure is a `Result`.** No `-1`, empty string or
  zero standing for "none", and no empty part to `trim` away later.
- **Read an `Option` with a pattern, not a check and an `unwrap`.** Write
  `if let Some(x)` or `let Some(x) = .. else`, not `is_some()` and then
  `unwrap()`.
- **Borrow by default.** Take `&str` and `&[T]` parameters, not `&String` or
  `&Vec<T>`; clone only where a value must be owned twice.
- **Keep the form the value comes in.** Do not convert to another type and
  back; convert once, where the value is used.
- **`impl Default`, not a `new()` without arguments.**
- **Never derive a fact from order or a naming convention.** `kind as u8 >=
  Kind::First as u8` or "names ending in `_id`" break silently when someone
  adds a variant or a name. Ask a generated list or an exhaustive `match`.
- **No `unsafe`** (`unsafe_code = "forbid"`). When code must walk a group of
  variants, generate the list of them.

## One source for each list

- **Declare a table once and generate the rest.** When an enum, its texts,
  constants and lookup lists must agree, write one `macro_rules!` table with
  one row per item (`tokens!`, `fixed_types!`, `operators!`). Two hand lists
  drift apart; one row cannot be half added.
- **A fact's doc sits on its row.** What a token, a built-in type or an
  operator does is a `///` comment on its row of the table, gathered into a
  `doc` function, so the docs cannot fall behind the table.
- **Change the source text, then read it again.** When data is parsed from
  text that changes (`peeper fix`), edit the text and parse it again; do not
  patch the text and the parsed data side by side.

## Strings and output

- **Write each part into the builder.** Use `push_str` per part or `write!`;
  never `out.push_str(&(a + b))` or `format!` a part only to push it.
- **An IR prints itself.** Each IR's printer lives beside its data, and tests
  call that same printer. Expected output lives in `x_test` files that
  `PEEPER_BLESS=1` rewrites, not in Rust strings.

## Where code goes

- **Data lives in `model`; logic lives in the crate that builds it.** Name a
  data module by its path: `use model::ast;`, then `ast::Expr`.
- **`main` only wires the parts together.** Parsing arguments, compiling and
  reporting each live in their own module.
- **A struct lists what it is made of first.** Storage reached only through
  a parent node, and lookups that only stop work being done twice, go in one
  field named for what they hold (`children`, `written`).

## Speed

Compiling Peeper programs fast matters; these rules keep the hot path cheap.

- **Measure before and after** a change on the hot path (lexing, parsing,
  checking, lowering, writing LLVM) with `scripts/bench-instructions.py`,
  runs interleaved with the old build. A speed trick without a measurement
  does not go in.
- **Keep hot data small.** Put a rare, big payload in a side table and keep
  an index in the hot enum or struct.
- **Hand each result on instead of collecting them all.** A function's IR
  goes to the next phase as soon as it is built, then is dropped.
- **Skip work that cannot find anything,** when a cheap fact proves it, and
  prove it from what the pass reports on.
- **Look ahead only where the rare case already runs,** not in the loop
  every case passes through.
- **Use `rustc_hash::FxHashMap` for a hot map.** Source text is not an
  attacker, so the standard hash buys nothing there.
- **Inline attributes only after a measurement.** `#[inline(always)]` on a
  tiny helper every walk calls, or `#[cold]` with `#[inline(never)]` on a
  rare big arm, once `perf` shows the inliner chose badly.
- **Don't guess capacities.** Reserve only when the size is known.
