# Language server

This map records language-server implementation observed in `internal/lsp`. Verify mutable details against linked symbols. It describes current use of compiler context, module artifacts, diagnostics, symbols, and type information without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Package surface

Key implementation files:

- `server.go` owns the session loop, method dispatch, diagnostic publication, and
  file URI/path conversion.
- `state.go` owns mutable server state, document overlays, compiler snapshots,
  diagnostic generations, debounce workers, and module reuse.
- `types.go` defines the LSP request, response, notification, position, range,
  document, diagnostic, hover, completion, rename, and workspace-edit payloads.
- `jsonrpc.go` frames JSON-RPC messages and serializes writes.
- `workspace.go` indexes source files, imports, components, fingerprints, and
  incremental reuse decisions.
- `position.go` converts between LSP UTF-16 positions, byte offsets, and compiler
  source positions; it also converts source locations to LSP ranges.
- `cursor.go` walks one compiled module AST, records parent links, and resolves
  identifiers, selectors, fields, methods, and scopes.
- `hover.go` resolves and renders hover subjects.
- `navigation.go` implements definition and rename.
- `completion.go` parses completion context and produces lexical, qualified,
  import, operation, and match-arm completion items.
- `symbol_render.go` renders symbols and callable signatures for hover and
  completion detail.

The package has no separate `uri.go`; URI conversion is in `server.go`.

## Session and protocol

`Run(in, out)` creates one `ServerState`, one `protocolWriter`, and a buffered
reader. It reads one framed request at a time and dispatches by `Request.Method`.
The supported lifecycle and document methods are:

- `initialize`: chooses `RootURI` or `RootPath`, stores `RootDir`, creates the
  workspace index, and reports full document sync (`TextDocumentSync: 1`). Hover,
  definition, rename, and completion are advertised.
- `initialized`: publishes diagnostics for indexed workspace components.
- `textDocument/didOpen`: converts the URI, stores the complete text and version,
  and publishes component diagnostics immediately.
- `textDocument/didChange`: converts the URI, takes the first full-text change,
  stores its version, and schedules diagnostics after the debounce delay.
- `textDocument/didClose`: removes the overlay and version, then publishes the
  resulting component diagnostics.
- `textDocument/hover`, `definition`, `completion`, and `rename`: invoke the
  corresponding `ServerState` handler and return its result.
- `shutdown`: returns a successful null result.
- `exit`: waits for scheduled diagnostic workers before returning.

Notifications have no response ID. Requests with an ID receive a JSON-RPC 2.0
response. Invalid parameter decoding produces `-32602`; handler errors become
protocol errors through `responseErrorFrom`.

`jsonrpc.go` accepts `Content-Length` headers and rejects bodies over 16 MiB.
`writeMessage` emits the header and JSON body. `protocolWriter.mu` serializes all
writes, including concurrent debounced diagnostic notifications. Its first write
error is retained and signaled through `failureCh`; `Run` closes the input when
that channel fires.

## Message to published diagnostics

```mermaid
flowchart LR
    M[LSP message] --> S[document snapshot]
    S --> C[compile / reuse modules]
    C --> P[publish diagnostics]
```

The same compiled state is then consumed synchronously by hover, navigation, and
completion requests.

## Server state and locking

`ServerState` contains:

- `RootDir`, `Cache`, `LastCtx`, and `LastMetrics` for workspace configuration,
  open-document text, the latest compiler context, and metrics.
- `modules` for retained compiled modules used by incremental recompilation.
- `workspace` for the source/import index.
- `documentVersions` for LSP document versions included in diagnostic
  notifications.
- `diagGeneration` for invalidating diagnostic snapshots after any overlay
  mutation.
- `diagVersion` for per-file debounce cancellation.
- `diagWG` and `diagErr` for waiting on workers and retaining their first error.
- `mu` for state, cache, compiler snapshot, workspace, and debounce metadata.
- `publishMu` for the publication/mutation boundary.

`NewServerState` initializes the maps. Most state reads and writes use `mu`.
`applyDocumentSnapshot` acquires `publishMu` before `mu`; this ordering prevents a
publisher from validating a generation while a newer document mutation is
simultaneously committed.

`publishDiagnosticSnapshot` acquires `publishMu`, then checks
`diagGeneration == snapshot.generation` under `mu` before writing notifications. The lock is
held while notifications are written. A mutation therefore cannot pass the
publication boundary after a snapshot has been accepted for publication.

The request loop itself is sequential, but diagnostic refreshes run in goroutines.
The protocol writer remains safe when those workers publish concurrently with the
request loop.

## Document overlays and generations

Peeper uses full synchronization. A document snapshot is the complete text from
`didOpen` or the first `didChange` content item. `applyDocumentSnapshot` canonicalizes
the path, stores text in `Cache`, and stores the supplied LSP version. Closing the
document deletes both entries, so later reads fall back to disk.

Every open, change, or close increments `diagGeneration`, including removal of an
overlay. `diagnosticSnapshotLocked` recompiles while state is locked, copies the
selected file list, captures current document versions, and records the current
generation in `diagnosticSnapshot`.

`currentCompiledModule` reuses `LastCtx` only when `lastCtxGeneration` equals the
current `diagGeneration`, the module is parsed, and its content hash matches
current workspace content. Otherwise it recompiles. This prevents hover,
definition, and rename from reading an AST from an older buffer.

A diagnostic result can therefore become stale in two ways:

1. A later edit increments `diagGeneration`; publication drops a snapshot whose
   generation no longer matches.
2. A later edit to one file increments its `diagVersion`; the older debounce
   worker sees a different per-file version and exits without compiling.

Stale diagnostic results are silently rejected, not sent to the client. Diagnostic
notifications include a version only when the snapshot captured a known
open-document version.

## Debounce

`diagnosticsDebounceDelay` is 150 milliseconds. Each full-sync change increments
`diagVersion[filePath]`, starts a worker, and sleeps for the requested delay. The
worker exits if its version is no longer current or if `diagErr` is already set.
Otherwise it calls the captured publish function. The first publish error is
stored in `diagErr`; `waitForScheduledDiagnostics` waits for all workers and
returns that error. EOF and `exit` use this wait before the session finishes.

This is debounce by version invalidation: old workers are not stopped, but they
do no work after a newer change supersedes them.

## URI, path, and position mapping

`uriToPath` accepts `file:` URIs without opaque data, query, or fragment. It
unescapes the path, rejects NUL, normalizes backslashes, supports non-localhost
file authorities as `//host/path`, and handles Windows drive paths. Invalid URI
forms become invalid-parameter errors for request handlers or are ignored by
notifications.

`pathToURI` normalizes backslashes and path cleaning, preserves UNC authorities,
and emits a `file:` URI. Internal cache, workspace, module, and version keys use
`project.CanonicalPath`; URI conversion is kept at the protocol boundary.

LSP positions are zero-based and use UTF-16 code units. `offsetAtPosition` walks a
line and counts one or two UTF-16 units per decoded rune. `positionAtOffset` does
the inverse for a byte offset. Compiler source positions are one-based line and
column values; `sourcePositionAt` and `offsetAtSourcePosition` bridge that model.
`rangeAtLocation` converts compiler locations into LSP ranges. Diagnostic labels
use this conversion when source text is available; otherwise diagnostics get a
zero range.

## Workspace index and incremental compilation

`workspaceIndex.rebuild` discovers source files under `RootDir` and adds cached
source files within that root. Cached content wins over disk content in
`workspaceContent`. Each indexed `workspaceModule` records canonical path,
project/import context, content hash, import/export fingerprints, and resolved
local import targets.

Changed or new files are lexed and parsed only for index construction. The index
builds a directed import graph and groups modules into weakly connected
`workspaceComponent`s. A component stores sorted files and roots. Roots are
zero-in-degree modules; if a component has no root, its first sorted file is used.

`syntheticEntry` creates a temporary `.peeper-lsp/__workspace__.pp` source file
for a disk-backed component. It imports component roots and adds an empty
`WorkspaceEntry` function. This allows component-level compilation while retaining
normal project import resolution. Synthetic modules are not retained in the
module cache.

`dirtyFiles` compares cached module hashes with indexed hashes. A changed content
hash marks a file dirty. An import or export fingerprint change propagates dirtiness
to reverse dependents through the import graph. If no difference is found, the
requested file is still marked dirty as a fallback.

`reusePhases` decides how far retained modules can be reused:

- byte-identical modules retain their completed phase;
- changed modules with import/export surface changes propagate invalidation;
- unchanged-text dependents of a changed surface are retained only through
  `phase.Parsed`, so semantic and later phases rerun.

`seedReusableModules` applies this policy. It seeds semantic export baselines,
clones modules when their retained phase must be downgraded, restores source
content, copies reusable diagnostics through ownership, and defers later
phase diagnostics until the current project barrier reaches usage. `captureModules` retains parsed, non-synthetic modules and their workspace
fingerprints after a compile. `activateReusableDiagnostics` enables deferred
usage-and-later diagnostics once the current context completed that barrier.

## Recompile path

`recompileLocked` canonicalizes the entry path, resolves its source project, and
creates a fresh `project.CompilerContext` with a new diagnostic bag and metrics.
If source-project resolution fails, it records a load diagnostic and retains the
context.

For a configured root, it rebuilds the workspace, computes dirty files, seeds
reusable modules, adds cached sources, and tries the synthetic component entry.
If that entry produces the requested module, the context is retained and returned.
Otherwise cached sources are added and the requested file is compiled with its
current overlay, if present. Reusable diagnostics are activated, then the context
and modules are retained.

`retainCompiledContext` updates `LastCtx`, `LastMetrics`, and
`lastCtxGeneration`. `diagnosticSnapshotLocked` selects a component's files when
its caller supplies no file list. `workspaceDiagnosticSnapshots` rebuilds the
workspace and creates one snapshot per non-empty component during initialization.

This is incremental at module/phase granularity, not incremental text parsing:
the current entry is compiled through the compiler pipeline, while unchanged
modules and authorized phase artifacts can be reused.

## Diagnostics

`publishWorkspaceDiagnostics` publishes one snapshot per component. Open and close
publish immediately through `publishComponentDiagnostics`; changes use the
150-millisecond debounce.

`diagnosticNotifications` initializes an empty diagnostic array for every selected
file, then filters compiler diagnostics to those files. It maps severity from the
compiler's error, warning, info, and hint values to LSP severities 1 through 4.
The diagnostic code and `Peeper` source are preserved. Extra diagnostic text of
kind `help` or `note` is appended to the message. A notification is emitted even
when a selected file has no diagnostics, clearing old client diagnostics.

## Shared cursor and symbol resolution

`cursor.go` builds `cursorContext` from one compiled module. `walkModuleAST` uses
`ast.Inspect` over imports and statements, maintaining a parent stack. It selects
the deepest node containing the compiler source position and records parent links.

`resolveIdentSymbol` first uses `module.Bindings.NodeSymbols`. It then handles
selector members, imported scope-resolution members and qualifiers, block/function
scopes, and module scope. Selector lookup uses the effective expression type,
struct field lookup, and receiver method lookup. `normalizedSelectorBaseType`
removes invalid/unknown results and pointer indirection before member lookup.
`lookupStructFieldSymbol` finds the declared field node and type across compiled
modules.

Hover, definition, rename, and completion reuse this cursor/context machinery
rather than maintaining independent AST parent maps for the same operation.

## Hover and rendering

`HandleHover` reads current overlay/disk text, converts the LSP position, resolves
a subject from the current compiled module, renders it, and returns Markdown plus
its source range. `resolveHoverSubject` checks attributes, imports, types,
declarations, selectors, symbols, and expressions in that order.

The normalized `hoverSubject` can represent a symbol, expression type, resolved
type, declaration, import, or attribute. Type syntax is resolved through
`typeinfo.TypeFromSyntax` with project/module syntax options. Effective expression
types come from the compiled module. Method sets are collected from module
bindings for resolved types.

`renderHoverSubject` emits a `peeper` fenced code block. Symbol and callable
signatures come from `renderSymbol`; expressions show `(expr): <type>`; types can
include inner struct/interface/enum details and methods; imports show their import
path; attributes show `#[name]`. Documentation comments are appended as Markdown
outside the fence. `symbol_render.go` formats mutability, receiver parameters,
generic parameters, defaults, return types, and return-origin text.

## Definition and rename

`HandleDefinition` compiles or reuses the current module, resolves the identifier
at the cursor, reads the target symbol location, and returns one LSP location with
its canonical file URI and converted range. Missing source, position, symbol, or
location returns no result.

`HandleRename` validates the new symbol name, resolves the target symbol, then
walks every AST module in `LastCtx`. It matches identifiers by resolved symbol
location, while retaining location matching for unresolved identifiers. It skips
fixed receiver return-origin identifiers (`self`) and emits grouped text edits by
file URI. It returns an empty edit for that fixed receiver case.

## Completion

`HandleCompletion` reads the current source and compiled module, then
`parseCompletionContext` classifies the cursor as names, operation, qualified,
or import completion. It rejects comments, character literals, and unrelated
strings. Replacement ranges are built from byte offsets and converted to UTF-16
LSP positions.

- Import completion uses `ctx.ImportCandidates` and returns file/folder items.
- Name completion walks the innermost block scope and parents, hides declarations
  after the cursor, deduplicates names, and sorts results.
- Qualified completion returns enum variants or public symbols from an imported
  module.
- Operation completion builds a sentinel source, compiles it with other cached
  overlays, resolves the base type, and offers fields, interface methods, receiver
  methods, intrinsics, adaptable local operations, and public imported operations.
  It creates edits for method-call or pipe syntax and can preserve existing
  arguments.
- Match-arm completion finds the typed enum subject, removes already-used cases,
  and creates snippet edits for remaining variants.

Completion details use `renderSymbol`; `sortCompletionItems` provides deterministic
ordering by sort text, label, and item kind.

## Invariants

- Protocol paths become canonical internal paths; internal paths become `file:`
  URIs only at response/notification boundaries.
- Compiler contexts are never published as diagnostics after their generation is
  invalidated.
- Query handlers do not reuse a compiled module whose generation or content hash
  no longer matches the current document overlay.
- Full-sync changes replace complete document snapshots; the server does not apply
  text ranges.
- Workspace reuse is constrained by content and import/export fingerprints; a
  surface change re-runs dependent semantic work.
- Diagnostic writes are serialized with protocol writes and fenced against state
  mutation by `publishMu`.
