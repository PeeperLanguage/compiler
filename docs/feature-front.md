# Design page: a front split by feature

Status: **draft for review, 2026-11-13.** Step 17 of
`middle-cleanup.localplan.md`. No code is written for steps 18-20 until this
page is agreed. The numbers come from scripts in
`target/step12-probes/front/` run on `745b61ce`.

## 1. The problem

One language concept is spread across every phase before the MIR. Ranges
(`range[T]`, `a..b`, `for i in a..b`) live today in 17 source files in five
crates:

| Crate | Files |
| --- | --- |
| parser | `expr.rs`, `dump.rs` |
| model | `ast.rs`, `thir.rs`, `semantic_types.rs`, `layout.rs` |
| check | `declaration.rs`, `expression.rs`, `expression/literals.rs`, `expression/places.rs`, `statement.rs`, `statement/loops.rs` |
| lower | `aggregates.rs`, `expression.rs`, `lib.rs`, `statement.rs` |
| eval | `interpreter.rs` (`constant_refusal`) |

A change to ranges is a change to all of them, and nothing tells the person
making it which ones. The reverse is as costly: 68 functions in the front
and `model` match the types of two or more features (section 3), so a
change to any one of those features reaches into code that is "about"
another.

The MIR work (`feature-free-mir.md`) already fixed this for everything
after lowering. This page does it for everything before: the parser, the
checker, THIR and lowering.

## 2. The rule

Decided 2026-11-11 (Fuad):

- **The core** holds concepts that have no syntax: integers of every width,
  `bool`, `void`, invalid, places and variables, functions (one concept with
  `owner: Option<TypeId>`: a free function has none, a method has its type,
  an interface method its interface and no body), calls, signatures,
  bodies, layout. It also holds the machinery: the lexer and parser engine,
  the checker's tables, scopes and cycle stack, the THIR builder, the
  lowering builder, and everything from the MIR on.
- **A feature** is syntax plus its rules on top of core concepts: the name
  `i32` and its literals, `true` and `&&`, `struct` and its literals.
- **Features plug in through core traits**, not through enums the core
  lists (this reverses the 2026-11-10 closed-enum choice for the front).
- **Features never depend on each other.** The test: delete one feature's
  folder, and everything else still builds.

This page adds one rule, the one that decides overlaps (section 4):

> **Two features meet only through a core concept.** A core trait asks a
> type what it *is* (its numbers, its elements, its members, what it views),
> never what to do with a piece of syntax.

## 3. What the front looks like today

### 3.1 Central matches

These functions match six or more kinds of one node enum, so every feature
with a node of that kind edits them:

| Function | Enum | Kinds |
| --- | --- | --- |
| `check::expression::check_expr` | `ast::Expr` | 30 |
| `syntax::ast::precedence` | `ast::Expr` | 29 |
| `parser::dump::expr` | `ast::Expr` | 29 |
| `model::thir::write_expr`, `any_part` | `thir::ExprKind` | 20 each |
| `lower::expression::lower_expr` | `thir::ExprKind` | 20 |
| `check::declaration::resolve_type`, `names_type_parameter` | `ast::Type` | 17 each |
| `check::statement::check_statement`, `parser::dump::stmt` | `ast::Stmt` | 14 each |
| `model::thir::write_statement`, `lower::statement::lower_statement` | statements | 11 each |
| `lower::expression::lower_into` | `thir::ExprKind` | 8 |

The enums themselves live in `model` and name every feature:
`ast::Expr` (29 kinds), `ast::Stmt` (20), `ast::Type` (21), `thir::ExprKind`
(20), `semantic_types::Type` (18).

### 3.2 Functions that match several features' types

68 functions match the `semantic_types::Type` of two or more features. They
fall into three groups.

**Every type answers a question about itself** (one arm per type; the
largest):

| Question | Functions |
| --- | --- |
| How it is written in messages | `Types::write_real_text` (11 features), `fixed_doc` |
| Size, alignment, offsets | `layout` (11), `in_memory`, `field_placed` |
| Which register class holds it | `lower::class` (11), `c_abi::c_passing`, `c_signature` |
| Its debug type | `lower::debug_type` (11) |
| Whether a copy is a move | `Types::is_copy` (11) |
| Which loans it can hold | `holds_loans`, `holders_in`, `add_holders`, `loan_holder_text`, `mark_moved` |
| Its value as a compile-time constant | `ConstantShape` (`check::function`), `constant_refusal`, `semantic_types::value_text` |
| How it prints | `check::printable`, `lower::print_value` |

**One piece of syntax over several types:**

| Concept asked | Functions |
| --- | --- |
| Is it a number, and which | `is_number`, `check_number`, `takes_type`, `check_unary`, `lower_unary`, `lower_binary`, `fold::operators::binary`, `fold::operators::unary`, `fold::conversions::convert`, `arithmetic::binary_operation`, `arithmetic::cast_conversion` |
| Its elements and their count | `elements`, `count_spread`, `lower_array_literal`, `part_count`, `lower_slice`, `lower_index`, `check_for_array`, `help_spread`, `check_variadic`, `check_field` (`.len`) |
| What it views and how | `pointee`, `borrows`, `is_slice_shaped`, `reborrow`, `place_root`, `check_slice`, `lower_borrow` |
| Its members and functions | `check_call`, `method`, `implementation`, `check_type_declaration` |
| Its written form | `resolve_type`, `names_type_parameter`, `resolve_signature_type`, `reference_type`, `method_type` |

**An operation across two features:** `Types::conversion` (9 features), and
`check_binary`'s raw pointer `+` with an integer. Section 4 splits each.

The codebase already asks by concept in places: `Type::elements`,
`Type::pointee`, `Type::borrows`, `Type::is_number`, `Types::is_copy` and
`layout` have about 60 callers between them. The design grows from these.

### 3.3 The parser is already table-driven

`parser/src/expr.rs` (`RULES`: a prefix rule, an infix rule with its
precedence, and a binary operator per token), `stmt.rs` (`STATEMENTS`) and
`decl.rs` (`DECLARATIONS`) are static arrays indexed by token kind, built by
`const` code. A feature filling its own rows is a small change.

## 4. Overlaps

Three kinds, from easy to hard. v4 (`feature/compiler-v4`, `v4/core/gateway`)
met all three; what it did is the evidence for each choice.

### 4.1 Two features start with the same tokens

`&T` and `&[]T`; `[N]T` as a type and `[N]T{...}` as a literal; `.name`, `.*`
and `.{`. A rule is keyed
by its grammar position, its first token and, when needed, the token after
it. Two features claiming one key is a build error, never a rule silently
replaced. v4 did exactly this (`rules.rs`, `RuleTable::build`) and it held.

Precedence levels stay one core enum (`syntax::operators::Precedence`:
`LogicalOr`, `Equality`, `Shift`, ...); a feature picks one. Only a new level touches the core.

### 4.2 One piece of syntax, a meaning per type

`.` (field, method, `.len`, `.*`), `x[i]` (array, slice), `for` (range,
array, slice), `+`/`==` (integers, floats, bools, pointers).

v4 gave each piece of shared syntax a neutral owner (`members` owns `.`)
that asked the type through a `TypeKind` method named for the syntax:
`field`, `call_method`, `operator`, `unary`, `convert`, `reborrow`,
`pointee`, `push`. Each new shared piece of syntax added a method, so the
core trait grew with the syntax.

Here the owner of the syntax asks a concept instead (section 5.2): `x[i]`,
`.len` and `for` read `elements`; `.name` reads `members`; `+` and `==`
look up the operation the operand's type registered (5.3). New syntax over
existing concepts touches only its feature.

### 4.3 One operation across two features

Each case in today's code, and the core concept it goes through:

| Operation | Today | Through |
| --- | --- | --- |
| `f as i32`, `bool as u8`, implicit widening | `Types::conversion`, number arms | `number` on both sides; the rule is core |
| `&mut T` as `&T`, `&[N]T` as `&[]T`, `&mut []T` as `&[]T` | `conversion`, `Reborrow` | `view`, and `elements` for the array |
| `&S` as `&Shape`, `S` as `Shape` | `conversion`, `ToInterface` | `members`: functions with owner `S`; `view` |
| `*T` as `rawptr`, `rawptr` as `*T` | `conversion` | `view` |
| `p + n` for `rawptr` `p` | `check_binary` | the raw pointer feature's `+` (5.3), asking `number` of `n` |
| a struct of arrays, an array of structs | `layout` | `layout` asks each part |

If two features must meet and no concept exists, the concept is added to
the core on purpose: the only time the core changes for a feature.

## 5. The core

### 5.1 Concepts

As decided: integers of every width, floats, `bool`, `void`, invalid;
places and variables; functions with an optional owner; calls,
signatures, bodies; layout. The MIR's own concepts (registers and their
classes, slots, places, loans and holders, debug types) are already core.

**Numbers** (decided 2026-11-13): integers and floats are one core
concept, asked through one view, `number()` (5.2), which answers
`Number::Integer { bits, signed }` or `Number::Float(Float)`. Floats join
because the MIR has float classes, conversions between numbers are one
core rule (4.3), and every operator rule but a few (`%` and shifts on
floats, the bit operators) is shared. Today a number is two variants,
`Type::Integer` and `Type::Float`, and the code that asks "is it a number,
and which" matches both by hand in the functions of 3.2's first row;
`Type::is_number` has four callers, all in the operator checks. Each of
those asks `number()` instead, so the list of which types are numbers
lives in one place. The view changes no storage: the type table, dumps and
fixtures stay as they are. A bound a program can write (a `number` bound
on a type parameter) is not part of this: no program needs one yet, and
`const T: type` copies are checked one by one, so a wrong type is already
reported at its copy. When the `<T>` generics bring bounds, a `number`
bound is one question, whether `number()` answers.

Also decided (2026-11-13), each because section 3 found code that needs
it without a feature:

- **views**: a value that refers to a place elsewhere, with or without
  loans (references, slices, interface references, raw pointers). The MIR
  already has borrows and holders.
- **elements**: a run of values of one type, with a length known now or
  when the program runs (arrays, slices, text, ranges' integers).
- **members** (decided 2026-11-13, step 19): what a name after `.` means on
  a type, a field or a function with the type as owner. `.name` stays core
  syntax (`ast::Expr::Selector`), as `ast::Field` and `ast::FieldValue`
  do: a method call (`p.m()`), an assignment target (`p.x = 1`) and
  `&mut a.len` all read it, and they belong to other features, which
  cannot name a members feature. A call would also need a "method of a
  value" in the THIR, which does not exist. The core's `.name` check asks
  the type's `members` (5.2) once the core no longer names the struct
  type.

### 5.2 What a type answers

A sketch; names and exact signatures are settled in step 19:

```rust
/// What a type is, asked by the core and by any feature.
pub trait TypeShape {
    // Facts every type has.
    fn text(&self, types: &Types, out: &mut String);
    fn layout(&self, types: &Types, target: &Machine) -> (u64, u64);
    fn class(&self, types: &Types, target: &Machine) -> Option<Class>;
    fn debug_type(&self, types: &Types, debug: &mut DebugTypes) -> DebugTypeId;
    fn is_copy(&self, types: &Types) -> bool;
    fn holders(&self, types: &Types, found: &mut Vec<Holder>);

    // Views; `None` when the type is not one.
    fn number(&self) -> Option<Number> { None }      // Integer { bits, signed } or Float (5.1)
    fn elements(&self) -> Option<Elements> { None }  // element type, length now or later
    fn members(&self) -> Option<&[Member]> { None }  // name -> offset and type, or a function
    fn view(&self) -> Option<View> { None }          // target, mutable, loans followed

    // The operations its feature registered (5.3); `None`: not supported.
    fn operation(&self, op: Operation) -> Option<&dyn OperationLogic> { None }
}
```

This trait is about as long as v4's `TypeKind`. The difference is what
makes it grow: every method is a concept the MIR or the core already has,
so new syntax never adds one.

Constant values (`eval::constant_of`) and printing (`printable`,
`print_value`) are left out on purpose: a constant holds only what has a
register class, which `class` and `number` answer; and printing becomes a
library function (decided earlier), so its per-type match goes then.

### 5.3 Operations (decided 2026-11-13)

The core keeps, for each type, the list of operations it supports, and
implements none of them. The operations are the language's unary and
binary operators (today's `UnaryOp` and `BinaryOp`). A feature implements
each operation on its own types (how the other operand is checked, the
result's type, the MIR it lowers to) and adds it to that type's list. The
core only looks up: for `a + b` it finds `+` on the type of `a` and hands
the expression to the feature that registered it. When `+` is not listed,
it reports the one error every operator shares (today's
`report_invalid_operand`: "operator `+` cannot be applied to `bool`").

| Type | Its feature registers |
| --- | --- |
| integers | `+ - * / %`, `<< >>` (the count any integer), `& \| ^`, `~`, negation, `== != < <= > >=` |
| floats | `+ - * / %`, negation, `== != < <= > >=` |
| `bool` | `&& \|\|`, `!`, `== !=` |
| `*T` | `== !=` |
| `rawptr` | `== !=`, `+` with a `usize` |

- A feature registers operations only on its own types, so no pair of a
  type and an operation has two owners. An operation over two types
  (`rawptr + usize`) belongs to its left operand's type.
- `rawptr + n` (formerly question 3) is the raw pointer feature's: its `+`
  checks that the right side is a `usize`, asking `number` (5.1), and
  lowers to the address that many bytes further on. No core concept is
  added for it.
- Bools implement the short circuit of `&&` and `||`: not running the
  right side is what those operations do.
- What is left to the operators feature is syntax: precedence, `none == p`
  read as `p == none`, an unsuffixed literal taking the other side's type,
  and the lookup with its error.

**Folding (decided 2026-11-13).** There is no separate constant folder. A
constant's operation (`const X = 2 + 3;`) is folded by running the MIR its
feature lowers it to through `eval`, as a constant's call already is. So
an operation has one meaning, at compile time and at run time, and a
folded value cannot differ from a computed one (the `-NAN` case of step
18a). A trap the folder finds today (a constant division by zero) is the
run's failure, reported where it is written. `check::fold` goes when the
operations become features'. What folding through `eval` costs is
measured first (section 10).

### 5.4 Not covered yet

- **Written types** (`resolve_type`, 17 kinds of `ast::Type`): this is
  syntax, so each feature owns the rows for its type syntax, and the core
  resolves names and generic arguments.

## 6. Features

The list, from today's syntax (decided 2026-11-13: all 22 kept; members
became a core concept in step 19, 5.1). Each owns
its rows in the parser tables, its node kinds, its checking and its
lowering:

| Feature | Owns |
| --- | --- |
| numbers | `i8`...`u128`, `isize`, `usize`, `f32`, `f64`, number literals and suffixes, `as` between numbers; their operations (5.3) |
| bools | `bool`, `true`, `false`; its operations, the short circuit of `&&` and `\|\|` included (5.3) |
| operators | precedence; looking up each operation on its operand's type, and the one error when it is not listed (5.3) |
| text | `str`, `cstr`, string literals |
| references | `&T`, `&mut T`, `&x`, `.*` |
| raw pointers | `rawptr`, `*T`, `@x`; their operations, `rawptr + usize` included (5.3) |
| arrays | `[N]T`, array literals, `...` spreads |
| slices | `&[]T`, `&x[a..b]` |
| ranges | `range[T]`, `a..b`, steps |
| indexing | `x[i]`, `.len`, through `elements` |
| structs | `struct` declarations, literals |
| interfaces | `interface` declarations, interface references, owned interfaces |
| methods | `Owner::f`, `x.m()`, receivers |
| functions | `fn` declarations, call syntax, `return`, `const` parameters |
| bindings | `let`, `const`, assignment, compound assignment |
| conditionals | `if`, `else` |
| loops | `loop`, `for`, `break`, `continue` |
| C interop | `extern`, variadic `...` parameters |
| type declarations | `type` aliases and named types |
| panic, print | `panic`, `print`, `println` (print becomes a library function later) |
| reserved | syntax the parser reads and the checker refuses today (`'c'`, variants, `is`, `orelse`, block values, `\|>`, `match`, enums, destructuring): kept as one feature so its messages stay the same |

Tokens stay core: the lexer must know every token, so a new keyword is one
line in the token list, plus its feature.

## 7. Node representation

The decision that matters most. `ast::Expr`, `thir::ExprKind` and
`semantic_types::Type` are closed enums in `model` that name every feature;
something must replace them. Two options:

**A. A core handle, the feature's node behind a trait object.**

```rust
// core
pub enum Expr { Name, Path(Id<Path>), Call { .. }, /* core kinds */ Feature(FeatureNode) }
pub struct FeatureNode { pub feature: FeatureId, pub node: u32 }
pub trait ExprFeature: Sync {
    fn check(&self, node: u32, builder: &mut ThirBuilder) -> thir::Expr;
}
// each feature: its own typed arena, kept in a per-module store
```

- A new feature is a folder and one entry in the feature list.
- Each feature keeps its nodes in its own arena, so the core must keep
  something it cannot name: in practice a `Box<dyn Any>` per feature that
  the feature downcasts to its own arena. That is v4's `state<T>()` again,
  confined to "a feature reads only its own storage".
- An indirect call per feature node on the checker's and lowering's hot
  paths.

**B. A core generic over one language type, the list of features in a top
crate.**

```rust
// core
pub trait Language: 'static { type Expr: ExprFeature<Self>; type Type: TypeShape; /* ... */ }
pub enum Expr<L: Language> { Name, Path(Id<Path>), Call { .. }, Feature(L::Expr) }
pub struct ThirBuilder<'c, L: Language> { /* ... */ }

// a crate above every feature (the driver level)
features! { numbers, bools, structs, arrays, /* one line per feature */ }
// expands to: enum Expr { Structs(structs::Expr), ... } and the delegation
```

- A new feature is a folder and one line in `features!`.
- Every node is typed; dispatch is a `match` the compiler checks; no `Any`.
- The core never names a feature (it sees only `L`); features never name
  each other (they see only the core); only the top crate names them all.
- Every core type and function carries `<L>`. That is noise in the core,
  and the compiler itself builds slower (one copy of each generic, since
  there is one `Language`). Building the compiler is the lower priority.

**Decided (2026-11-13): B.** It keeps the type checking that v4 lost with
`Any`, and its costs fall where the priorities say they may.

Both options need the same thing from the core: the THIR and lowering
builders' helpers (`add_expr`, `resolve`, `report`, `temporary`, `store`,
`emit`, `call`, blocks) become the core's public API, which features call.
v4's `gateway::lower` had this API (`emit`, `temporary`, `new_block`,
`branch`, `declare`); it was the part that worked.

**Built (step 19, piece 2).** Each phase names what it asks of a feature
in its own trait, so a phase depends on no later one:
`syntax::ast::Language` (the feature nodes, `precedence`, `fields`),
`parser::Grammar` (the rule table) and `check::Checked` (`check_expr`,
`resolve_type`, `declare_type`). The table is built by `const` code in the
top crate, so two rules for one token fail the build. A feature claims a
declaration keyword, a type keyword (`Rules::keyword_type`), an expression
prefix, or what continues a path (`Rules::after_path`: `Point.{`). There is
no `features!` macro while structs are the one feature: `Peeper`'s nodes
are the struct crate's own. A field and a field's value stay core nodes
(`ast::Field`, `ast::FieldValue`), since the core reads fields too
(`point.x`, the field names `model::program::field_names` gives).

## 8. The boundary after lowering

`feature-free-mir.md` made the MIR the line, but five places past it still
read the front's types. They move before any feature moves (step 18a):

| Where | What it reads | Fix |
| --- | --- | --- |
| `llvm/src/text.rs::copy_symbol` | `Types`, `ConstArgument` to name a copy with `const` arguments | the front makes the name and hands it down with the body: `model::program::copy_symbol` and `copy_name`, called by the driver |
| `eval/src/interpreter.rs::constant_refusal`, result typing | `Type::Struct`, `Array`, `Range` and the scalar types | the checker gives the run each argument's and the result's `ConstantShape` (`Bool`, `Integer`, `Float`, ..., or `Refused` with its reason); `constant_refusal` moves to the checker |
| `eval/src/operators.rs`, `conversions.rs` | the checker's constant folder, on `semantic_types` | moves to the checker (`check::fold`); the arithmetic itself stays in `eval::operations` |
| `model/src/mir.rs::binary_operation`, `cast_conversion` | the front's `BinaryOp` and `Type` | moves into lowering (`lower::arithmetic`) |
| `model/src/mir.rs::DebugTypes` | a map from the checker's `TypeId` | the map moves into lowering (`lower::ProgramDebugTypes`); the MIR's table is read by id only |

The core of `model` those crates read (`mir`, `machine`, `constant::Value`
and the ids in `lib.rs`) reached the checker's types too, so three more
moved: `ConstArguments` (the id in `FunctionCopy`) to `lib.rs`, the widest
integer to `machine::MAX_INTEGER_BITS`, and `Constant` and `value_text`,
which read a `Type`, to `semantic_types`.

`check.sh` checks it (done, step 18a): the core files name no front module,
and `flow`, `eval` and `llvm` name only the core. The last link, `llvm`
reading the syntax tree through `Program`, went at the start of step 19:
the driver hands `llvm` the project root, each module's file, a function's
shown name and where its name is declared, and the entry symbol, so a
generic `Program<L>` does not reach the back end. The check is a grep until
`model`'s core is a crate of its own, which Cargo checks (section 9). The second check, that no
feature crate's `Cargo.toml` names another, waits for the first feature
crate (step 19): with none, it cannot fail.

## 9. Crate layout

```text
crates/
  base/       arena, bigint, diagnostics, literal, source, runtime_abi (unchanged)
  core/
    syntax/   tokens, lexer, parser engine and rule tables, tree storage
    check/    declarations, scopes, cycle stack, the type table, the THIR builder,
              the core concepts' checking
    lower/    the lowering builder, the core concepts' lowering
    model/    THIR, MIR, layout, machine (what the phases share)
    flow/ eval/                                          (unchanged)
  features/   one crate per feature (section 6)
  backend/llvm/                                          (unchanged)
  driver/     compile, peeper, lsp, and the crate that lists the features
```

One crate per feature lets Cargo refuse a dependency between features for
free; one module per feature in a single crate needs the grep check
instead. **Decided (2026-11-13): a crate per feature**, so Cargo refuses a
dependency between features.

This also answers plan item 10: the syntax tree leaves `model`; its core
part goes to `core/syntax`, each feature's nodes to the feature.

A consequence for tests: a crate's own tests cannot use a crate that
depends on it, so the core's tests cannot write a feature's syntax. Each
core crate (`parser`, `check`) tests in a language of its own with no
feature (`Bare`). A feature's crate tests its syntax and checking in a
language of the core and itself. A core test that needs a feature only to
reach core behaviour (an `extern` function taking a struct, a type's
functions) moves unchanged to the top crate's tests
(`driver/compile/tests/`). No test is deleted: the test names before and
after a move are compared.

## 10. Gates

For every step from 18 on:

1. Every fixture's output byte-identical (`.check`, `.native`,
   `.interpret`, `.mir`, `.ll`, the parser dump), on the host and i686.
2. The boundary check of section 8, and, from the first feature crate on,
   the check that no feature crate's `Cargo.toml` names another.
3. Speed: the structs pilot is measured with
   `scripts/bench-instructions.py`, interleaved with the build before it.
   Limit (decided 2026-11-13): **no more than 1% more instructions** on the
   benchmark.
   Over it, the pilot stops and the representation is reconsidered before
   any other feature moves. Question 5.
4. Folding through `eval` (5.3): before `check::fold` goes, a program of
   many constant operations is measured against today's folder, with the
   same benchmark and limit.

## 11. Order

- **18a.** The leaks of section 8 and the boundary check.
- **18b.** The syntax tree (`ast`, `operators`) out of `model` into its
  own crate (section 9); a pure move, byte-identical.
- **19.** Structs as the pilot: the `Language` generic (section 7), the
  `TypeShape` trait with only what the core must ask of a struct type, its
  syntax rows, nodes, checking and lowering into `features/structs`, then
  the speed gate. One commit per piece, each byte-identical:
  1. `llvm` takes the files and names it reads, not `Program`.
  2. The syntax side and struct checking: `Language`, the generic tree, the
     parser table in the top crate (`driver/compile`), the struct rows and
     nodes, and the checking of a struct declaration and literal into the
     core's struct type and value. Speed measured.
  3. The type and lowering: `Type::Struct` becomes the feature's type,
     with the rest of the trait; `.name` asks the `members` view (5.1).
     Speed measured again. (Was two pieces; the checker side of fields
     needed no crate of its own once members became a core concept.)

  Then one feature per step; each adds the views of 5.2 it needs (`number`
  and `operation` with numbers, `view` with references, `elements` with
  arrays).

The traits of 5.2 are not added ahead of the pilot: while every type is a
variant of one enum, a view only renames a match, and a method with no
caller breaks `RULES.md` 1.1. A view arrives with the first feature whose
type the core can no longer name.
- **20.** Removal: the closed enums and every per-phase match of section
  3.1 are gone; no half-moved state outlives this step.

## 12. Open questions

1. Node representation: decided 2026-11-13, B (generic core, list in a top
   crate; section 7).
2. Crates or modules: decided 2026-11-13, a crate per feature (section 9).
3. `rawptr + n`: decided 2026-11-13, the raw pointer feature's own
   registered `+` (5.3).
4. Core concepts: decided 2026-11-13, numbers (floats included), views
   and elements (5.1).
5. The speed limit: decided 2026-11-13, 1% more instructions (section 10).
6. The feature list: decided 2026-11-13, all 22 kept (section 6). A merge
   such as `indexing` into `arrays` would make slices and text depend on
   arrays.
