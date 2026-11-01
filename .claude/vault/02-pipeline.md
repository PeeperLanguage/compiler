# Pipeline as a state machine

From `crates/driver/compile/src/lib.rs`, `crates/middle/check/src/function.rs`,
`crates/middle/check/src/lib.rs` and `docs/ir-design.md`.

## One compile

```mermaid
stateDiagram-v2
    [*] --> Loading: project::load (workers parse files in parallel)
    Loading --> NotCheckable: a loading error, or a tree or imports left incomplete (Program.checkable)
    NotCheckable --> [*]: loading's diagnostics only
    Loading --> Scopes: Checker::new collects every module's scope
    Scopes --> Module: modules in program order (imports first)
    state Module {
        [*] --> DeclaredTypes: checker.declared_type for each type and alias
        DeclaredTypes --> Reused: text same, imports unchanged, pointed-at files reused
        DeclaredTypes --> Compiling: otherwise
        Compiling --> Interface: compile_module, then hash the interface
        Reused --> [*]
        Interface --> [*]
    }
    Module --> Module: next module
    Module --> EntryPoint: entry given and no error
    Module --> Done: no entry, or an error
    EntryPoint --> Done: find_entry_point (M0006 when missing)
    Done --> [*]: Results kept for the next compile
```

- `compile_module` walks the module's declarations in order: a function goes
  through `Checker::build_function` and out as `Output::Function` (THIR, MIR,
  facts); a constant through `Checker::constant` as `Output::Constant`; last
  the module's remaining `Output::Facts`.
- A `ModuleResults` keeps only the file, interface hash, diagnostics,
  `reused`, `changed` and `ran_modules` between compiles; ids, types, THIR and
  MIR are rebuilt every compile.
- Consumers of `Output` (`use_output` callback): the CLI driver (LLVM writer,
  dumps), `compile::interpret` (keeps MIR after `flow::cleanup`), the LSP
  (facts). Nothing holds every body at once.

## One function body (`Checker::build_function`)

```mermaid
stateDiagram-v2
    [*] --> FunctionType: function_type(d)
    FunctionType --> NoBody: no body or signature error
    FunctionType --> CheckBody: check_body, AST to THIR in one walk
    CheckBody --> Errors: an error reported, THIR kept with has_errors
    CheckBody --> Lower: lower::lower, THIR to MIR
    Lower --> PathChecks
    state PathChecks {
        [*] --> VariableState: T0039 T0034 T0035 T0007
        VariableState --> Loans: T0037 T0036 T0016
        Loans --> Unreachable: W0001
        Unreachable --> MissingReturn: T0017, last
    }
    PathChecks --> MIR: handed on, None when an error was found
```

`Built` per function (`function.rs`): `NotYet` → `Reported` (first build
reports its diagnostics; a later build drops what it finds again) or `Kept`
(built for a compile-time run, MIR kept in `Checker` for the next run).

## Per declaration (`check::State`)

`NotStarted` (or being checked: then it is on `Checker::checking`) →
`Done(T)`. Asking for a declaration that is on `checking` is a cycle,
reported once (T0013). Tables: `declared_type(d)`, `function_type(d)`,
`constant(d)`, `body(d)`, `mir(d)` (`docs/ir-design.md`, Scheduling).

## After the checks

- `flow::cleanup::remove_unused_code` drops `scope end`s, jump-only blocks
  and unreachable blocks; only readers of a compiled body run it (backend,
  dumps, `compile::interpret`). A compile-time run keeps `scope end`s.
- `llvm`: one walk per function's MIR to LLVM text; `clang -c` per module;
  link with `libpeeper_rt_v1.a` and `-lm` (`docs/ir-design.md`, LLVM).
- `eval::interpreter::program::run`: the same MIR, for `run -interpret` and
  the browser.

## Ownership rules the machine relies on (`docs/architecture.md`)

One writer per fact; immutable once handed over; a step reads only its
inputs; no global registry; determinism from data (order by module path).
