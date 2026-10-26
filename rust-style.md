# Rust style

Patterns and fixes the project has settled on while writing the Rust compiler.
Each one records what to do, a short example, and why. Add to it whenever a
review finds a new one. Engineering rules (imports, names, re-exports) are in
[`RULES.md`](./RULES.md) section 5.

## Write each fact once

**Declare a table once and generate everything else from it.** When the same
list of items needs an enum, constants, names or lookup lists, write one
`macro_rules!` invocation and let the macro build them all. Do not keep a hand
list next to a `match` that must agree with it.

- `model/src/semantic_types.rs` `fixed_types!`: each built-in type is one line; the
  macro builds `FIXED`, the `TypeId::*` constants and the `NAMED` name list.
- `frontend/token/src/lib.rs` `tokens!`: each token kind is one line with its
  text; the macro builds `TokenKind`, `TokenKind::text`, `OPERATORS` and
  `KEYWORDS`.

Why: two lists that must agree drift apart. One line per item means adding an
item cannot be half done.

**Edit the source, then read it again.** When parsed data comes from a text
that can change, keep the text as the one source: an edit changes the text and
parses it again into new data. Do not patch the text and the data side by side.
Checking that the new data holds what was set catches a value written wrongly.

- `peeper fix` writes the fixes into the files' text, then loads and checks
  the program again (`Command::execute_check`); it never patches a syntax tree.

Why: two copies patched by hand drift apart; a reparse cannot.

**Let the compiler count.** Number generated constants with a hidden enum
instead of hand-written numbers:

```rust
enum Position { $($name,)* }
$(pub const $name: TypeId = TypeId(Position::$name as u32);)*
```

**An optional column is `$(...)?`.** A row that has no value for a column
leaves it out (`INVALID => Type::Invalid` has no name; `BOOL "bool" => ...`
has one). Use the optional part inside a nested repetition:
`&[$($(($text, TypeId::$name),)?)*]`.

**Group with one header, TOML style.** When items fall into groups, mark each
group once with a header inside the list, not every item and not a nested
block:

```rust
tokens! {
    pub enum TokenKind {
        Eof = "end of file",
        #[operators]
        ColonColon = "::",
        #[keywords]
        As = "as",
    }
}
```

The macro is one plain pattern (`$(…)* #[operators] $(…)* #[keywords] $(…)*`).
A marker on every item needs a macro that sorts one item per step and repeats
the marker; `name { … }` blocks stop the list from reading like an enum. The
cost of headers, a fixed group order, is fine when the order is fixed anyway.

## Keep facts out of conventions

**Never derive a fact from variant order.** `self as u8 >= TokenKind::Import as
u8` makes "is a keyword" depend on where a variant sits; a variant added in the
wrong place silently changes it and no compiler check catches it. Ask the
generated list (`KEYWORDS.contains(&kind)`) or an exhaustive `match` instead.

**Never derive a fact from a naming convention** (AGENTS.md section 8). The
same reason: the compiler cannot check it.

**Keep what the parser knew, and read it by name.** A literal's node holds
what its token said (`Expr::Bool(bool)`, `Expr::String { c_string }`), so no
later phase reads the text again. A rule shared by two tokens reads which one
with `p.is_either(yes, no)`, which names both and panics on any other, not
`p.at(yes)` with the other falling into `else`: registering the rule for one
more token then fails loudly instead of reading as the default.

**A word that means "nothing" is its own token, and the tree records the
absence.** `_` is `TokenKind::Underscore`, not an identifier with the text
`_`: the parser turns it into `Pattern::Wildcard`, `Pattern::Rest`,
`Length::Inferred`, or a parameter or `for` index with no name, and rejects it
anywhere else. No later phase compares a name with `"_"` or remembers to skip
it. Allowed today: `_ =>`, `.{ a, _ }`, `[_]T{ .. }`, `with _`, `for _, v in`,
`fn f(_: T)`; `let _`, `const _` and `_ = x` have no use until tuples.

**A variable the checker makes says so.** A variable's name is the text at
its span. One the checker makes (`for v in a` keeps the array and the counter
in two) has no name there, so `Variable::hidden` marks it: the IR printers
write `_` and the debug info leaves it out. Do not give it a fake span to get
a name.

**Unsafe code is forbidden** (`unsafe_code = "forbid"`), so a number cannot be
turned back into an enum variant. When code must walk a group of variants,
generate the list of them.

**Say what a span is by its type.** A name written in the source is a
`ast::Ident { span }`, read with `ident.text(&file)`, not a bare `Span`
named `name`: the type tells the reader it is an identifier and where its
text comes from. A node whose whole text is its arena span (`Expr::Name`,
`Type::Name`) needs no wrapper.

## Give each item only what it needs

Not every item needs every property. A token kind has one `text()`: a keyword's
or operator's spelling, or for a named kind (`Ident`, `Eof`) the words
messages use. It replaced `name()`, `spelling()` and a separate keyword
`match`, which repeated the same text three times. When a few callers need to
know a group, they ask the group's list.

## Fast lookups on the hot path

**Group by first byte at compile time** instead of searching a whole list. The
lexer's `by_first_byte` `const fn` builds a 128-entry table for a kind list
once, at build time; a lookup then compares only the few entries sharing the
first byte. A linear search over the 29 keywords made a large file 28% slower;
the grouped lookup cost nothing measurable.

**Measure before and after** any change on the hot path (lexing, parsing,
checking), with a large file and the minimum of 20 interleaved runs.

**Keep hot enum variants small; put rare big payloads in a side table.** MIR's
`Operand::Constant(u32)` indexes the body's `constants` instead of holding a
`Value`, so an operand is 8 bytes instead of 24 and an instruction shrinks with
it. Lowering a 20k-function file got measurably faster.

**Hand each result on; do not collect them all.** `compile::run` gives each
function's MIR to a callback as soon as it is built and drops it after, the way
Go compiles one function at a time. Keeping every function's MIR in a `Vec`
took a 20k-function file from 60 MB to 98 MB and was slower.

**Hand a cache in and get it back as a value.** `compile::run` takes the
previous compile's `Results` by value and gives new ones: cached entries
move instead of being copied, and there is no long-lived object changing in
place. The command line passes `Results::default()`.

**Pick a field with a closure when one routine serves several tables.**
`Checker::check_once` runs the check state machine for every declaration;
each caller names its table as `|module| &mut module.declared_types`, since a
field cannot be a parameter. Write the closure in the call: stored in a
`let`, its returned borrow does not get the right lifetime.

**Skip a pass that cannot find anything.** When a cheap fact proves a pass
has nothing to report, do not run it: the variable state check runs only for a
function with a `let` without a value or a move, since nothing else can be
unset or moved. It made the check free for the usual function (a 20k-function
file: +7% to +0%). Prove it from what the pass reports on: deciding by the
variables' types missed `return r.*` moving a struct out of a reference, in a
function whose only variable, the reference, copies (2026-10-26).

**Force inlining of a tiny helper every hot walk calls, once measured.**
The inliner's choices can change when unrelated code moves: taking the path
checks out of `lower` made it stop inlining `Lowering::emit` (called for each
MIR instruction), and a compile cost 10 M more instructions. `#[inline]` did
not help; `#[inline(always)]` did. Find such a helper with `perf record -e instructions:u`
on two builds and diff the functions; do not add it on a guess.

**Give the common case of a general walk its own entry, once measured.**
A plain variable is a place with no steps, but sending every variable read
through `Lowering::place` (a loop, a `Vec`, a `Result`) cost a compile about
10 M instructions; inlining it did not help. Lowering builds a variable's
`Place::whole` directly and shares the rest (the one copy-or-move choice).
Likewise the LLVM writer loads and stores a whole variable at `%vN` directly:
passing the address as a nested `Display` cost 40 M. A nested `write!` in a
hot `Display` costs more than the line it writes.

**Look ahead only where the rare case already runs.** The LLVM writer builds
a struct or array straight into the place the next store writes. Deciding
that in `write_block` for every instruction cost a program with no structs
30 M instructions; deciding it in `Function::value_address`, which only the
arms keeping a value in memory call, cost nothing (2026-10-25).

**Give LLVM a size as a number.** A copy of a struct or array kept in memory
passes its byte size from `layout`. `ptrtoint (getelementptr (T, ptr null, i32
1))` is exact on any target, but clang's optimizer reads it as an expression,
not a constant, and splits no copy by it: a struct-heavy program's `-release`
build took twice as long (2026-10-25).

**Hash a hot name table with `FxHashMap`.** The checker looks up every
non-local name a body uses, and every value it declares, in `Scope.names`.
The standard hash (SipHash) made checking each declaration cost about 1% of
a compile; `rustc_hash::FxHashMap`, as rustc uses, made it free (3,857 M to
3,802 M instructions, 2026-10-26). Source text is not an attacker, so the
standard hash's resistance to crafted keys buys nothing here. Switch another
map only once a measurement shows it is hot.

**Do not guess capacities.** Reserving MIR arenas from THIR sizes over-allocated
and made lowering slower; `Default` and normal growth won. Reserve only when the
size is known, not estimated.

## Printing and testing an IR

**An IR prints itself, beside its data, and a command writes it.**
`thir::Body::text` and `mir::Body::text` live beside their data, as
`Types::text` lives in `model::semantic_types`, and `peeper -dump [thir | mir]` writes
them to files under build/debug/dumps. A printer only tests call has no
production caller and grows inside a test module; tests call the same printer.

**Expected output lives in files, not in Rust strings.** A MIR test is an
`x_test` folder with `mode = "mir"`, its source and `expected.mir`;
`PEEPER_BLESS=1 cargo test -p peeper --test fixtures` rewrites the expected
files, and `git diff` shows what changed. rustc (`tests/mir-opt`, `--bless`)
and Cranelift (`filetests`) work the same way.

## Small rules

- **No `pub fn new()` without arguments**: write `impl Default` instead
  (clippy `new_without_default`). `model::thir::Body` does this.
- **Remove a parameter the moment it is unused.** When a change makes a
  parameter dead (the printer's `token_color(file, t)` after keyword coloring
  stopped reading the file), delete it and update the callers; do not keep it
  with an `_` prefix.
- **Name a function by everything it does.** `check` became
  `compile::run` once it also lowered to MIR; the CLI's command runner is
  `execute`, not `check`. In a compiler, "check" means the type checker (the
  `check` crate), so no other function is named that. A function that also
  runs the program is not `link`: the driver's `link` was split by goal.
- **One word, one meaning.** Beside `execute_check` (the `check` goal), the shared
  step is `compile`, not `check`. Say "body" for anything with statements,
  not "function body", when the fact holds for any body.
- **Match a mode once.** `Command::execute` matches its `Goal` once and each arm
  calls `execute_check`, `execute_build` or `execute_run`, which read top to bottom
  with the shared steps (`compile`, `print`, `link`). Scattered
  `matches!(goal, ..)` through one function hide each mode's flow.
- **No one-field struct around a value.** `thir::Const { value }` was removed:
  `consts` holds `Option<Constant>` until a body constant has a second fact.
- **Private is no `pub`, never a leading `_`.** In Rust `_name` means "unused
  on purpose" and silences the dead-code warning, so a helper that lost its
  caller would go unnoticed; clippy also flags calling one.
- **`main` only wires the parts together.** The `peeper` binary keeps
  argument parsing and the usage text in `args.rs`, turning them into a
  `driver::Command` and its `Goal` in `cli.rs`, loading, compiling and
  reporting in `driver.rs`, and the dumpers in `dump.rs`; `main.rs` gets the
  command, runs it and returns the exit code.
- **Data lives in `model`, logic in the crates that build it.** Name a data
  module by its path: `use model::ast;`, then `ast::Expr`.
- **No function around a one-line id conversion.** Lowering gives MIR the
  same ids as THIR (block `i` is debug scope `i`); it writes
  `Id::from_index(block.index())` where it needs one, as it does for
  variables, and states the rule once in a comment. A `scope(block)` helper
  added a name to learn for nothing (owner, 2026-10-22).
- **An optional part is an `Option`, not an empty string to trim.**
  `c_extension` gives `Option<&str>` and the caller matches it, instead of
  `format!("{ty} {extension}").trim_end()` hiding a missing part.
- **Never write a concatenated string into a `String` builder**
  (`out.push_str(&(a + b))`); push each part, or use `write!`.
- **A language fact's doc sits on the row that defines it.** What a keyword
  does is a `///` comment on its row of `tokens!`, what a built-in type
  holds on its row of `fixed_types!`, and what an operator does on its row
  of `operators!` (one text for every operand type, no flag to pick the
  wording); the macro gathers the lines into a `doc` function
  (`TokenKind::doc`, `semantic_types::builtin_doc`, `BinaryOp::doc`).
  Each doc has one shape: a first line that says what it does, then the
  details, then a `peeper` example at the bottom, each paragraph one line;
  hover's top block shows only the keyword or operator itself. An
  editor only lays them out. A keyword without a doc does not build. Each
  line keeps the space after its `///`, which Markdown drops (owner,
  2026-10-26, after the old Go compiler's `token/keywords.go`).
