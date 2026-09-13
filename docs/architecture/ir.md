# IR: CFG, HIR, and MIR

This map records IR implementation observed under `internal/ir`. Verify mutable details against linked symbols. It describes current sequencing and ownership without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

Current flow is:

```text
resolved AST + semantic results
  -> CFG topology
  -> HIR lowering
  -> typed HIR expression folding
  -> MIR lowering
  -> backend
```

CFG, HIR, and MIR are separate artifacts.
A later phase consumes the previous phase's model and published semantic evidence;
it does not replace an earlier phase by rediscovering its decisions.

## Source files

Key implementation files:

- `internal/ir/nodes.go`: shared expressions, places, source identity, traversal.
- `internal/ir/types.go`: runtime types and compilation-local `TypeTable`.
- `internal/ir/constfold.go`: expression/place folding and constant conversion.
- `internal/ir/inspect.go`: depth-first expression and place inspection.
- `internal/ir/cfg/model.go`: CFG graphs, blocks, sites, edges, terminators.
- `internal/ir/cfg/build.go`: CFG construction and topology finalization.
- `internal/ir/cfg/analyze.go`: source-level control-flow diagnostics.
- `internal/ir/cfg/validate.go`: CFG construction invariants.
- `internal/ir/hir/model.go`: structured HIR statements and modules.
- `internal/ir/hir/validate.go`: HIR shape and published-evidence validation.
- `internal/ir/hir/lower/module_lower.go`: module, statement, place, expression lowering.
- `internal/ir/hir/lower/lower_types.go`: semantic-to-runtime type interning.
- `internal/ir/hir/lower/lower_interface.go`: interface construction and slot types.
- `internal/ir/hir/fold/fold.go`: typed HIR expression folding.
- `internal/ir/mir/model.go`: MIR model and textual rendering.
- `internal/ir/mir/module_lower.go`: HIR/CFG/ownership-to-MIR lowering.
- `internal/ir/mir/validate.go`: MIR shape and transfer validation.

## Shared IR model

### Source identity

`ir.NodeID` is a `uint32` identifying source syntax without retaining an AST
object in IR. `ir.SourceInfo` stores a `NodeID` with its current diagnostic or
debug `source.Location`.

`SourceInfo` is embedded in shared IR expressions. `Origin` returns it, and
`WithOrigin` updates it. HIR statements carry the same identity through the
`hir.NodeID` alias and location fields.

Identity is provenance, not graph topology:

- a function, statement, expression, block, condition, or scope can retain the
  source `NodeID` that produced it;
- transformations preserve identity while replacing expression objects;
- locations are projections for diagnostics and can be absent;
- `NodeID` values are not block numbers and do not identify CFG edges.

`symbols.SymbolID` is a separate identity for bindings and callable symbols.
HIR parameters, bindings, identifiers, and MIR symbol maps use it where storage
identity is needed.

### Types

`ir.TypeID` identifies an entry in one compilation's `ir.TypeTable`.
IDs do not cross compilation contexts. HIR lowering is the semantic-to-IR type
construction boundary.

`ir.Type` describes runtime shape: void, integer, float, bool, byte, char, cstr,
string, allocator, raw pointer, owned pointer, reference, variant, array, slice,
struct, interface, or function.

`TypeTable.Intern` canonicalizes descriptors. Named structs and named variants use
`ReserveNamed` followed by `CompleteNamed`; this publishes a stable ID before
recursively lowering children. `LookupABIKey` retrieves an already interned type
without parsing or creating one. `Text`, `ABIKey`, `Type`, `NamedTypeIDs`, and
`IndexType` expose finalized type information.

`OptionalVariant` fixes optional case order:

- `OptionalAbsentCase` is case `0` and has no payload;
- `OptionalPresentCase` is case `1` and carries the optional payload.

`VariantCase` and `OptionalPayload` validate access to those descriptors.

### Expressions and places

`ir.Expr` is a sealed, typed expression interface. Its common contracts are:
child traversal, textual rendering, `TypeID`, source origin, and origin update.

`nodes.go` covers literals, variants, identifiers/operators/calls, loads and
borrows, strings and slices, interfaces, aggregates, arrays, allocation, casts,
printing, and drops.

`ir.Place` describes storage rather than a computed value. It has a root
expression and ordered `PlaceProjection` values. Projections are dereference,
field, index, or variant-payload operations. An index projection owns an
expression operand; the root preserves storage identity.

`InspectExpr` walks an expression in depth-first preorder. `InspectPlace` walks
the root followed by projection operands. Each expression's `forEachChild` is the
single child-shape declaration used by these traversals and by folding.

`FoldPlace` therefore keeps the place root and projection order, while folding
only projection index expressions. Replacing a root with a constant would change
which storage a load, store, or address operation denotes.

## CFG

### Job and boundary

`internal/ir/cfg` converts source control constructs into canonical control-flow
topology. `BuildModule` accepts an AST module and `BuildQueries` containing facts
already resolved by semantic analysis:

- `MatchCases` supplies variant case indexes in source arm order;
- `LoopGuaranteedEntry` supplies proof that a loop body executes before its first
  condition check;
- `CheckedIterations` supplies typechecked desugared loop forms.

The builder does not infer those semantic facts from scratch. It constructs one
`cfg.Graph` per function with a dense block list, entry and exit blocks, ordered
sites, terminators, and derived adjacency indexes.

`cfg.Module.Function` retrieves a graph by the function's source `ir.NodeID`.
`Graph.NodeID`, name, return metadata, and location retain function identity.

### Blocks and scopes

A `Block` has a dense integer `ID`, a source `NodeID`, a `BlockOrigin`, a
location, ordered `Sites`, a `Terminator`, and a reachability bit.

`BlockOrigin` distinguishes normal blocks from then/else blocks and loop init,
header, body, latch, and exit blocks. `BlockLoopExit` names the continuation
*after* a loop; it is not part of the loop body structure.

The builder tracks active AST block scopes in `builder.scopes`. `buildBlock`
pushes the current AST block, uses its AST ID as the `Site.ScopeID`, lowers its
statements, and appends a `SiteScopeExit` site for a nonzero block ID.

`Site.ScopeID` is lexical scope provenance. It is not a storage slot, block ID, or
edge endpoint. Break and continue handling appends scope-exit sites for scopes
left by the transfer before installing the loop jump.

### Site identity versus node identity

`cfg.SiteID` identifies one ordered semantic program point inside one CFG block:

```text
SiteID{Block: block.ID, Index: position-in-block.Sites}
```

`ir.NodeID` identifies source syntax. A site can point at a statement, scope exit,
terminator, or synthetic join and stores the source `NodeID` plus `ScopeID` and
location. Multiple sites can therefore refer to related source constructs, and
synthetic sites can have no source node.

The distinction matters:

- use `NodeID` to join CFG sites with HIR statements and semantic evidence;
- use `SiteID` to identify an exact ordered program point for dataflow evidence;
- use block integer IDs only for graph block topology;
- use `ScopeID` to associate a point with lexical cleanup scope.

### Terminators and edges

CFG terminators are sealed by `Terminator`:

- `Jump` transfers to one block;
- `Branch` stores condition, source node, scope, and true/false targets;
- `SwitchVariant` stores source node, scope, and case-to-block targets;
- `Return` transfers function control to the graph exit.

`BlockEdges` is derived from terminator successors. It is a directed graph keyed
by block integers and contains `BlockEdge{From, To}` values.

`SiteEdges` is the canonical ordered program-point topology. It is a directed
graph keyed by `SiteID`, with `cfg.Edge` metadata:

- `EdgeNormal` connects adjacent sites and ordinary transfers;
- `EdgeTrue` and `EdgeFalse` retain branch meaning;
- `EdgeReturn` connects a return site to the function exit site;
- `EdgeVariantCase` retains the selected variant case in `Edge.Case`.

Finalization appends terminator sites for branches and variant switches. Empty
blocks receive a `SiteJoin`. It assigns site indexes, connects adjacent sites,
then connects the final site to target block first sites. Block and site adjacency
are reverse-indexed by the shared `internal/graph` directed graph.

Topology is published as a generation. Consumers must not mutate terminators,
blocks, sites, or indexes independently; rebuild CFG if topology changes.

### CFG diagnostics and validation

`cfg.Analyze` assumes a valid finalized graph and reports source-program issues:
unreachable statements and terminators, constant non-loop conditions supplied by
its callback, and missing returns for value-returning functions.

Missing-return analysis walks paths to the graph exit backward through
`BlockEdges` and reports the most specific structured branch locations.

`Module.Validate` checks compiler construction invariants, not source semantics.
It checks dense block identity, ownership of entry and exit, reachable
termination, successor ownership, variant case uniqueness, block adjacency, site
identity, site-edge agreement, reverse adjacency, and reachability. Validation
errors are internal compiler failures; `Analyze` owns user diagnostics.

## HIR

### Job and inputs

`internal/ir/hir/lower.GenerateHIR` receives a `project.CompilerContext` and a
semantic `project.Module`. It consumes AST declarations plus bindings,
resolved expression types, conversions, flow facts, typechecking evidence, and
symbol scopes. It does not run a new type inference pass.

The result is `hir.Module`:

- module name, file path, and shared `*ir.TypeTable`;
- `Extern` signatures for declarations without bodies;
- `Function` values with parameters, return `TypeID`, symbol/source identity, and
  a structured `*hir.Block` body.

`lowerExternSignature` and `lowerASTFunctionNamed` consume resolved function and
parameter types. Missing signature evidence produces invalid HIR for validation;
lowering does not reconstruct it from syntax. `lowerASTFunctionNamed` builds
function metadata, parameters, and its body through `appendBlock`.

### Structured model

HIR keeps source control structure explicit. `Stmt` is sealed and provides child
traversal, node-local artifact validation, text rendering, and source information.
The statement set is:

- `Block` with ordered statements and block identity;
- `Binding` with name, constness, type, optional initializer, and symbol identity;
- `ExprStmt` with expression and value-node identity;
- `Assign` with an `ir.Place` target and value expression;
- `Invalid` for recoverable lowering failures;
- `Return` with an optional value;
- `If` with condition, then block, and optional else statement;
- `For` with optional init, condition, bindings, body, and next blocks;
- `SwitchVariant` with subject and ordered `VariantCaseBlock` bodies.

`VariantBinding` records field index or whole-payload binding, name, type, and
symbol identity. HIR owns the semantic subject and case bodies; CFG owns the
control-flow targets for the same match.

`InspectStmt` is the canonical depth-first preorder traversal and ignores plain or
typed-nil statement slots. Each statement's `forEachChild` declares recursive
structure. `Module.Text` and statement `appendText` provide textual output, not
execution semantics.

`hir.Module.Validate` uses `InspectStmt` once. Each sealed statement implements
`validateSelf`, which checks only evidence and required slots owned by that node;
adding a statement type therefore requires its validation contract before it can
implement `Stmt`. Validation covers callable signatures, statement slots, required
bodies, explicit invalid nodes, expression and place types, place projection shape,
and variant-binding types. It does not re-derive whether a source construct was
semantically legal or which construct should have been lowered; those decisions
remain with semantic analysis and HIR lowering.

### HIR lowering details

`appendStmt` maps source statements to HIR and shared IR expressions. It consumes
semantic evidence for match arms and for-in loops. Break and continue have no HIR
statement: CFG owns their transfers.

`lowerForStmt` makes range and sequence loop segments explicit, including
cursors, limits or length conditions, bindings, indexed loads, and increments.

`lowerPlace` turns identifiers, selectors, indexes, dereferences, fields, and
variant payload paths into `ir.Place` projections. Ordinary selectors consume
`Typechecking.StructFields` for selected slot, physical field type, and implicit
dereference. This physical type remains distinct from flow-refined expression type.
Flow-refined variant selectors consume `Flow.VariantFields`. HIR still chooses
place load versus temporary field extraction from addressability. The lowerer does
not repeat ordinary field lookup by source name. `appendVariantPayloadPlace` uses
flow payload facts to append payload projections. `lowerReferenceValue` chooses
`AddrOf`, `SliceView`, or `TempBorrow` based on addressability and target shape.

`lowerASTExpr` first reads the canonical resolved expression type and conversion
side tables. It handles flow case tests, place loads, optional promotion,
interface construction, numeric and struct casts, variant construction, literals,
identifiers, operators, calls, indexing, aggregates, allocation, strings, and
borrows. It applies `WithOrigin` with the AST expression's `NodeID` and location.

`lower_types.go` owns semantic type conversion. `runtimeTypeInterner.intern`
normalizes aliases, optionals, pointers, references, arrays/slices, structs,
interfaces, and functions into `ir.TypeID`s. `internDefined` and `internNamed`
reserve named shells before recursively completing descriptors. `loweredRuntimeType`
strips semantic-only named layers while preserving recursive runtime shells.
Invalid type shapes emit diagnostics through `runtimeTypeInterner.invalid`.

`lower_interface.go` consumes resolved interface implementation evidence. It builds
`InterfaceSlot` descriptors, uses a raw pointer receiver slot plus lowered method
value types, and emits `ir.InterfaceMake`. `lookupInterfaceMethod` finds a method
slot in a lowered interface descriptor. Abstract `Self` shapes that cannot be
materialized are rejected during lowering.

## HIR constant folding

`hir/fold.ApplyTypedExpressionFolding` folds expressions in every HIR function
body and returns the same module after replacing bodies with copied structured
blocks. It folds values, not source-written control-flow structure: an `If`
remains an `If`, loop segments remain present, and match cases remain present.

`foldBlock` clones the incoming constant environment before walking statements.
`foldStmt` calls the shared `ir.FoldExpr` and `ir.FoldPlace` implementations:

- bindings fold their initializer; constant bindings publish a folded value;
- expression statements fold their value;
- assignments fold target projection indexes and RHS;
- returns fold their optional value;
- if conditions and both branches fold independently;
- every loop segment receives a cloned environment;
- every variant case receives a cloned environment and keeps its bindings.

`cloneConstEnv` copies the name-to-`constvalue.Value` map. Environments are
branch-local: a constant introduced in one branch does not leak into its sibling
or parent. The current implementation keys these folding environments by HIR
binding name, while symbol IDs remain preserved on HIR nodes.

`ir.FoldExpr` recursively folds all shared expression forms and can replace an
entire expression with a literal or variant constant. `ir.ConstValueOf` converts
supported literal and variant expressions back to `constvalue.Value`; constant
conversion retains the original `SourceInfo`. Unknown expression kinds panic so
new sealed expressions cannot be silently omitted.

## MIR

### Job and inputs

`mir.GenerateMIR` receives HIR, the finalized CFG module, ownership results,
module scope, and evaluated constant values. It returns a MIR module containing
shared types, static data, interface thunk metadata, extern functions, and lowered
functions.

It requires both HIR and CFG. For each HIR function it looks up the CFG graph by
HIR function `NodeID`, indexes HIR statements by source `NodeID`, and calls
`lowerCFGFunction`. MIR therefore does not rebuild loops or branches from HIR;
it follows the already finalized CFG topology and joins each site back to HIR by
`NodeID`.

The module-level pass also interns constant strings and creates static entries for
module constants when their ABI key is present in the type table. `InternString`
deduplicates raw string data by bytes and alignment.

### MIR model

A MIR `Function` has name, parameters, return type, integer `EntryID`, blocks, and
location. A MIR `Block` has an integer ID, ordered sealed `Instr` values, and one
sealed `Terminator`.

MIR instructions are `Assign`, `Store`, `Print`, `Drop`, `DynamicArrayOp`, `Call`,
and `InterfaceCall`. Terminators are `Jump`, `Branch`, `SwitchVariant`, and
`Ret`.

`ValueExpr` describes computed right-hand-side values. `ValueRef` describes a
reference usable by instructions. They include constants, names, string
literals, unary/binary operations, concatenation, moves, casts, address/load,
slice and length operations, string conversions, field and aggregate values,
array allocation and operations, allocation, zero values, variants, and interface
construction/calls.

MIR `Place` mirrors HIR place structure with MIR `ValueRef` roots and indexes.
`Text` methods render the model; sealed markers separate instructions from
terminators.

### CFG-to-MIR flow

`lowerCFGFunction` creates MIR blocks for reachable non-exit CFG blocks and keeps
CFG block IDs as MIR block IDs. It initializes parameter symbol values, registers
binding symbols by walking block sites, and records variant case entries from CFG
case targets plus HIR case bodies.

For each reachable source block it:

1. selects the current MIR block and source location;
2. materializes variant bindings and planned match drops when entering a case;
3. lowers synthetic loop segments according to `BlockOrigin`;
4. lowers `SiteStatement` HIR statements;
5. emits ownership cleanup at `SiteScopeExit` using `CleanupPlan.AfterScope`;
6. lowers the CFG terminator.

`lowerCFGStmt` lowers bindings, discarded expressions, assignments, and invalid
statements. Return values are deliberately handled with their CFG return
terminator so evaluation and return cleanup occur once on that path. Assignment
cleanup uses ownership's `BeforeAssign`; lowering does not invent drop obligations.

`lowerCFGTerminator` maps CFG transfers directly:

- a jump to CFG exit becomes `Ret` for void functions;
- other jumps become MIR `Jump`;
- CFG branches lower HIR `If` or `For` conditions and become MIR `Branch`;
- variant switches preserve case labels and target order;
- CFG returns lower the HIR return value, append `BeforeReturn` drops, and emit
  `Ret`.

`lowerExpr` converts shared IR expressions to MIR value references and emits
three-address-like `Assign` instructions for computed values. It lowers calls,
loads, stores, variants, interfaces, arrays, strings, casts, allocation, print,
and drop operations. `lowerPlace` lowers roots and projection index expressions.
Temporary drops are collected and flushed in reverse order at expression and
statement boundaries.

### MIR validation

`mir.Module.Validate` checks artifact shape, not semantic meaning. It rejects nil
functions, duplicate block IDs, missing entry blocks, nil instructions, missing
block terminators, unknown terminators, and transfers to blocks not present in
the function. Variant terminators must not select one case twice.

MIR validation is an internal compiler check. Backend lowering consumes validated
MIR and is not expected to recover missing blocks or terminators.

## End-to-end identity and data flow

The important joins are:

```text
AST node ID
  -> ir.NodeID / hir.NodeID
  -> CFG Graph.NodeID, Site.NodeID, terminator NodeID
  -> HIR statement NodeID
  -> MIR statement lookup and cleanup keys
```

The important topology is separate:

```text
CFG Block.ID
  -> BlockEdges: block-to-block reachability
  -> SiteID{Block, Index}
  -> SiteEdges: ordered sites plus edge kind/case
  -> MIR Block.ID and terminator target IDs
```

The important lexical and ownership evidence is also separate:

```text
Site.ScopeID
  -> ownership CleanupPlan.AfterScope[SiteID]
  -> planned MIR Drop instructions

HIR Binding.SymbolID
  -> MIR lowerer.symbolValues
  -> ownership BeforeAssign / BeforeReturn / match cleanup lookups
```

No one identifier substitutes for another. `NodeID` preserves source provenance,
`SiteID` names exact CFG program points, block IDs name topology nodes, scope IDs
name lexical cleanup regions, and symbol IDs name storage bindings.
