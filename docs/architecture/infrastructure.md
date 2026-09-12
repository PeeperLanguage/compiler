# Infrastructure: modules, pipeline, diagnostics, graphs

This map records infrastructure implementation observed in `internal/project`, `internal/pipeline`, `internal/driver`, `internal/diagnostics`, `internal/graph`, `internal/problems`, `internal/phase`, `internal/moduleid`, `internal/source`, and `internal/toolchain`. Verify mutable details against linked symbols. It describes current state carriers, scheduling, invalidation, diagnostics, and native toolchain behavior without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Package map

| Package | Responsibility |
| --- | --- |
| `project` | Compilation context, module registry, paths, imports, snapshots, fingerprints, semantic lookup, metrics. |
| `pipeline` | Concurrent module loading, import graph construction, phase barriers, phase advancement, invalidation. |
| `driver` | Public compile entrypoints and source/overlay selection. |
| `diagnostics` | Phase/module-scoped diagnostic storage, codes, labels, source cache, rendering, suggestions, highlighting. |
| `graph` | Directed adjacency, reverse edges, topology algorithms, FIFO worklists. |
| `problems` | Reusable construction of common semantic diagnostics. |
| `phase` | Ordered phase identity shared by artifacts, scheduler, diagnostics, and reuse. |
| `moduleid` | Collision-safe canonical module identity. |
| `source` | Positions, locations, source extraction, and source-cache interface. |
| `toolchain` | Validated native compiler/linker profile and argument generation. |

Non-test files in requested packages:

- `project/context.go`, `export_fingerprint.go`, `generic_types.go`, `imports.go`,
  `metrics.go`, `modules.go`, `sources.go`, `type_lookup.go`, `type_syntax.go`.
- `pipeline/loader.go`, `pipeline/pipeline.go`.
- `driver/compiler.go`.
- `diagnostics/bag.go`, `codes.go`, `diagnostic.go`, `emitter.go`, `suggest.go`,
  `syntax_highlighter.go`.
- `graph/directed.go`, `graph/graph.go`, `graph/worklist.go`.
- `problems/problems.go`.
- `phase/phase.go`.
- `moduleid/identity.go`.
- `source/location.go`, `source/position.go`.
- `toolchain/profile.go`.

Tests are omitted from this map. They exercise these contracts but do not add
runtime jobs or production data flow.

## Compiler context and module registry

`project.CompilerContext` is shared state for one compilation. It contains:

- normalized `Config` and immutable target metadata;
- canonical `ir.TypeTable`, `GlobalScope`, and shared `DiagnosticBag`;
- `CompletedProjectPhase`, the highest project-wide barrier completed in this run;
- optional `CompileMetrics`;
- module and canonical-file indexes;
- semantic-export baselines supplied by an incremental client;
- generic declaration and concrete-instance indexes;
- the shared import `graph.Graph`;
- `*sync.RWMutex`, guarding module indexes and related context maps.

`New` creates a minimal config. `NewWithConfig` fills defaults, normalizes target
OS/architecture, resolves the host fallback on invalid targets, canonicalizes root
and library paths, creates predeclared symbols and target-sized index type, and
initializes the import graph with `GraphEdgeImport`.

`WithDiagnostics` shallow-copies context and replaces only the diagnostic writer.
The copy still shares modules, graph, type indexes, target, and metrics. Pipeline
phase code uses it to label output without creating an independent compiler state.

`LibraryRoot` resolves an explicit namespace root first, then the packaged library
base. `ModuleOriginForFile` classifies local and packaged-library files. Dependency
origin is part of the model but is not selected by this path classifier.

### Module identity and registration

`moduleid.ID` has `Origin`, `Namespace`, `Dependency`, and `ImportPath`. `Valid`
requires non-empty origin and import path. `String` calls `Frame`, which encodes
each component as length plus hexadecimal bytes; delimiters in components cannot
collide with identity boundaries. The encoding is used at graph, diagnostic,
symbol, and linkage string boundaries.

`project.IdentityForFile` is the canonical constructor: it first calls
`ImportPathForFile`, then combines origin, namespace, and import path. Local paths
use the configured root (or manifest source directory); namespaced library paths
use that library's source directory. `NewModuleForFile` creates an explicit-content
module with this identity.

`AddModule` canonicalizes `FilePath`, then takes the context write lock. It rejects
both directions of conflict before touching indexes:

- one file already associated with another identity;
- one identity already associated with another file.

A conflict creates `ErrAmbiguousImport`. A clean registration updates `modules` and
`fileIndex`. A pathless replacement removes a stale file index. Registration also
reindexes collected generic declarations, because fresh incremental contexts may
reuse module artifacts while their derived context index starts empty.

`ModuleByID`, `ModuleByFile`, and `Modules` use read locking. `Modules` returns a
slice snapshot of module pointers; it does not deep-copy modules or impose order.

## Module snapshot

`project.Module` is the per-source snapshot shared by all phases. Its input and
identity fields are:

- `ID`, canonical semantic/import/graph identity;
- `FilePath`, absolute slash-separated path;
- `IsEntry`;
- `Content` and `ContentProvided`;
- `ContentHash`, syntax import/export fingerprints, and semantic export fingerprint.

Its phase artifacts are:

- `Phase`;
- `AST` and `TypedASTNodes`;
- `ModuleScope`, `Bindings`, `Constants`, and `Typechecking`;
- `Imports`, mapping aliases to `ResolvedImport`;
- `CFG`, `Flow`, `Effects`, and `Ownership`;
- `HIR`, `MIR`, and emitted `LLVMIR`;
- collected generic declaration syntax and semantic shells.

`RebuildTypedASTIndex` indexes source AST nodes, checked loop expansions, and
expanded effective call arguments. It avoids replacing checked nested-loop trees
when an outer expansion still contains the original nested loop. `BaseExprType`
returns canonical typechecker evidence; `EffectiveExprType` prefers flow-refined
evidence and falls back to the base result.

`ResetSemanticData` replaces binding/constants staging and clears typechecking.
`ExpandedDefaultBinding` exposes generated default-expression bindings while marking
them non-local for escape analysis. `TypeDeclarationIdentity` anchors nominal names
at declaring module identity.

### Phase advancement and reset

`Module.Phase` is the last completed per-module phase. `resetToPhase` retains all
artifacts through `retained` and clears downstream artifacts:

- at or below `Parsed`: scope, bindings, constants;
- below `Collected`: generic declaration index;
- below `Typechecked`: typechecking, semantic export fingerprint, typed-node index;
- below `CFG`: CFG;
- below `FlowTyped`: flow result;
- below `Effects`: effects;
- below `Ownership`: ownership result;
- below `HIR`: HIR;
- below `MIR`: MIR;
- below `Backend`: LLVM IR.

`CompilerContext.ResetModule` calls that reset, removes owned generic instances, and
removes declaration-index entries when resetting below `Collected`. It then calls
`DiagnosticBag.DiscardModuleAfter`, removing diagnostics produced after the retained
phase. Incomplete generic-instance wait channels are closed during removal.

The reset is a snapshot downgrade, not a source reload. Existing AST/fingerprints
can survive a parse-only reset; later pipeline phases rebuild dependent artifacts.

## Inputs, outputs, and loading

`driver.CompileFile` is the ordinary entrypoint. It resolves an absolute input path,
reads disk content when `overlay == nil`, or uses the overlay string otherwise. It
creates or obtains the module, marks it as entry, runs `pipeline.Run`, and returns
the module. Read/path/pipeline failures are added to a load-scoped diagnostic bag.
`driver.AddSource` registers virtual content without running the pipeline.

`project.DiscoverSourceFiles` expands explicit files and directories, accepts the
Peeper source extension, skips `.git`, `build`, `_builtin_library`, and `.tmp*`,
deduplicates canonical paths, and sorts output.

`pipeline.moduleLoader` owns asynchronous loading. Its inputs are a context and an
entry module. Its outputs are registered modules, parsed ASTs, import aliases,
import graph edges, and load/parse diagnostics. `scheduled` and its mutex deduplicate
identity scheduling; a `WaitGroup` waits for recursive loads.

`Load` enqueues the entry and waits. `enqueue` first records an identity, checks an
existing registry module, registers new modules, then starts `loadModule` in a
 goroutine. A same identity from a different path still reaches `AddModule` so an
identity conflict cannot be hidden by scheduling deduplication.

`loadModule` has two paths:

1. An existing AST keeps its fingerprints, resets only if below `Parsed`, then
   resolves imports.
2. A file-backed module reads content if needed, adds source content to diagnostics,
   hashes text with `ast.HashText`, lexes, parses, releases source text, records AST
   fingerprints, resets to `Parsed`, and resolves imports.

`resolveImports` converts declarations to raw paths, calls
`CompilerContext.ResolveImportPath`, selects explicit or basename aliases, detects
alias collisions, records redundant-prelude information, stores `ResolvedImport`,
adds `module -> imported module` graph edges, and enqueues imported modules.

`ResolveImportPath` validates root-relative paths, rejects unsupported remote paths,
resolves local project or namespaced library roots, appends/checks the configured
extension, requires a regular file, canonicalizes it, and returns identity plus path.
`ImportCandidates` applies the same root, namespace, hidden-segment, extension, and
sorting policy for editor completion.

## Pipeline jobs and data flow

`pipeline.Run` is the central scheduler. It marks the entry, registers it, starts
load diagnostics, loads the prelude and entry, and adds edges from every non-prelude
module to the prelude. It snapshots registered modules into graph IDs and calls
`Graph.TopoSort`.

A graph edge points from importer to imported module. Topological output is therefore
consumed in dependency-first order: imported modules appear before their importers.
Cycles produce `ErrCyclicImport` diagnostics and stop before semantic phases.

The pipeline then runs these project jobs:

| Job | Input | Output / barrier |
| --- | --- | --- |
| Load | entry, prelude, file/overlay content | registered modules, ASTs, imports, graph, `Load`/`Parsed` diagnostics. |
| Ownership-through | ordered modules and import readiness | phases through `Ownership`; symbols, bindings, types, CFG, flow, effects, ownership. |
| Usage | ownership-ready modules | usage diagnostics and `Usage` module/project barrier. |
| Entrypoint check | entry module scope and function type | optional `ErrInvalidEntrypoint`. |
| Backend-through | usage-ready modules and clean diagnostics | HIR, MIR, LLVM IR, `Backend` module/project barrier. |
| Finalize | all MIR modules | runtime-symbol validation and `Finalize` project phase. |

`advanceModulesThrough` repeatedly gathers modules whose next phase is ready, runs
that batch concurrently, waits, then invalidates semantic dependents between
batches. Readiness checks prelude state and imported-module prerequisite phases.
The scheduler never advances a module more than one phase per call.

The per-module sequence is:

`Parsed -> Collected -> Bound -> Resolved -> ConstEval -> Typechecked -> CFG ->`
`FlowTyped -> Effects -> DefiniteInit -> Ownership -> Usage -> HIR -> MIR -> Backend`.

`advanceModulePhase` owns the dispatch. Collection builds declarations; binding fills
symbol/type state; resolution fills imports and names; constant evaluation publishes
values; typechecking publishes semantic types and the semantic export fingerprint;
CFG builds and validates topology; flow typing refines types and origins; effects
publish ordered storage/value actions; definite-init and ownership analyze evidence;
HIR and MIR lower; LLVM backend emits text. HIR/MIR/backend are blocked when active
errors exist. Each successful advance increments metrics.

`usage` is run as a separate project barrier because its diagnostics consume the
completed ownership state. `requireScheduledModulesAtLeast` turns a scheduler stall
without user diagnostics into an internal error rather than successful partial
compilation.

## Fingerprints and invalidation

There are three source/semantic identity layers:

- `ContentHash`: exact source text hash, set during loading or workspace capture.
- `ImportFingerprint` and `ExportFingerprint`: parser-owned syntax surfaces. Import
  surface identifies dependency names; export surface excludes function bodies but
  includes declaration shape, names, types, fields, enum payloads, and defaults.
- `SemanticExportFingerprint`: typechecked compiler-visible API. It includes public
  symbols, types, mutability, methods, attributes/link names, default-expression
  syntax and resolved facts, and published constant values.

`SemanticExportFingerprint` uses `SemanticExportFingerprint(ctx, module)`. Recursive
semantic types are guarded by a visiting set. Constants use defining-module values
through `PublishedConstant`, so imported constant changes affect the fingerprint.

The workspace index uses content hashes to avoid work for byte-identical files.
Changed import/export syntax propagates dirtiness through reverse dependents; body-only
edits remain local. Unchanged dependents whose upstream surface changed can retain
`Parsed` artifacts but must rerun semantic and lowering phases.

The LSP seeds semantic baselines before reuse. `invalidateSemanticDependents` runs
between scheduler batches, compares a newly typechecked module against its baseline,
walks graph predecessors, and resets typechecked dependents to `Parsed`. This is
semantic invalidation after final dependency facts, not parser-surface invalidation.

`ResetModule` also invalidates phase-scoped diagnostics and generic instances. Thus
artifact reset, diagnostic reset, and semantic cache reset move together.

## Diagnostics

`diagnostics.Diagnostic` carries severity, message, stable code, file path, ordered
labels, and extras. A primary label is the diagnostic origin; secondary labels carry
context such as a previous declaration or recursive generic application. Code hints,
insertions, removals, replacements, notes, and help are extras.

`DiagnosticBag` stores `groups[producingPhase][moduleScope]`. `BeginPhase` replaces
one phase/module group and returns a scoped writer. `AppendPhase` appends to an
existing group. The module scope is opaque to diagnostics; pipeline callers pass
`moduleid.ID.String()`.

Groups can be copied inactive for incremental reuse with `CopyModuleRange`, then
published with `ActivateModuleRange` after the required project barrier. Inactive
groups do not count, emit, or participate in duplicate suppression. `DiscardModuleAfter`
removes groups invalidated by a module reset.

`Add` holds the bag mutex. Active error diagnostics with the same file, line, and
column are deduplicated by first primary label. Counts and `Diagnostics` use the
same active-group rule. `Diagnostics` returns a phase-sorted, module-sorted snapshot;
`emitFiltered` then sorts by primary file/line/column before rendering.

Codes are string constants in `diagnostics/codes.go`:

- `L` lexer errors and `P` parser errors;
- `D` declaration family where used by the codebase and `T` type/semantic errors;
- `M` module/import/entrypoint errors;
- `S` style/info codes and `W` warnings;
- `ICE0001` generic internal compiler error, `ICE0002` invalid evidence, and
  `ICE0003` invalid topology.

Important infrastructure codes are `M0001` module not found, `M0002` cyclic import,
`M0003` invalid path, `M0005` ambiguous import, `M0006` invalid entrypoint,
`ICE0002` invalid evidence, and `ICE0003` invalid topology. The complete list remains
canonical in `codes.go`; callers should use constants, not duplicate strings.

`problems/problems.go` centralizes construction for array bounds, unreachable code,
and redeclarations. `ReportRedeclaration` looks up the previous symbol and attaches
both locations. It does not store or emit diagnostics itself beyond the supplied bag.

`source.Position` is one-based for line/column and zero-based for byte index.
`Advance` treats newline as a line transition and tabs as one source column.
`source.Location` owns filename/start/end pointers and extracts text through the
`SourceCache` interface. `GetSourceLinesRange` uses a cache when present and falls
back to bounded file scanning.

`diagnostics.SourceCache` stores split source lines under an RW mutex. The bag accepts
in-memory source through `AddSourceContent`, allowing diagnostics for overlays. The
emitter supports ANSI/HTML strings and stderr output, source snippets, labels,
code hints, tab expansion, and optional syntax highlighting. `SyntaxHighlighter`
performs lightweight line tokenization; `suggest.go` supplies Levenshtein-based,
ambiguity-aware nearest-name suggestions with stable priorities.

## Graphs and worklists

`graph.Directed[Node, Edge]` is the unsynchronized topology kernel. An endpoint
function maps each edge to `(from, to)`. It stores one outgoing index and one reverse
incoming index. `AddEdge` deduplicates equal edge values while preserving distinct
metadata-bearing edges. Edge snapshots protect callers from mutating indexes.

`Successors`, `Predecessors`, degrees, `TopoSort`, and
`WeaklyConnectedComponents` all consume these same indexes. `TopoSort` restricts
traversal to supplied IDs, uses temporary/done DFS states, returns dependency-first
postorder, and reports directed cycles by extracting the active stack segment.
Components traverse both successor and predecessor edges.

`graph.Graph` is the synchronized domain facade. Its mutex protects `Directed`; its
`edgeKind` supplies a default filter. It rejects empty domain node IDs, supports
edge-kind filters, and exposes the same topology queries. `project` uses edge kind
`import`; the pipeline stores module IDs as `graph.NodeID`.

The loader adds import edges. The pipeline adds prelude edges. Invalidation walks
`Graph.Predecessors(changed)` because predecessors are importers/dependents under
this edge orientation. Workspace components use weak connectivity to limit reuse and
invalidation to a related module set.

`graph.Worklist[Node]` is the canonical FIFO fixed-point queue. `queued` prevents
multiple pending copies; `Next` removes a node from `queued`, so transfer code may
schedule it again after new information arrives. Queue compaction bounds stale prefix
storage. Flow typing, definite initialization, ownership, and liveness reuse this
mechanism but retain their own lattice, join, transfer, direction, and diagnostics.

## Lock discipline

- `CompilerContext.mu` protects module/file indexes, semantic baselines, declaration
  indexes, and generic-instance maps. Use `RLock` for lookups/snapshots and `Lock`
  for registration, baseline updates, and invalidation-related map mutation.
- `DiagnosticBag.mu` protects phase/module groups, active flags, counts, snapshots,
  and source-cache access where applicable. Scoped writers share the same mutex and
  group maps; callers do not hold context locks while emitting diagnostics.
- `graph.Graph.mu` protects its `Directed` adjacency. `Directed` itself has no lock;
  phase-local users may use it without synchronization, while long-lived graph users
  must provide one.
- `CompileMetrics.mu` protects counters and snapshots.
- `moduleLoader.mu` protects scheduling maps; its `WaitGroup` covers recursive load
  goroutines. Module artifact fields are advanced by scheduler jobs, not by arbitrary
  graph readers.
- `diagnostics.SourceCache.mu` protects cached line slices.

Pipeline parallelism is batch-based: independent ready modules advance concurrently,
then the scheduler waits before semantic-dependent invalidation or the next barrier.
Prelude injection occurs once after its collected scope is available. This preserves
stable dependency facts without requiring every phase artifact to be globally locked.

## Native toolchain boundary

The compiler emits LLVM IR; `cmd/build.go` owns native jobs. `toolchain.Profile` is
the validated contract for target triple, Clang/linker paths, sysroot or SDK,
runtime archive/ABI, link mode, debug format, and minimum OS.

`NewManagedProfile` creates release profiles for supported host OS/architectures.
`Load` parses strict JSON, rejects unknown/trailing data, validates schema, target,
link/debug modes, SDK rules, and installation-root-relative paths, then resolves
managed paths. `Resolve` loads an installed managed profile or falls back to `clang`
in `PATH`, discovering an Apple SDK where required and attaching the runtime archive
when present.

A successful executable build has these jobs:

1. Compile source with `driver.CompileFile`; require no active errors and valid entry.
2. For each module, write `LLVMIR` to a temporary `.ll` input.
3. Run Clang with `ObjectArgs` to produce a temporary `.o` output.
4. Write `objects.rsp`, containing object paths and optional runtime archive.
5. Run the linker with `LinkArgs` and response-file input to a staged executable.
6. Atomically replace the requested output path via the build command's replacement
   step; temporary artifacts are removed on exit.

`ObjectArgs` selects target, sysroot/minimum OS, optional debug flags, LLVM input,
and object output. `LinkArgs` selects target/sysroot/minimum OS, static CRT/runtime
arguments where configured, response-file input, and executable output.
`WriteResponseFile` rejects line breaks and quotes paths containing whitespace.
`runCompilerTool` captures combined subprocess output and reports action/tool errors.

This boundary consumes completed `Module.LLVMIR`; it does not participate in module
phase scheduling, graph invalidation, or diagnostic grouping beyond build-level
errors returned by the command.
