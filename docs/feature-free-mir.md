# Design page: a MIR of core concepts only

> The crates have moved since this was written: the phases are now
> `crates/syntax/*` (the tree crate is `tree`), `crates/check`,
> `crates/model`, `crates/lower`, `crates/flow` and `crates/eval`.

Status: **done 2026-11-11.** Agreed 2026-11-10 (section 9) and built in
order B; the old MIR, its lowering and `flow::cleanup` are deleted, and the
MIR of core concepts is `model::mir`.

## 1. The rule this page serves

The compiler has two kinds of things:

- **The core** depends on no language feature: the parser engine, the
  checker's tables and cycle stack, the THIR builder's block and name
  machinery, the lowering machinery, and everything after lowering: the
  path checks, the interpreter, the LLVM writer, the driver.
- **A feature** is one language concept and owns its whole meaning: its
  syntax rules, its checking, and how it is written into the MIR.

**The MIR is the line between them.** It holds only core concepts: bytes,
offsets, loads, stores, calls, jumps, slot sizes and holders. Nothing after
lowering can name a feature, because there is nothing to name. Adding or
removing a feature touches the parser, check and lower, and no other crate.

## 2. What today's MIR carries, and who pays for it

Today every instruction carries a language type (`Instruction::ty`,
`crates/middle/model/src/mir.rs:71`), every variable does
(`Variable::ty`, `:46`), and eight of the twenty instruction kinds are
language features: `Length`, `Borrow` of a slice, `ToInterface`,
`Aggregate`, `ArrayParts`, `Print`, `Field`, the typed `Binary` and
`Convert` (`:176-268`). Every reader re-derives machine facts from the
types:

| Reader | `Type::` matches | What it derives again |
| --- | --- | --- |
| `middle/eval` | 86 | widths, signedness, sizes (`layout`), copy or move, printing |
| `backend/llvm` | 89 | the same, plus the C ABI, debug info, printing |
| `middle/flow` | 11 | holders per type, loan words, mutability words |

Measured 2026-11-10 (`target/middle-baseline/interpreter-measure.txt`):
v4's interpreter, which reads a MIR of this page's kind, runs the same
programs 3.3 to 4.9 times faster than main's. Main's profile shows where
the time goes: `model::layout::layout` 7.6%, bytes to `Limbs` and back
5.5%, width-generic bigint arithmetic 4.5%, `Body::operand_type` 1.5%,
malloc and free 2.5%. None of this work exists when the MIR says the class
and the size.

The owned-interfaces change (`daf50434`) touched 22 files in 6 crates. With
this page, the same feature touches check and lower.

## 3. The MIR

Taken from v4's (`v4/core/model/src/mir.rs` on `feature/compiler-v4`), with
what main's language needs and v4 never had: unsigned integers, 128-bit
integers, floats, dynamic indexing, extern calls under a C ABI, read-only
data, and a debug-type table.

### 3.1 Registers and classes

A value an instruction gives lives in a register of one class:

`I8 I16 I32 I64 I128 Wide(bits) F32 F64 Pointer`

Lowering picks the class from the type once: `usize` and `isize` are the
pointer-wide integer class of the `Machine` (`pointer_bits`), `bool` is
`I8`. An integer wider than 128 bits (Peeper allows up to 2^23 bits:
`i200`, `u256`) is `Wide` of its exact width, as LLVM has `i200`; its
operations wrap there, and its constants are read-only data, loaded.
Signedness is not a class: it is in the operation (`SignedDivide`
against `UnsignedDivide`), as machines do it. A type narrower than its
class (`i24` in `I32`) is kept extended to the class; after an operation
that may carry past its width, lowering cuts the value back. Anything that is not one
register (a struct, an array, a slice's two words, an interface value's two
pointers, a range) lives in memory and is handled by its address.

### 3.2 Slots

A function's memory is slots: a parameter, a `let`, or a temporary. A
parameter passed in memory is the caller's private copy, at the address it
passed (`Body::parameter_in_memory`), so it is never copied again.

```text
Slot { size, align, holders: [Holder { offset, words }], debug_type: Option<DebugTypeId>,
       span, mutable, hidden }
```

`holders` are the byte offsets of the pointers in the slot that may hold a
loan (v4's `Slot::holders`), each with the `LoanWords` a message names it
by ("reference used later here"). `Body::returned` is how a message names
what the function returns ("a slice of"), `None` when it holds no loan. Today the loan check walks the type for this
(`Types::holders_in`, `semantic_types.rs:912`); here lowering writes it
once and the check reads it.

### 3.3 Places

`Place { root, steps }`: a root, then steps. Root is a slot or the memory
at an address an operand gives. Steps are:

- `Offset(bytes)`: a field, a word of a slice, an element at a known index;
- `Through`: what the pointer here points to (a reference, a raw pointer,
  a slice's elements);
- `Index { index: Operand, size, count }`: the element `index` of `size`
  bytes, which lowering has already checked to be inside, of `count`
  elements from here (`None` for a slice's elements, which only the
  program counts). Main's `Projection::Index` (`mir.rs:97`) is this with
  the element size written down. v4 had no dynamic index (arrays were
  paused).

The path checks see a place in a slot as a byte range: `Offset`s and
constant indexes add up, an index only the program knows covers its
`count` elements, and `Through` leaves the slot. Two accesses conflict
when their ranges meet, and a place's holders are those inside its
range. Each access says how many bytes it touches: a class's width,
`Copy`'s, `Borrow`'s, `Moved`'s and `Address`'s `size`, a slot's size
for `Dead`. A slice of an array covers from its start to the array's
end, so a write before the start is allowed while the slice is in use.

### 3.4 Operands

`Value(id)`, `Constant { class, bits: [u64; 2] }` (128 bits, so `i128`
fits), `Parameter(n)`, `Function(id)`, `Global(id)`.

Globals are read-only data: string bytes, panic messages, and interface
tables (an array of function addresses). v4's `Global { parts }` with
byte and function-address parts covers all three.

### 3.5 Instructions

Those that run:

| Instruction | Meaning |
| --- | --- |
| `Integer { operation, class, left, right }` | add, subtract, multiply, signed and unsigned divide and remainder, shift left, signed and unsigned shift right, and, or, xor; wraps at the class |
| `Float { operation, class, left, right }` | add, subtract, multiply, divide, remainder on `F32`/`F64` |
| `Compare { comparison, class, left, right }` | equal, not equal, signed less, unsigned less, float less, and their or-equal forms; gives an `I8` |
| `Convert { kind, from, to, value }` | sign extend, zero extend, truncate, integer to float (signed or unsigned), float to integer (saturated at the integer's own width, which may be below its class's), float widen or narrow, pointer cast (a raw pointer as one to other bytes) |
| `Load { class, place }` | one register from memory |
| `Store { class, place, value }` | one register to memory |
| `Copy { to, from, size }` | `size` bytes between two places that do not overlap |
| `Address { place, size }` | the address of the `size` bytes at `place`; no loan |
| `Call { callee, arguments }` | `callee` is a function id, a runtime symbol (`runtime_abi::symbols`), or an extern function; its signature says each parameter's class, or `Memory { size, align }` for a value passed by its address |
| `CallThrough { signature, pointer, arguments }` | a call of the function at `pointer` |
| `Check { failed, message }` | stops the program with `message` when the `I8` is not 0 |

The events, for the path checks only, removed before the backends run:

| Event | Meaning |
| --- | --- |
| `Borrow { place, size, mutable, words }` | a loan of `size` bytes from `place` (`None`: all there are, a slice's elements); its value is the address. `words` are the `LoanWords` the message uses (a reference's or a slice's: noun, `&mut` form, "to"/"of"), chosen by lowering from the type |
| `Moved { place, size, lent_again }` | the value of `size` bytes just read at `place` is moved out; `lent_again` is the step a message adds to lend it again instead (`.*`, `[..]`), for a `&mut` value |
| `Dead(slot)` | the slot's storage ends |

Terminators: `Jump`, `Branch { condition, span, then, otherwise }`,
`Return { value, span }`, `FallOff(span)`, `Panic { message: GlobalId,
span }`.

What is deliberately **not** here: `Phi`. Today only `&&` and `||` make
one (`mir.rs:255`). Lowering writes them as a store and a load of a hidden
`I8` slot; LLVM's own passes turn that into a phi in release builds, and
the interpreter and the path checks have one case fewer.

### 3.6 Debug types

LLVM's debug info needs names, sizes and member offsets. Lowering writes
them into a table beside the MIR, keyed by `TypeId` so one type gets one
entry:

```text
DebugType = Basic { name, bits, encoding }
          | Pointer { to }
          | Composite { name, size, members: [(name, offset, type)] }
          | Array { element, lengths: [u64] }
```

Everything `llvm/src/debug_info.rs:95-240` writes today fits these four
shapes: a slice is a composite of a pointer and a length, an interface
value a composite of two pointers, a range a composite of its fields. The
LLVM writer reads the table and never sees a language type.

## 4. From today's instructions to the new ones

| Today (`mir.rs:176-268`) | Lowering writes |
| --- | --- |
| `Load(place)` of a register type | `Load { class }` |
| `Move(place)` of a register type (`&mut T`) | `Load { class }`, then `Moved` |
| `Load`/`Move(place)` of a memory type | the place itself is the value; `Moved` for a move; a `Copy` where it is stored |
| `Length(place)` | `Load { Pointer-wide, place + Offset(pointer_bytes) }` |
| `Store { place, value }` | `Store` or `Copy` |
| `Borrow { place, mutable }` of a reference | `Borrow { words: REFERENCE_WORDS }`; its value is the address |
| `Borrow` of a slice `&a[i..j]` | the bounds `Check`s, then `Borrow { words: SLICE_WORDS }` of the elements, then two `Store`s (address, length) into the slice slot |
| `Address(place)` | `Address` |
| `Binary { op }` on integers | `Integer` or `Compare`; division and shifts are preceded by a `Check` with the message from `runtime_abi::messages`, which the backends trap on today |
| `Binary` on floats or bools | `Float`, `Compare`, or `Integer` on `I8` |
| `Unary { op }` | `Integer` with a constant (`0 - x`, `x xor -1`), or `Compare` with 0 for `!` |
| `Convert(value)` | one `Convert { kind }`; `Types::conversion` (`semantic_types.rs:819`) decides the kind once, in lowering |
| `Call { callee: Function }` | `Call`; a memory-class argument is passed as the address of a copy |
| `Call { callee: Method(n) }` | `Load` the table pointer from the interface value, `Load` the function at `Offset(n * pointer_bytes)`, `CallThrough` |
| `ToInterface { reference, implementation }` | two `Store`s into the interface slot: the address, and `Global(table)` |
| `Aggregate(parts)` | a `Store` or `Copy` per part at its field offset |
| `ArrayParts(parts)` | the length `Check` from `Integer` sums, then a `Store` or `Copy` per element; a spread of a slice or range is a counted loop of blocks |
| `Print { values }` | a `Call` of the runtime's print symbol for each value's class; a struct or array does not print (section 11) |
| `Field { value, fields }` | the aggregate temporary is a slot, so a `Load` at the field's offset |
| `Phi` | a hidden `I8` slot (3.5) |
| `Check` | `Check` |
| `ScopeEnd(variable)` | `Dead(slot)` |
| `Terminator::Panic { message }` | `Panic` with the message as a global |

The interface table itself is a `Global` of function addresses, one per
`ImplementationId`, written by lowering from `Types::implementation`.

## 5. What each phase becomes

**Lowering** (`middle/lower`, 1,370 lines today) grows: it owns layout
(`model::layout`, already there), the class of each type, holders, debug
types, the C ABI classes of extern signatures, interface tables, printing
sequences. It is the only phase after check that reads `Types`. Estimate:
2,500 to 3,000 lines.

**Path checks** (`middle/flow`, 2,004) read slot holders instead of
walking types; `origins` (the parameters a returned pointer may point
into, `semantic_types.rs:192`) move onto each `Signature` in the MIR, so
a call carries its callee's; the mutability words in `variable_state.rs:345` come with the event
as `Borrow::words` does. Estimate: about the same size, 11 type matches
gone.

**Interpreter** (`middle/eval`, 2,578) is rewritten on v4's
(`v4/backend/eval/src/lib.rs`, 291 lines): byte memory, registers as
`u128` so `I128` fits; a `Wide` register holds a big integer, the one
place bigint stays. Every rule of a constant's run stays a
rule at its operation, as today (`eval/src/interpreter/instructions.rs:61,
73, 102, 144`, `places.rs:39`, `calls.rs:233`): `Integer` on the pointer
class, `Compare` of two non-null pointers, a pointer cast of a non-null
pointer, a runtime call, a call of a function with no body, each refused
with today's message; per-variable blocks stay. Estimate: 800 to 1,000
lines.

**LLVM writer** (`backend/llvm`, 4,191) is rewritten on v4's
(`v4/backend/llvm/src/lib.rs`, 687 lines) plus: floats, `i128`, unsigned
operations, the C ABI for `Memory` parameters of extern and runtime calls
(`byval`/`sret` as the writer before step 13 wrote them, chosen by
`Machine::c_abi`), globals, debug info from the table. Estimate: 2,000 to
2,500 lines.

**Check** and the **parser** do not change in this step. The driver and
the LSP do not read the MIR (`driver/lsp`: no `mir` use) and do not change.

Net: roughly 3,000 fewer lines, and three crates that cannot name a
feature.

## 6. Target widths

All target knowledge sits in lowering and `Machine`: the pointer-wide
class, `i64_align` into slot alignment (`layout.rs:28, 35`), the C ABI.
Backends read classes, sizes and alignments. The i686 fixtures
(`run_i686_*`) check this as they do today.

## 7. What changes for the fixtures

- `-dump mir` (19 fixtures): the text changes once and is re-blessed after
  review; the new dump reads as `%3 = load i64 p[Offset(8)]`.
- `-dump ll` (13 fixtures): the goal is identical text; the C ABI and the
  debug metadata are written from the same facts. Where it differs, the
  difference is reviewed, not blessed blindly.
- `run` (95) and `check` (97): identical output and exit codes, checked by
  `target/middle-baseline/compare.sh`.

## 8. Order of the change

The MIR has one producer and four readers (flow, eval, llvm, the dump), so
the readers cannot switch one at a time against one MIR. Two ways:

**A. One branch, one swap.** Build the new MIR, lowering, path checks,
interpreter and LLVM writer on `feature/core-mir`, each step compiling and
tested on its own, and switch the driver at the end. Intermediate commits
carry code without a production consumer until the swap. Fewer moving
parts; one long stretch without fixture proof.

**B. Two MIRs alive for a few steps.** Add the new MIR and lowering beside
the old; switch `run -interpret` and compile-time runs to the new
interpreter first (v4's 291 lines; compared against the old interpreter on
all 95 run fixtures); then the LLVM writer; then the path checks; then
delete the old lowering, MIR, interpreter and writer. Each step is proved
by the fixtures. The cost is a tracked migration state with a named end.

Recommended: **B**, with the removal step written in the plan before the
first commit.

## 9. Decided (2026-11-10)

1. `&&` and `||` lower to a hidden `I8` slot; the MIR has no `Phi`.
2. Struct and array printing is written inline at each `print`.
   Replaced by section 11: structs and arrays do not print.
3. The name stays `mir`; the crate layout stays `middle/model`,
   `middle/lower`, `middle/flow`, `middle/eval`, `backend/llvm`.
4. Order B: two MIRs alive for a few steps, the interpreter switched
   first, then the LLVM writer, then the path checks, then the old
   lowering, MIR, interpreter and writer deleted. The removal step is in
   the plan before the first commit.

## 10. Not in this page

- Moving check's and lower's logic into one file per feature (the dispatch
  rule). Separate step; it does not change any output.
- Generics, enums, `match`, true interfaces: each is a feature on top of
  this MIR and should need nothing new in it. That is the test of the
  design; the first of them to need a new instruction says where the line
  was drawn wrong.

## 11. Decided (2026-11-11): structs and arrays do not print

This changes decision 9.2. Printing a struct inline at each `print`
writes one print per number in it, so a struct that holds another twice
doubles the code at each level (`peeper build` at depth 18: 1.4 s and
400 MB). A printer function per type would fix the size, but printing
aggregates was only a stopgap for a language without interfaces.

So `print` and `println` take numbers, `bool`, `&cstr`, `&str`, raw
pointers and references to them (`printable` in `check`), and `Print`
lowers to one runtime call per value. A program prints a struct's
fields or an array's elements one by one. Later, `print` becomes a
library function and a type prints by meeting an interface.
