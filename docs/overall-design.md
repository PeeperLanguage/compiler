# Overall design: incremental compiler and LSP

Status: PROPOSAL, not yet approved.

This puts the decisions so far (see `architecture.md`) together with one more
goal: **after a small edit, only the work that the edit really affects runs
again**, both in the LSP, which keeps everything in memory, and in the command
line compiler, which starts fresh on every run.

## The idea in one paragraph

Every step is a **pure function**: its output depends only on its inputs. Each
input is named by a **fingerprint** (a hash of its content). A step's **key** is
the hash of all its input fingerprints. Before running a step, look its key up in
a **cache**; on a hit, reuse the stored result instead of running the step. The
LSP keeps the cache in memory; the command line keeps it on disk. It is the same
code with a different store. Reuse stops spreading as early as possible: a module
whose **interface** (what importers can see) did not change does not make its
importers run again, even if its function bodies changed.

## 1. Pipeline

> Superseded by [`ir-design.md`](ir-design.md) (proposal, 2026-10-17): check builds a THIR per body; there is a `thir` crate and no check tables over the AST.

```mermaid
flowchart LR
    src["source files"] --> load
    subgraph load["load (parallel workers)"]
        lex["lexer"] --> parse["parser"] --> ast["AST (flat)"]
    end
    load --> modgraph["module graph<br/>cycle check + order"]
    modgraph --> check
    subgraph check["check (per module, parallel when imports are done)"]
        names["names + types + constants<br/>one pass"] --> tables["result tables<br/>types[node], symbols[node]"]
        tables --> iface["interface + its fingerprint"]
    end
    check --> mir
    subgraph mir["mir (per module)"]
        lower["lower AST + tables to CFG"] --> flow["borrow / move / init /<br/>missing return checks"]
    end
    mir --> llvm["llvm: object file per module"]
    llvm --> link["link"]

    cache[("cache<br/>memory: LSP<br/>disk: command line")]
    load <-.-> cache
    check <-.-> cache
    mir <-.-> cache
    llvm <-.-> cache
```

Every step returns `(result, diagnostics)`. Diagnostics are cached with the result
and shown again when the result is reused, so warnings never disappear on a
cached run.

## 2. Fingerprints and keys

```mermaid
flowchart TD
    text["file text"] -->|hash| srcfp["source fingerprint"]
    srcfp --> parsekey["parse key"]
    cfg["compiler version + target + options"] --> parsekey
    cfg --> checkkey
    srcfp --> checkkey["check key"]
    impfp["interface fingerprint<br/>of each import"] --> checkkey
    checkkey --> checkres["check result:<br/>tables, interface, diagnostics"]
    checkres -->|hash of the interface only| ifacefp["interface fingerprint<br/>(feeds importers' keys)"]
    checkkey --> mirkey["MIR key"]
    mirkey --> mirres["MIR + flow diagnostics"]
    mirkey --> objkey["object key"]
    cfg --> objkey
    objkey --> obj["object file"]
```

What goes into a module's **interface** (and so into its fingerprint):

- names and signatures of exported functions, types, constants and variables;
- constant values;
- the syntax of exported generic declarations and of default argument values,
  because importers build instances and expand defaults from them;
- type layouts that importers lower against.

Function bodies are **not** in the interface. Editing a body changes the
module's own keys only.

The interface is computed by `check` from its results. (The old compiler built
its export fingerprint in the parser, which made the parser own a semantic
decision.)

## 3. Early cutoff

`main` imports `util`. The user edits the body of a function in `util`.

```mermaid
flowchart LR
    e["edit body of util.f"] --> u1["util: source fingerprint changed"]
    u1 --> u2["util: check runs again"]
    u2 --> u3{"util interface<br/>fingerprint changed?"}
    u3 -->|no| m1["main: check key unchanged<br/>reuse check, MIR, object"]
    u3 -->|yes| m2["main: check runs again"]
    m2 --> m3{"main interface changed?"}
    m3 -->|no| stop["stop: main's importers reused"]
    m3 -->|yes| next["continue to main's importers"]
    u2 --> u4["util: MIR and object run again"]
```

The same check happens at every level, so a change spreads only as far as
interfaces actually change.

## 4. Scheduling one module

```mermaid
stateDiagram-v2
    [*] --> Waiting: path known
    Waiting --> Ready: every import has its interface fingerprint
    Ready --> Reused: key found in cache
    Ready --> Running: key not in cache
    Running --> Done: result stored in cache
    Reused --> Done
    Done --> [*]
```

A small loop walks the graph in order; all `Ready` modules run in parallel on a
pool of workers. There is no phase enum and no per-module mutable phase field:
a module is `Done` when its result exists.

## 5. Command line run (stateless, disk cache)

```mermaid
sequenceDiagram
    participant CLI as peeper build
    participant D as disk cache
    participant W as workers

    CLI->>D: read manifest (path, source fingerprint, imports)
    CLI->>CLI: hash every source file
    Note over CLI: an unchanged file is not even parsed:<br/>its imports come from the manifest
    loop modules in graph order, in parallel
        CLI->>CLI: key = hash(source fp, import interface fps, options)
        CLI->>D: lookup key
        alt hit
            D-->>CLI: interface, diagnostics, object file
        else miss
            CLI->>W: parse, check, MIR, LLVM
            W-->>CLI: result
            CLI->>D: store (write to temp file, then rename)
        end
    end
    CLI->>CLI: link object files, print diagnostics (sorted)
```

What the disk cache stores per module: the manifest entry, the serialized
interface (importers that must re-check read it instead of re-checking this
module), the diagnostics, and the object file. It does **not** store ASTs, tables
or MIR: a module that must run again re-parses its file, which is cheaper than
reading a stored tree.

Rules for the disk cache:

- the compiler version, target and options are part of every key, so an old
  cache is simply never hit;
- entries are content addressed and never changed; each is written to a temporary
  file and renamed into place, so a crash or two builds at once cannot leave a
  half-written entry;
- a corrupt or unreadable entry is a miss, never an error;
- the cache can be deleted at any time.

## 6. LSP edit (in memory)

```mermaid
sequenceDiagram
    participant E as editor
    participant L as LSP
    participant M as memory cache

    E->>L: didChange main.peep
    L->>L: cancel work for the older version
    L->>L: parse main.peep (buffer text)
    L->>M: check key of main
    M-->>L: miss
    L->>L: check main
    alt interface unchanged
        L->>L: importers of main keep their results
    else interface changed
        L->>L: re-check importers, level by level, with early cutoff
    end
    L->>L: MIR flow checks for open files only
    L-->>E: publish diagnostics (only for finished work)
    E->>L: hover / completion
    L->>M: read types[node], symbols[node] of the latest result
    M-->>E: answer
```

- All other modules' results are reused **by pointer**: they are immutable, so
  sharing them between versions is safe.
- Results are published only when complete; a newer edit cancels unfinished work.
- The memory cache keeps the latest result per module and drops older ones.

## 7. Data and ownership

> Superseded by [`ir-design.md`](ir-design.md) (proposal, 2026-10-17): check results live in THIR bodies and signatures in `ir`, not in tables over the AST.

```mermaid
classDiagram
    class Module {
        Path string
        Source *source.File
        Syntax *ast.Tree
        Imports []Import
        Diagnostics []Diagnostic
    }
    class CheckResult {
        Types []Type
        Symbols []Symbol
        Interface Interface
        InterfaceFingerprint Hash
        Diagnostics []Diagnostic
    }
    class MIRResult {
        Functions []Function
        Diagnostics []Diagnostic
    }
    class Cache {
        lookup(key) result
        store(key, result)
    }
    Module --> CheckResult : read by check
    CheckResult --> MIRResult : read by mir
    Cache o-- CheckResult
    Cache o-- MIRResult
```

- Every value is written once, by one step, and is immutable after that.
- The cache is the only shared structure. It only adds entries and never changes
  one, so concurrent use cannot corrupt results.
- A step for module M reads M's own data and the finished results of M's imports,
  nothing else.

## 8. Packages

> Superseded by [`ir-design.md`](ir-design.md) (proposal, 2026-10-17): crates are `ir`, `thir`, `mir`, `llvm`, `build` as listed there.

```mermaid
flowchart TD
    cmd["cmd: peeper"] --> build
    lsp["lsp"] --> build
    build["build: graph walk, keys, cache"] --> project
    build --> check
    build --> mir
    build --> llvm
    build --> cache
    project["project: files, imports, graph"] --> parser
    parser --> lexer --> token --> source
    parser --> ast --> source
    check --> types
    check --> ast
    mir --> check
    mir --> types
    llvm --> mir
    cache["cache: memory or disk store"]
    lexer --> diag
    parser --> diag
    check --> diag
    mir --> diag
    diag --> source
```

| package | owns |
| --- | --- |
| `source`, `token`, `lexer`, `ast`, `parser`, `diag` | done or in progress |
| `project` | reading files, resolving imports, the module graph |
| `check` | names, types, constants, interface, interface fingerprint |
| `types` | the type model |
| `mir` | lowering, all control-flow checks |
| `llvm` | object files |
| `cache` | one interface, two stores (memory, disk) |
| `build` | the graph walk: keys, cache lookups, parallel workers; used by `cmd` and `lsp` alike |
| `lsp` | editor protocol; calls `build` with buffer text and a memory cache |

Compared with the old compiler: no `pipeline`, `phase`, `project`, `module`
registry, `prelude`, `collector`, `binder`, `resolver`, `typeresolution`,
`symbols`, `place`, `usage`, `intrinsics`, `analysis`, `thir`, `cfg`,
`exprlower`, `typelower` or `constvalue` package.

## Cost on the hot path

- Hashing all source files on each command line run: fast (about 1 GB/s with a
  standard hash), and it replaces parsing for unchanged files.
- One cache lookup per module per step.
- Computing the interface fingerprint: one walk over a module's exports after
  checking.
- With a flat AST and result tables, results are a few arrays each, which makes
  them cheap to keep in memory and to serialize for the disk cache.

## Decisions for the user

1. Unit of reuse: one module (file) to start; per-function reuse for MIR and
   object files can come later without changing the design.
2. What the disk cache stores: manifest, interface, diagnostics, object file
   (proposed), but not ASTs, tables or MIR.
3. Where the disk cache lives: in the project (`.peeper/cache/`) or in the user's
   cache directory.
4. The hash function: a fast non-cryptographic 128-bit hash, or SHA-256 (slower,
   but no collision worry).
5. Generic instances: built by the importing module from the declaring module's
   interface (the declaration syntax is part of it), merged by identity at MIR or
   link time.
