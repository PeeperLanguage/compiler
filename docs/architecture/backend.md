# Backend: LLVM emission and targets

This map records backend and target implementation observed in source. Verify mutable details against linked symbols. It describes current sequencing and ownership without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Scope

- Backend currently consumes MIR and emits textual LLVM IR.
- Physical layout and code generation currently live in `internal/backend/llvm`.
- Target identity, LLVM triples, pointer width, and index width currently live in `internal/target`.

## Inputs and outputs

`llvm.GenerateLLVMIR` receives:

- `*mir.Module`.
- `*diagnostics.DiagnosticBag`.
- `target.Info`.
- `debugBuild bool`.

The module contains:

- module name and source path;
- one shared `ir.TypeTable`;
- raw or typed static data;
- interface thunks;
- function declarations and definitions.

Each MIR function contains parameters, a return `TypeID`, entry block ID, blocks,
and an optional source location. Each block contains MIR instructions and one
terminator. `Instr` and `Terminator` are sealed interfaces; the emitter uses
exhaustive type switches and panics if a new MIR node is not classified.

The result is LLVM module text. Invalid target or invalid runtime-symbol use
returns an empty string after diagnostics. Unsupported layouts and lowering
errors set emitter invalid state; `finalLLVMText` then returns an empty string.

## Module emission job

`GenerateLLVMIR` emits in this order:

1. Validate non-nil module and `target.Info.Valid()`.
2. Validate reserved runtime symbols with `ValidateRuntimeSymbols`.
3. Create one `llvmEmitter` for the module.
4. Emit `source_filename` and `target triple`.
5. Resolve and emit identified named type definitions.
6. Emit print format globals when print is used.
7. Emit MIR static data: raw byte arrays or typed constants.
8. Discover interface constructions and emit unique itab globals.
9. Emit declared MIR functions.
10. Emit print, allocator, free, and overflow-intrinsic declarations as needed.
11. Emit default allocator descriptor thunks when allocator runtime is needed.
12. Collect and emit direct-call declarations.
13. If no function has blocks, finalize.
14. Emit named drop helpers.
15. Emit MIR interface thunks.
16. Emit interface payload drop/release thunks.
17. Emit each defined function and its blocks.
18. Append late external globals and debug metadata.

Static strings are already interned by `mir.Module.InternString`; raw entries are
escaped by `llvmEscapeString`. Typed constants are lowered by `staticConstant`.
Variants use `staticVariantConstant` and `staticVariantPayload`.

## Emitter state

`llvmEmitter` owns module-wide state:

- `mod`: MIR input.
- `diag`: diagnostic sink.
- `target`: immutable target metadata.
- `badTypes`: type text already reported as unsupported.
- `layouts`: interned backend layout descriptors by `ir.TypeID`.
- `layoutBuilding`: recursion guard while constructing layouts.
- `dropHelpers`: named type IDs mapped to generated drop helper symbols.
- `invalid`: module-level failure bit.
- `externalGlobals`: globals discovered while lowering external references.
- `debug`: optional `llvmDebugEmitter`.

`layout` is the only normal entry to a layout. `layoutType` caches successful
layouts, builds named recursive shells first, and reports failure to
`reportUnsupportedType`. `markInvalid` records an error and preserves the first
module-level failure state; it does not manufacture valid output.

`finalLLVMText` is intentionally late. External globals are only known when
`emitRef` encounters a name containing `$`, so declarations are appended after
function lowering. Debug metadata is also appended at finalization.

## Backend layout and type interning

The IR `TypeTable` is canonical type storage. `Intern` keys ordinary descriptors;
identified structs and named variants key by stable identity. `ReserveNamed` and
`CompleteNamed` publish recursive named composites in two stages. `IndexType`
is the target-sized integer selected before backend emission. `ABIKey` is stable
inside the compiler ABI model and is used for symbol identity, not raw `TypeID`.

LLVM adds a second, physical layer: `llvmLayout` carries emitted LLVM text and
physical evidence. It records:

- `Kind`: void, scalar, pointer, aggregate, array, or function;
- pointer pointee or array element;
- aggregate elements and named carrier-field indexes;
- variant tag index and case-payload indexes;
- function return and parameter layouts.

`llvmScalarLayout`, `llvmPointerLayout`, `llvmAggregateLayout`, and
`llvmFunctionLayout` construct descriptors. `llvmLayoutsMatch` checks both kind
and LLVM text. The backend does not use source field names to address builtin
carriers: `llvmFieldName` constants provide canonical fields such as `data`,
`length`, `capacity`, `allocator`, `tag`, `present`, `value`, and `dispatch`.

Named identified composites receive `%peeper.type.<hex ABIKey>` names. The
backend first builds recursive shells, then emits completed definitions using
`emitNamedTypeDefinitions`. Recursive construction is rejected when a non-shell
layout would re-enter a layout currently being built.

## Physical layouts

Primitive layouts:

- `void` -> `void`.
- integer -> `i<Bits>`.
- 32-bit float -> `float`; 64-bit float -> `double`.
- bool -> `i1`.
- byte -> `i8`.
- char -> `i32`.
- C string, raw pointer, allocator -> `i8*`.

Carrier layouts:

- string: `{ i8*, Index, i8* }` for data, length, allocator.
- owned pointer: `{ Element*, i8* }` for data and allocator.
- borrowed reference to ordinary `T`: `T*`.
- reference to string: `{ i8*, Index }`.
- reference to a slice: `{ Element*, Index }`.
- interface reference: `{ i8*, i8* }` for data and dispatch.
- owned interface: `{ i8*, i8*, i8* }` for data, dispatch, allocator.
- dynamic array: `{ Element*, Index, Index, i8* }` for data, length,
  capacity, allocator.
- fixed array: LLVM `[N x Element]`.
- user struct: aggregate with source field order.

A source `TypeSlice` has no direct layout in this backend stage; slice carriers
are represented through references or array carriers. Functions are LLVM
function-pointer layouts with the lowered return and parameter layouts.

Optional variants use `{ i1, Payload }`, with named fields `present` and `value`.
Named variants use a tag (`i8` through 256 cases, `i16` through 65536 cases,
otherwise `i32`) followed by one element for each payload-bearing case. The tag
and payload indexes are stored in the layout descriptor.

## Values versus addresses

`llvmValue` is an SSA value plus its physical `llvmLayout`. `llvmPlace` is an
address plus its pointee layout. This distinction is enforced by the builder:

- `value` and `place` reject missing text or physical evidence.
- `load` consumes a place and returns its pointee layout.
- `store` checks target pointee against value layout.
- `alloca`, GEP, field GEP, and array GEP return places.
- `pointerValue` converts a place to a typed pointer value.
- `pointerPlace` converts a data-pointer value back to a place.
- `extractIndex`/`insertIndex` operate on aggregate or array values.
- `extractField`/`insertField` resolve canonical carrier fields first.

`emitRef` lowers constants, locals, functions, static data, external globals, and
named symbols to values. A local in `locals` is an SSA value until addressing is
needed. `localPtrs` holds entry-dominating storage for parameters, repeated
assignments, and roots used by places. `stackLocalSlots` finds those roots and
`emitStackLocalSlots` emits their `alloca`s at function entry.

`emitPlacePtr` walks MIR place projections. Dereference loads a carrier when
needed, then obtains its data pointer. Field and variant-payload projections use
layout evidence and field GEP. Index projections use carrier data and length or
fixed-array GEP. `AddrOf` returns a typed pointer and bitcasts non-byte pointers
to raw pointer only when its result type requires `rawptr`.

The builder panics on backend invariant violations: mismatched stores, calls,
returns, PHI inputs, aggregate inserts, casts, pointer conversions, or branch
conditions. These are compiler/backend bugs, not source diagnostics.

## MIR expression and instruction lowering

`instruction_emit.go` owns general MIR value and place lowering:

- unary and binary arithmetic select integer or floating LLVM opcodes;
- comparisons select signed, unsigned, floating, or string equality paths;
- casts use LLVM integer, floating, pointer-integer, and extension/truncation
  operations;
- division and remainder trap on zero and signed minimum divided by `-1`;
- shifts trap when the count is outside operand width;
- struct and array literals use zero plus `insertvalue`;
- variants use tag insertion and payload insertion;
- `len`, loads, fields, addresses, slice views, strings, calls, allocation, and
  zero values delegate to specialized lowering.

`emitStore`, `emitPrint`, and `emitDrop` lower corresponding MIR instructions.
Direct and interface calls are emitted even when their result is discarded.
Block terminators become `br`, conditional `br`, `switch`, or `ret`.

`main` is the one return ABI adjustment: a source-level void `main` is emitted
with LLVM `i32` return type. A void return in that case emits zero.

## Labels, SSA, and debug locations

`llvmBuilder` owns function-local state: output builder, temporary ID counter,
SSA locals, local places, emitter/debug references, current debug scope/location,
and current label. Registers are `%t1`, `%t2`, and so on, starting at one per
builder.

MIR block ID `N` becomes label `bN`. Specialized lowering allocates unique
numeric suffixes from the same `nextID`; examples include bounds, allocation,
array, string, UTF-8, drop, and division labels. `namedLabel` updates
`currentLabel`, which loop and PHI emission uses to name incoming edges.

PHI results and loop-carried arithmetic results can reserve a register before
its defining instruction. `definePhi` checks every incoming layout and label;
`defineArithmetic` defines a previously reserved result. `trap` emits
`@llvm.trap` followed by `unreachable`.

When debug output is enabled, `setLocation` maps source locations to cached
`!DILocation` nodes. Function definitions receive `!DISubprogram`. Module flags
use CodeView on Windows and DWARF version 4 elsewhere. `line` attaches the
current location to emitted instructions.

## Calls and interface ABI

Direct calls use the lowered function layout. `collectCallDecls` finds direct
MIR calls not defined in the module and emits declarations. Call arguments and
return values must exactly match physical layouts.

Interface values carry a data pointer and itab/dispatch pointer. Itab globals
are emitted once per interface/data ABI pair. Vtables begin with a drop slot;
owned interfaces add a release slot, followed by method slots. `interfaceSymbolName`
uses borrowed/owned kind plus `TypeTable.ABIKey` values, so these ABIs cannot
share symbols accidentally.

Interface method slots receive `i8*` data first. `emitInterfaceThunk` adapts this
raw receiver to a concrete pointer receiver or loads a concrete value receiver,
then calls the implementation. `emitInterfaceCallTarget` extracts data and
itab, indexes the vtable, loads a function pointer, and bitcasts it to the method
slot layout. Consuming owned interface calls release storage after dispatch.

## Allocation, strings, arrays, and drops

`allocator_emit.go` treats allocator handles as pointers to a three-slot
descriptor: context, allocation function, and deallocation function. Allocation
checks null and traps. Storage size uses target index width and
`llvm.umul.with.overflow`; zero-sized storage is normalized to one byte.
Missing allocator operands use the generated default descriptor.

`dynamic_array_emit.go` lowers dynamic array allocation, reserve, append, resize,
and shrink. Reserve reallocates and relocates elements, then releases old storage.
Append checks length overflow and doubles capacity with overflow detection.
Resize fills newly covered elements. Shrink drops removed elements.

`string_slice_emit.go` lowers string concatenation, byte-to-string conversion,
string equality, string and array slice views, UTF-8 validation/counting,
character-array conversion, and byte copying. Slices carry pointer plus length.
String slices validate UTF-8 boundaries. Index and slice bounds trap in generated
LLVM control flow.

`drop_emit.go` lowers MIR-directed destruction. It recursively drops strings,
owned pointers, variants, dynamic/fixed arrays, structs, and interfaces in
representation order; aggregates walk fields/elements in reverse order. Named
runtime-owning types get private drop helpers. Interface payloads get drop and,
for owned interfaces, allocator-release thunks.

## Runtime symbols and ABI validation

`ValidateRuntimeSymbols` scans actual MIR operations before emission. It reserves:

- `peeper_rt_v1_printf` when print is used;
- `peeper_rt_v1_alloc` for allocation paths;
- `peeper_rt_v1_free` for raw frees.

It rejects user declarations conflicting with reserved signatures and rejects
extern functions whose parameters or return types carry allocation ownership.
Runtime declarations are emitted only when the corresponding operation requires
them. The backend therefore consumes ownership/lowering decisions already
represented by MIR drops, allocator operands, and consuming interface calls.

## Target configuration

`target.Info` is immutable metadata for one compiler context:

- normalized OS;
- normalized architecture;
- canonical LLVM triple;
- pointer width;
- index width.

`target.New` normalizes OS/architecture, resolves `LLVMTriple`, and sets pointer
and index widths from `DefaultSizeBitsForArch`. `Host` uses `runtime.GOOS` and
`runtime.GOARCH`. `Valid` requires OS, architecture, and triple plus 32- or
64-bit pointer width and equal index width.

`llvm_triple.go` owns the supported OS/architecture-to-triple table. It also
provides `SystemLLVMTriple` for the unmanaged system Clang ABI, normalization,
Windows executable suffix, and host-target comparison. Empty OS or architecture
means host values; unknown combinations return errors.

`wordsize.go` defines `Bits32` and `Bits64`. Its `arch32` set covers 32-bit Go
architectures; unknown architectures default to 64 bits. `ArchFor32BitMode`
selects compatible 32-bit counterparts for supported architectures and rejects
ones without a supported LLVM counterpart.

The target’s index width flows into `TypeTable.IndexType`, then into layouts for
lengths, capacities, allocator sizes, bounds operations, and runtime allocation
signatures. Lowering widens target-sized indexes to `i64` only in paths whose
arithmetic is deliberately i64, and truncates back after representability checks.
