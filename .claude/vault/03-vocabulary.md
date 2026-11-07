# Vocabulary

The codebase's fixed terms: use these words, with these meanings.

## Crates (`Cargo.toml` workspace; ownership from `docs/ir-design.md` Crates)

| Layer | Crate | Owns |
| --- | --- | --- |
| base | `source` | files, byte `Span`s; line and column on demand |
| base | `arena` | typed `Arena`, `Id<T>`, `List`; the `nodes!` macro |
| base | `bigint` | integer arithmetic of any width, like LLVM's APInt |
| base | `diagnostics` | `Diagnostic`, `Code`s, the printer, `nearest_name` |
| base | `literal` | literal text rules (`literal::numeric`) |
| base | `runtime_abi` | what a running program does as seen from outside, shared by `llvm` and `eval` |
| frontend | `token`, `lexer`, `parser` | tokens, lexing, parsing (tree in `model::ast`) |
| frontend | `project` | loading, import resolution, module graph, `peeper.toml` (`name`, `build = "program"` or `"lib"`) |
| middle | `model` | data only: `ast`, `thir`, `mir`, `semantic_types`, `constant`, `machine`, ids, operators |
| middle | `check` | `Checker`: scopes, names, types, THIR, signatures, folding; builds bodies |
| middle | `lower` | THIR to MIR |
| middle | `flow` | path checks over MIR, and `cleanup` |
| middle | `eval` | operators on constants; the MIR interpreter |
| backend | `llvm` | MIR to LLVM text; clang and the linker; the runtime build |
| driver | `compile` | one compile: module walk, reuse, `Output` |
| driver | `peeper`, `lsp`, `browser` | command line, language server, web build |
| tools | `toolchain` | toolchain packs |

## Ids

- `ModuleId`: a module's place in the program's order.
- `DefId { module, index }`: a top-level declaration by its index in the
  tree; memo tables are vectors indexed by it.
- `TypeId(u32)`: an interned type; equal types have equal ids.
- `StructInternals`, `ArrayInternals`: what the compiler works out from a struct's
  fields or an array's element, kept inside the type so nothing walks them again.
- `Id<T>`: an index into an `Arena<T>`. In `flow::loans`, `Id<Holder>` and
  `Id<Loan>` name holders and loans, which have no stored node.
- Module identity is its import path string `package:path`
  (`myapp:utils/file`, `core:global` is the prelude).

## Diagnostic code families (`crates/base/diagnostics/src/lib.rs`)

| Prefix | From | Severity |
| --- | --- | --- |
| `L` | lexer | error |
| `P` | parser | error |
| `M` | modules, imports, entry point | error |
| `T` | checks (types, names, path checks) | error |
| `S` | style and literal form (`S0004` info, `S0007` error) | mixed |
| `W` | warnings (`W0001` unreachable, `W0018` hidden `mut`) | warning |

Severity is declared with each code, never derived from its letter. Loan
codes: T0037 borrow conflict, T0036 local address returned, T0016 invalid
return (origins), T0043 not supported yet, T0044 evaluation failed.

## MIR terms (`docs/ir-design.md` MIR)

Variable slot, instruction (`load`, `length`, `store`, `move`, `borrow`,
`address`, binary, unary, `convert`, `call`, `phi`, `aggregate`, `field`,
`array parts`, `check`, `print`, `scope end`), terminator (`goto`, `branch`,
`return`, `panic`, `fall off`), `Place` = variable + `Projection` steps
(field, dereference, index, slice elements, subslice), operand = constant,
argument or earlier value. Holder: a reference or slice in a variable or
value, per field. Loan: one `borrow`, then one parameter loan per parameter.
Two-phase: a `&mut` used once, as a call argument, is reserved until the
call.

## Fixtures (`x_test/*/peeper.toml`)

Modes: `check` (95), `run` (90), `mir` (19, with `expected.mir`), `ll` (13,
with `expected.ll`). `negative_*` fixtures expect errors; `warn_*` warnings;
`run_i686_*` build for `i686-linux`. `PEEPER_BLESS=1` rewrites expected files.

## Words the code avoids (`RULES.md` §5)

`handle`, `processData`, `helper`, `doThing`, `fixStuff`; short aliases that
lose meaning (`use TokenKind as K`); glob and grouped imports; `pub use`
re-exports.
