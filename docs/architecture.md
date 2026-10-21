# Compiler architecture: the base

Status: APPROVED by the user on 2026-10-16 (all five decisions as proposed).

This document designs only the base of the compiler: reading many source files,
parsing them in parallel, resolving imports, and building the module graph.
Language features (types, methods, interfaces, generics) come later and must fit
inside the rules set here. If the base is wrong, every later phase inherits it.

## Goals

1. Many files are parsed at the same time, with no data race by design, not by luck.
2. The same input always gives the same output, whatever the thread timing.
3. The LSP can re-parse one edited file and reuse everything else safely.
4. The fewest types, fields and functions that do the job. No duplicated facts.

## What the old base did wrong (evidence)

- **One mutable `Module` shared by everything.** The loader, every phase and the
  LSP wrote into the same struct (`Content` set then cleared, `AST`, `Imports`,
  `Phase`, scope, index). Other goroutines read it while it changed. This is the
  root of #165 (one module writing another module's symbol index) and of the
  prelude race (the binder rewriting fields the collector owned).
- **Shared registries behind locks.** `CompilerContext` held a locked module map,
  a locked import graph and a `scheduled` set; workers called into it. Every
  phase could reach every other module's mutable state.
- **Identity stored four ways.** `moduleid.ID{Origin, Namespace, Dependency,
  ImportPath}`, plus `FilePath`, plus a canonical path, plus a string encoding,
  plus conflict rules to keep them in agreement.
- **The prelude was a special case everywhere.** Separate loading
  (`prelude.Load`, `ModuleForFile`), injection into a global scope, special
  scheduling waits (which caused a deadlock risk), extra graph edges.
- **LSP reuse by shallow copy.** `reused := *module` shared maps and pointers
  between the old and the new compile.
- **One shared diagnostics bag** with phase groups, module scopes, function
  groups and a mutex, so that goroutines could write into it.

## The rules (apply to the base and to every later phase)

1. **One writer per fact.** Each value is created by exactly one step, and it is
   complete when that step returns.
2. **Immutable once handed over.** After a step returns a value, nobody changes
   it. Values move between goroutines over channels; nothing else is shared.
3. **A step reads only its inputs.** A step for module M reads M's own data and
   the finished, immutable results of the modules M imports. It never reads
   another module's work in progress, and it never writes another module's data.
4. **No global registry.** The coordinator owns the collection of modules. Workers
   get inputs and return results; they do not look things up in shared state.
5. **Determinism comes from data, not timing.** Results are ordered by module path
   before they are printed or used.

## The design

### Identity: one string

A module is one source file. Its identity is its **import path**, one string of
the form `package:path`:

- `myapp:utils/file`: a file of this project (`myapp` is the name in its
  `peeper.toml`; the path is under its `src/`, without the extension);
- `core:global`: a file of a bundled library (`core`, under `libs/core/src/`);
- later, downloaded packages the same way (`json:parse`), named in the dependency
  file the tool writes.

One file has one spelling from every importer; there are no relative paths.
The compiler core gets a map from package names to folders and nothing else; it
never reads `peeper.toml`. `peeper check` on a folder reads that folder's
`peeper.toml` (folders above are not searched); `peeper check file.peep` checks
the file on its own, with no project and no imports.

This string is the module's name in the graph, in diagnostics and, later, in
linking. The file path on disk is computed from it when the file is read, and is
not stored as a second identity.

### Data: two types

```
Module   the result of loading one file; immutable once created
  Path        string            import path (identity)
  Source      *source.File      file name, text, and where each line starts
  Syntax      *ast.Module       parsed source
  Imports     []Import          in source order, each already resolved
  Diagnostics []diag.Diagnostic produced while loading this file

Import
  Path   string                 resolved import path of the target
  Alias  string                 name used in this file
  At     source.Span            where the import is written
```

Positions are byte offsets. A `source.Span{Start, End}` is a plain value, so
tokens and syntax nodes allocate nothing for their location and do not repeat the
file name. `source.File` turns an offset into a line and column only when a
message is printed, using the line starts it records once when the file is read.

`Program` is just `map[string]*Module` plus the order computed from the graph. It
is built by the coordinator and is immutable once loading ends.

### Loading: one coordinator, many workers

```
coordinator (owns the map and the "seen" set)
   |  send path                       ^  receive finished *Module
   v                                  |
worker: read file -> lex -> parse -> resolve each import string -> return Module
```

1. The coordinator starts with the entry path (and the prelude path, see below)
   and sends each new path to a pool of workers (size = number of CPUs).
2. A worker reads, lexes and parses its file, resolves every import string into
   an `Import` (a pure function of the package map and the string), records
   its own diagnostics, and sends the finished `Module` back.
3. The coordinator stores it, and sends every import path it has not seen yet.
4. When no work is left, loading ends. No locks: the map is touched only by the
   coordinator, and each `Module` is touched only by its worker until it is sent.

Import errors (bad path, not found, duplicate alias) belong to the importing
file's diagnostics, because its worker found them.

### The graph

After loading, the coordinator checks for import cycles once (a depth-first
search over `Imports`) and computes a topological order, breaking ties by path so
the order never depends on timing. A cycle is an error naming the files in it.

### The prelude: an ordinary import

Every module except the prelude itself gets an implicit `Import` of
`core:global`. It is loaded, parsed and ordered like any other module. Making its
names visible everywhere is a job for name resolution later, not for loading.
No special loading path, no injection step, no special scheduling.

### Diagnostics: owned by the module

Each `Module` carries the diagnostics its worker produced. Printing sorts modules
by path and prints each one's list. There is no shared bag and no lock. Later
phases follow the same rule: a phase returns its diagnostics with its result.

### LSP and incremental reuse

Because a `Module` never changes, the LSP keeps the last `Program` and, after an
edit, re-loads only the edited file (and any file whose import list now names a
new path). Every other `*Module` is reused by pointer; sharing it is safe because
nobody writes it. The graph is rebuilt from the map, which is cheap. Later phases
reuse their results the same way: a result is reused when its inputs (the
module's own syntax and the results of its imports) are the same values.

## Later phases (contract only)

> Superseded by [`ir-design.md`](ir-design.md) (proposal, 2026-10-17): check builds THIR per declaration, scheduled on demand inside a module, not phase by phase per module.

Each later phase is a function: `(this module's earlier results, imported modules'
finished results) -> new immutable result + diagnostics`. The scheduler runs a
module's phase once the results it needs from its imports exist. A phase never
changes an earlier result and never writes another module's data. Generic
instantiation, default arguments and similar features must be designed inside
this rule (#165 came from breaking it).

## Packages for the base

| Package | Owns | Comes from |
| --- | --- | --- |
| `source` | source files and byte spans; line and column on demand | rewritten (old one tracked line and column per character and allocated every location) |
| `token`, `lexer` | tokens, lexing | old frontend, reviewed |
| `literal` | values of string, character and byte literal tokens, shared by the lexer, the parser and later phases | split out of the lexer |
| `pkg/numeric` | rules of number literals (base, digits, suffix; values and fits-in-type come in M4), shared by the lexer, the parser and the type checker | rewritten; old one was regex based |
| `parser` (tree in `model::ast`) | syntax tree, parsing | old frontend, reviewed (parser only parses) |
| `diagnostics` | one diagnostic value and how it is printed | rewritten small; the old Go emitter and `suggest.go` reviewed |
| `project` | import resolution from a package map, workers, graph, cycles; `project::manifest` reads `peeper.toml` in one folder (the project name) | new; ideas from old loader/resolver |
| `peeper` | `peeper check <path>` | new |

Every item brought back from the old code passes three questions first: is it
needed now, is it the simplest form, does something else already do it.

### Parser layout

The parser uses two methods, each in its own files, so every file stays small
and has one job:

- **Pratt parsing for expressions** (`expr.rs`), driven by one table indexed
  by token kind, as in the Go parser: `rules.prefix(kind, rule)` and
  `rules.infix(kind, precedence, rule)` register each token's rule in one line.
  Whether a token starts an expression is read from the table, never kept in
  a second list.
  Literal rules live in `literals.rs`, the operators and the table in
  `expr.rs`.
- **Recursive descent for everything else**: statements (`stmt.rs`), types
  (`types.rs`), declarations (`decl.rs`), patterns. The keyword that begins
  a statement or declaration picks its rule from a table too (`STATEMENTS`,
  `DECLARATIONS`), the same way.
- Rules in a table are plain functions taking `p: &mut Parser`; shared helpers
  stay methods.
- `lib.rs` holds only what all rules share: the token cursor, errors and
  recovery, lists.

One kind of syntax per file. When a file grows past about 500 lines of code
(tests not counted), split it by kind of syntax, not by size alone. A new rule
goes in the file of its kind; it never goes in `lib.rs`.

## How we know the base works

- A project of several files with nested imports loads and parses; the module
  order is the same on every run.
- Errors: import not found, path outside the project, wrong library, duplicate
  alias, import of a folder, and an import cycle (with its path) are reported at
  the right import line.
- No data race: the workspace forbids `unsafe`, so Rust's `Send` and `Sync`
  rules keep races out at compile time; a test loads a many-module project in
  parallel and gets the same order every time.
- Re-loading after editing one file re-parses only that file.

## Decisions for the user

1. Identity is one import-path string (proposed), instead of a struct with
   origin, namespace and dependency fields.
2. A module is one file (as today), not a folder of files.
3. Diagnostics are owned by each module and printed sorted (proposed), instead
   of one shared, locked bag.
4. The prelude is an implicit ordinary import (proposed).
5. Import cycles stay an error.

## Diagnostics

Status: APPROVED by the user on 2026-10-16 (all five decisions as proposed, plus
editor quick fixes).

Every step reports problems, so this must be right before more steps exist.

### What a diagnostic must handle (from the old compiler's real output)

- A primary place and secondary places, each with a short message, and they may
  be in **different files** (an error in `main.peep` with "defined here" in
  `global.peep`).
- Notes and help lines; help may carry fixes (insert, remove or replace text),
  shown as the fixed line. Several alternative fixes are just several help lines.
- No place at all (a file that cannot be read, an import cycle summary).
- Errors, warnings and style notes (`S` codes), counted in a final summary.

### Design

```
Diagnostic
  Code     Code       declared once with its ID and severity
  Message  string
  Labels   []Label    first is the primary place; may be empty
  Notes    []Note

Code    ID string, Severity Severity     e.g. Code{"L0002", Error}
Label   File *source.File, Span source.Span, Message string
Note    Help bool, Text string, Fixes []Fix
Fix     File *source.File, Span source.Span, NewText string
```

- **Self-contained.** A label names its own file, so a diagnostic can point into
  any module, and printing needs no lookups. The pointer is shared, not copied.
- **Severity is declared with the code.** Each code is one declaration holding
  its ID and its severity; a diagnostic holds the code. Renaming or renumbering
  an ID cannot change a severity, and no diagnostic can carry a severity that
  disagrees with its code. (Deriving severity from the ID's first letter was
  rejected: IDs can change.)
- **Owned and returned.** Each step returns its diagnostics with its result
  (already true for the lexer). There is no shared bag and no lock.
- **Deterministic order.** Before printing, diagnostics are sorted by the file
  and position of their primary place; ones without a place come first, in the
  order they were made. The same input always prints the same way.
- **Printing is separate from data.** Colors and layout live only in the
  printer. The printer never panics: a span outside its file's text is clamped
  and still printed.
- **Visible format unchanged.** `[CODE]: message`, `--> file:line:col`, the
  numbered source lines with `^` under the primary place and `-` under secondary
  ones, `= note:` / `= help:` lines with fixed lines, and the summary line. Users
  already know it.
- **Codes defined once.** Every code is a constant in `diagnostics` with a one-line
  meaning.
- **Suggestions.** The "did you mean" helper (`nearest_name`, from the old Go
  compiler's `suggest.go`) comes back as part of `diagnostics`.

### Editor quick fixes

The fixes are the data an editor needs for clickable quick fixes (LSP code
actions):

- each help note with fixes is one action: its text is the title, and all its
  fixes are applied together, possibly in several files;
- several help notes are alternatives the user picks from;
- when a diagnostic has exactly one help note with fixes, that action is the
  preferred one (an editor may apply it with one key or "fix all"); this is
  derived, not stored, and alternatives are never preferred;
- a fix's spans must lie inside its file's text and must not overlap; a broken
  fix is dropped and reported as an internal error, never applied;
- the LSP converts byte offsets to its UTF-16 positions with one `source.File`
  function, added with the LSP;
- a `peeper fix` command can later apply all preferred fixes from the command
  line, using the same data.

### Not now

- Coloring code inside snippets (syntax highlighting): it would make `diag`
  depend on the lexer, which already depends on `diag`. Can come back later as
  a printer option.
- HTML output: the LSP will take diagnostics as data, not text.
- Hiding internal compiler error details: comes back with the first internal
  error code.

### Tests

Golden tests for the printer: primary and secondary labels in two files, a span
over several lines, tabs and wide characters, CRLF line ends, notes and help
with fixes, a diagnostic with no place, a span outside the text (no panic), the
summary, colors off.

### Decisions for the user

1. Labels and fixes carry their own file (proposed).
2. Severity is declared together with each code (changed by the user from
   "derived from the code's first letter").
3. Keep the visible output format (proposed).
4. Print sorted by file and position (proposed).
5. Syntax highlighting in snippets later (proposed).
