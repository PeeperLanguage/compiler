# Design page: the compiler's layout

Decided by Fuad on 2026-11-18, after the registry pilot (kept as a stash).
Each phase can be read alone, and a developer adding a feature is led to
every place that must change by the compiler itself, never by memory,
docs or a search. The shape follows Carbon's toolchain: split by phase,
and inside a phase one list, one dispatch and one file per concept.

## The rules

1. **One list per phase.** Each phase names every kind it knows once, as
   a closed Rust `enum`: tokens (`token`), syntax nodes (`syntax/tree`
   `ast.rs`), checked types (`model` `semantic_types.rs`), THIR and MIR
   nodes (`model`). The list holds names and shapes, no logic.
2. **One dispatch per phase, exhaustive.** A phase reaches a kind's work
   through one `match` with no `_ =>` arm, so a new kind is a compile
   error at the exact place its handler goes. A dispatch function carries
   `#[deny(clippy::wildcard_enum_match_arm,
   clippy::match_wildcard_for_single_variants)]`, so a `_ =>` added to it
   later fails clippy. A concept file must be named by a `mod` line;
   `scripts/check.sh` fails on a source file no `mod` names, which Rust
   would skip without a word.
3. **One file per concept, same name in every phase.** Pipes are
   `parser/src/pipes.rs` and `check/src/pipes.rs`; to read how pipes
   work, open those two files. The dispatch files share names too:
   `expression.rs` (with `expression/literals.rs`), `statement.rs` and
   `declaration.rs` in both.
4. **Each fact about a type has one exhaustive function.** A question
   every checked type must answer (how it is laid out and held, whether
   it copies or holds loans, its text and hash) is one `match` in
   `semantic_types` or `layout.rs`, with the lints of rule 2, so a new
   type is a compile error at each. An answer is worked out when asked,
   not stored: each is one `match`. One is stored: whether a type is open
   (`docs/generics.md`), asked of every type checked, is worked out once
   when the type is interned, by the same kind of `match`. The other phases read the answers and
   do not decide them again. A `match` that asks only "is it an array?"
   keeps its `_ =>`: a new type is not an array.
5. **Conversions are decided in one file**, `semantic_types/conversions.rs`,
   by the pair of types: whether a value converts, with `as` or without,
   and the cast that does it. A pair it does not name is refused. Lowering
   and the constant folder take the cast from it and decide nothing again.
   An integer cast (`bool` is `Integer::BOOL`) is `Integer::cast_to`, which
   this file calls and lowering calls for a range's walk too: one rule,
   two callers.
6. **THIR and MIR hold core ideas only.** A concept checks into core THIR
   nodes, so lowering needs no handler for it unless it brings a new core
   idea. MIR already follows this; LLVM, borrow flow and the interpreter
   read only MIR.
7. **No runtime registry.** No table found by address, no `Any`, no type
   parameter for the language: plain enums and functions.

## Layout

```
syntax/token        the list of tokens
syntax/lexer        engine
syntax/tree         the `tree` crate. ast.rs: the list of nodes;
                    operators.rs: the precedence ladder
syntax/parser       lib.rs engine; RULES (token → parse function, built once);
                    expression.rs, statement.rs, declaration.rs, types.rs:
                    the rules; one file per concept (arrays.rs, pipes.rs, ...)
check               lib.rs engine (Checker, scopes, reports); expression.rs,
                    statement.rs, declaration.rs: the dispatch; one file
                    per concept, as in the parser
model               the data between phases: semantic_types (list, table,
                    facts), thir, mir, layout (size, alignment, how a value
                    is held), machine, constant, program.
                    semantic_types.rs holds the list, the shapes and one
                    match per question; a type with logic of its own has a
                    file in semantic_types/ (integers.rs, structs.rs, ...),
                    the small ones share primitive.rs; conversions.rs
                    decides every conversion
lower               engine and dispatch over core THIR nodes
flow, eval          MIR only
backend/llvm        MIR only
base, driver, tools shared pieces, the programs that run the phases, tools
```

The `features` crate is gone: a feature's parse half is a concept file of
the parser, its check half one of the checker.

## Adding a feature

| Step | Where | Found by |
| --- | --- | --- |
| a token, if new | `token` | the developer |
| a precedence rung, if new | `tree/operators.rs` | the developer |
| the node | `tree/ast.rs` | the developer: the start |
| how it is read | `parser/src/<concept>.rs`, one line in `RULES` | the developer |
| its precedence, dump, check | the exhaustive `match`es | compile errors |
| how it is checked | `check/src/<concept>.rs` | the dispatch arm calls it |
| a new type | `semantic_types.rs`, then each fact it must answer | compile errors |

## Measures

Each step reports these before and after:

- places to touch to add a node, and how many of them a compile error finds;
- facts every type must answer that still have a `_ =>`, and decisions
  about types written in two places;
- crates that name a concept's node or type.

## Steps

1. Dissolve `features`: concept files in `parser` and `semantic`, one
   `parser::RULES`, no `CheckHooks`; the tree's `Feature*` wrappers are
   folded into the lists, so the main `match` of each phase is the
   dispatch. Done. Measured by adding a probe `Expr` variant: the
   compiler then points at precedence, dump and the check dispatch (3
   compile errors, 2 before); a node is named in 3 crates (tree, parser,
   semantic), 4 before.
2. Type facts. Done. `borrows`, `holds_loans` and `holders_in` lost
   their `_ =>`; `layout::held` (register, two words, memory, nothing)
   replaces lowering's `class` list and `layout::in_memory`; lowering's
   list of types lent again on a move reads `borrows`. A probe `Type`
   variant now stops the build at 8 facts (4 before); facts with a
   `_ =>` 4 → 0; decisions written twice 2 → 0.
3. Conversions in one file. Done. `Conversion::Implicit` and
   `Conversion::NeedsCast` carry the cast (`mir::Cast`, renamed from
   `mir::Conversion`, so one file does not use two types named
   `Conversion`); `lower::arithmetic::cast_conversion`, a second match over
   the same pairs whose `_ =>` relied on the checker agreeing, is gone, and
   the checker's two hand-made `Conversion::Implicit` ask
   `Types::conversion`. Places that decide how a pair converts 2 → 1;
   conversions made by hand 2 → 0.
4. THIR nodes named after a concept become core ideas, where one exists.
   Done, with no change: none has one. `Range` and `ForRange` walk with
   wrapping arithmetic, which THIR has no node for; `ToInterface` needs a
   function's address, which THIR cannot name; `StructLiteral` and
   `ArrayLiteral` differ in spreads; `Length`, `Slice`, `Address`, `Panic`
   and `Temporary` are core already.
5. The phases at the top of `crates/`: `kernel/` is gone, a folder is named
   as its package (`check/`, was `kernel/semantic`), and the tree crate is
   `tree` (was `syntax`), so `syntax/` names the group.
6. The lint of rule 2 on the last dispatches: `check_statement`,
   `resolve_type` and the dump's `item`. Their inner two-arm "is it X?"
   matches became `if let`, and `self`'s owner lists its impossible cases.
   A probe `Stmt` variant stops the build at the dump and the check.
   The same lint is on lowering's `lower_expr` and `lower_statement`, and
   on every match over all of MIR's instructions: the interpreter's
   `run_instruction`, LLVM's `write_instruction`, and `mir.rs`'s
   `accesses`, `operands`, `class_of` and its text writer. Their two "is
   it X?" matches became `if let`.
7. Review fixes. The parser's `decl.rs`, `expr.rs` and `stmt.rs` are
   `declaration.rs`, `expression.rs` and `statement.rs`, and its
   `literals.rs` is `expression/literals.rs`, as in the checker. A `bool`
   converts as `Integer::BOOL` in the conversions file and in lowering, so
   its cast is written once. The files that name `mir::Cast` and
   `layout::held` import their modules (`use model::mir;`), two levels
   deep, as every other `use`.
</content>
</invoke>
