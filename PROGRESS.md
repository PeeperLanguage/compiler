# Progress

Which language features the Rust compiler handles, as of `main` = `70f9e8b6`
(2026-10-28).

- **Done:** checked, lowered to MIR and LLVM, runs, with `x_test` fixtures.
- **Partly done:** works, but some uses are still rejected.
- **Not done:** the parser reads it, but `check` reports
  "... are not supported yet" (T0043).

`examples/` has one program per done feature. `TODO.md` lists the
toolchain and release work.

## Done

| Feature | What works |
| --- | --- |
| Functions | Parameters, results, calls; `main`'s `i32` result is the exit code. |
| Numbers | `i1` to `i8388608` and the same `u` widths, `usize`, `f32`, `f64`, `bool`; literal suffixes; constant folding. |
| Casts | `value as T` between every number type and `bool`. |
| Constants | `const` at the top level and in blocks, computed while compiling. |
| Variables | `let`, `let mut`, a `let` with no value that is set later, block scopes. |
| Conditions | `if` / `else if` / `else`, and `&&`, `\|\|`, `!`, with short-circuit evaluation. |
| Loops | `for { }`, `for cond { }`, `for v in range/array/slice`, `break`, `continue`. |
| Ranges | `a..b`, `a..=b`, `:step`, counting down; `range[T]` values that can be kept, passed and returned. |
| Type aliases | `type Name = T`, in any order, with cycle errors. |
| Modules | Many files, `package:path` imports, exports, the prelude, cycle errors with the path. |
| Moves and loans | Rust-like checks on MIR: use after move, use before set, `&mut` exclusivity, borrows that end at their last use, two-phase `&mut` call arguments. |
| Warnings | Unreachable code; an inner `let` that hides an outer `mut` variable. |
| Targets | `-target <name>`, and 32-bit targets (the `run_i686_slices` fixture). |

## Partly done

| Feature | What works | What is left |
| --- | --- | --- |
| Arrays | `[N]T`, literals, `[_]T`, index with a run-time bounds check, `.len`, spreads `a...`, printing. | Arrays of references or slices; indexing an array that is not in a variable; a range as an index. `.len` will later be a free function. |
| Structs | Declarations, literals `Name.{ }` and `.{ }`, field reads and writes, moves, by-value parameters and results. | References or slices in fields; methods; struct types written inline; generic structs. |
| References | `&x`, `&mut x`, `.*`, field access through a reference, returning a reference. | References to references or to slices; references in structs and arrays; parameters that share one origin. |
| Slices | `[&]T`, `[&mut]T`, `&a[i..j]`, `&s[i..j]`, `for v in s`, `[N]T{s...}`, bounds checks. | Slices of slices or of references; variadic `...T` parameters, which wait on the `...mut T` decision. |
| Pointers | Raw pointers `*T`, `rawptr`, `@x`, `.*`, `none`. | Owned pointers with moves, drops and `free`; the allocator. `*T` is a raw pointer today. |
| Ownership | Moves and loans (see Done). | Drop insertion. |
| Strings | `c"..."` as `cstr`; plain string literals inside `print` and `panic`. | The `str`/`string` and `byte`/`char` types, character literals, run-time strings. |
| Printing | `print` and `println` for numbers, `bool`, `cstr`, string literals, raw pointers, arrays, structs and references to them. | Temporary: arrays and structs will print through a trait or interface later. |
| `panic` | `panic "message"`, exit code 101; run-time check failures panic too. | Messages other than a string literal. |
| Compile-time calls | Functions with numbers, `bool`, variables, loops, branches, calls and panics. | Structs, arrays, references and printing at compile time. |
| C functions | `#[extern("name")]` with numbers, `bool`, `cstr` and pointers. | Structs, arrays, ranges and slices in extern signatures (the C ABI rules); one calling convention. |
| Paths | `module::Name` for another module's constants, functions and types (`geometry::Area(...)`, `geometry::Point`). `.` is only for fields, `.len`, `.*` and, later, methods. | Paths of more than two names (`a::b::c`); paths into types (`Color::Red`). |

## Not done

The parser already reads all of these.

| Feature | Notes |
| --- | --- |
| Enums | Enum declarations, variant literals, `match`, `is`. |
| Optionals | `?T`, `orelse`. The plan makes `Option` a compiler-known generic enum. |
| Dynamic arrays | `[]T`, and the built-ins `alloc`, `append`, `reserve`, `resize`, `shrink`, `len`, `as_bytes`, `as_chars`, `from_bytes`. |
| Allocator | The `Allocator` type. |
| Methods | Functions with a receiver, and method calls. |
| Interfaces | Interface declarations and types. |
| Generics | Generic types, generic functions, paths with type arguments. |
| Function values | Function types, functions as values, calls of computed functions. |
| Default parameters | `fn f(x: i32 = 1)`. |
| Destructuring | Destructuring `let`. |
| Block expressions | A block used as a value. |
| Pipes | The pipe operator. |
| Dependencies | `[dependencies]` in `peeper.toml` and `peeper.lock`; the design is waiting on the owner's answers. |

## Planned order

From the local plans:

1. Slices step 2: variadic `...T` parameters.
2. Owned pointers and drops, then the allocator.
3. Lifetime design for references stored in structs.
4. Enums and `match`, with `Option` as `?T`.
5. Strings, dynamic arrays, methods, interfaces, generics.
6. Port the Go compiler's fixtures from the `legacy` branch. On 2026-10-24 the
   Rust compiler accepted 19 of its 146 positive fixtures.
