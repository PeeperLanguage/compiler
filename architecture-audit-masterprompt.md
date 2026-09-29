# Master prompt: Peeper compiler architecture audit

Use this prompt to resume architecture audit in another agent session. Repository source proves current implementation, not desired architecture. Tests prove current contracts, not that those contracts are well designed. Every markdown document—including `RULES.md`, `RULEBOOK.md`, specifications, architecture guides, and generated maps—is evidence to evaluate, not unquestioned design authority.

## Start state

Run from compiler repository supplied by user. Never assume saved path, branch, commit, or worktree snapshot remains current.

Before work:

1. report repository root, branch, HEAD, and `git status --short`;
2. read `architecture-audit.localplan.md` when present;
3. identify existing user changes and unrelated files;
4. do not reset, discard, stage, commit, or overwrite existing work.

## Task

Perform end-to-end architectural review of Peeper compiler. This is not ordinary bug review and not request for immediate refactoring.

Evaluation goals:

- high cohesion and low coupling;
- one canonical owner where one owner reduces inconsistency;
- simple, linear, self-explanatory control flow;
- fewer repeated walks, switches, conversions, validations, and test setups where repetition has no distinct purpose;
- explicit contracts where they clarify real boundaries;
- minimal rediscovery of decisions already proven elsewhere;
- adding features changes genuinely responsible code, not unrelated packages;
- abstractions remove work and concepts rather than moving code behind interfaces;
- tests prove module contracts and observable behavior.

These are evaluation principles, not a prescribed component graph. Dependency injection, query contracts, immutable results, visitors, direct concrete calls, or another pattern may be recommended only after comparing alternatives against actual code. Do not assume current phase boundaries, phase order, result models, package splits, identity schemes, or compiler terminology are correct because documentation names them.

Goal is **minimum coupling with maximum clarity**. Maximum modularity, maximum reuse, and any specific architecture pattern are not goals by themselves.

## Hard semantic constraints

Do not propose or make semantic changes during audit.

In particular:

- `anyfn() -> ?T` is required language behavior.
- Do not restore or design around obsolete `Next()` iterator protocol.
- Current iteration design uses explicit call iteration and canonical optionals.
- Older docs/issues that still describe `Next()` are outdated and should be identified separately from architecture defects.
- Definite initialization follows the approved rule in `docs/language-spec.md`: declarations without initializers create uninitialized storage, never implicit zero values; only whole-value assignment initializes that storage; projected writes into an uninitialized root are rejected; uninitialized storage owns nothing and receives no cleanup.
- Necessary coordinated edits across parser, semantics, lowering, and tests are valid. Do not label every repeated syntax switch as duplication.

## Audit inputs and evidence rules

Read before analysis:

1. `AGENTS.md`
2. `RULES.md`
3. `RULEBOOK.md`
4. `go-style.md`
5. `docs/language-spec.md`
6. `docs/compiler-architecture.md`
7. `architecture-audit.localplan.md`
8. relevant files under `docs/architecture/`

Do not use precedence declared inside these documents to settle architecture questions. Evaluate conflicts instead.

Evidence roles:

1. Explicit user requirements define audit goals and confirmed language requirements.
2. Source shows what implementation currently does.
3. Tests and fixtures show behavior currently protected; they may preserve a mistake and must also be evaluated.
4. Specifications, rules, architecture documents, comments, issues, and names show intended design claims; verify those claims against behavior and engineering purpose.
5. Established engineering guidance supplies reasoning tools, not automatic answers.

Separate three questions for every important claim:

- What exists now?
- What behavior is intentionally required?
- Is current design best way to implement that behavior?

`RULES.md` should contain durable guidance about how work is judged and performed. Flag rules that freeze one current pipeline, representation, package boundary, or design choice without enduring reason. Factual architecture belongs in architecture maps; proposed architecture belongs in design documents; neither becomes correct merely by being documented.

Reconstruct actual pipeline and dependencies from source. Then evaluate whether pipeline order, boundaries, models, and handoffs are justified. Do not hardcode documented phase order into findings or target design.

Audit is read-only unless user explicitly authorizes writes. Do not edit source, tests, generated files, docs, local plans, GitHub issues, or roadmap items during read-only work. Put handoff state in response when writes are not authorized.

Do not fabricate findings. Every reported finding needs source verification and design reasoning.

## What user values

Favor:

- less code and fewer concepts;
- ordinary `if`, `switch`, and `for range` when flow becomes clearer;
- visible tree/graph/collection walks;
- descriptive domain names;
- one phase-owned decision reused downstream;
- stable identity and explicit evidence;
- direct call sites over pass-through wrappers;
- deletion/reuse before new abstraction;
- boundaries justified by ownership, lifetime, phase, or independent reuse.

Reject:

- clever pointer-identity or traversal-order tricks;
- one-field wrappers without a present domain distinction, invariant, ownership boundary, or public API contract;
- old-name wrappers after renames;
- ignored parameters retained to avoid call-site updates;
- speculative interfaces and registries;
- generic visitor/dataflow frameworks that save lines but hide semantics;
- comments that restate code;
- test-only abstractions that obscure behavior;
- recommendations based only on aesthetics.

## Review scopes

Review all scopes. Parallelize independent scopes when subagents are available.

### Scope A: frontend and binding/type semantics

Inspect:

- `internal/frontend/lexer`
- `internal/frontend/token`
- `internal/frontend/parser`
- `internal/frontend/ast`
- collector, binder, resolver
- symbols, scopes, type information, places, constants
- base typechecker and call/iteration evidence

Questions:

- Are syntax walks duplicated without semantic need?
- Is node identity stable and intentionally owned?
- Are symbol/type/place lookups canonical?
- Do generated nodes have explicit identity and scope rules?
- Does typechecker publish evidence rather than force later rediscovery?
- How many true extension points does new syntax/type require?

### Scope B: CFG and semantic analyses

Inspect:

- `internal/ir/cfg`
- flow typing
- effects
- definite initialization
- ownership and cleanup
- usage
- their result models and validators

Questions:

- Do analyses consume canonical CFG and semantic evidence?
- Are repeated worklists/walks shared mechanics or distinct lattices?
- Are reads, writes, moves, borrows, definitions, calls, iteration, discard, and cleanup represented generically enough downstream?
- Does any analysis rediscover AST meaning already known earlier?
- Are `ast.NodeID`, `ir.NodeID`, `symbols.SymbolID`, and `cfg.SiteID` used for correct roles?
- Are normal, `break`, `continue`, `return`, and failure cleanup paths explicit?

Prior conversation says one subagent completed this scope, but its report may not survive. Treat `docs/architecture/semantics-analyses.md` as lead only. Re-verify important claims in source.

### Scope C: HIR, MIR, backend, and target

Inspect:

- shared `internal/ir` model
- HIR model, validation, lowering, folding
- MIR model, validation, lowering
- backend target/layout/ABI ownership
- LLVM emission

Questions:

- Does HIR consume semantic evidence rather than repeat typechecking?
- Does MIR consume HIR/CFG/ownership instead of rediscovering AST semantics?
- Are type lowering, ABI, mangling, physical layout, and runtime-carrier fields canonical?
- Are backend values and addresses carrying sufficient physical type evidence?
- Are validators checking each phase's own invariant?
- Which repeated type switches are required closed-world completeness checks?

### Scope D: infrastructure, LSP, CLI, packages, and tests

Inspect:

- `internal/project`
- `internal/pipeline`
- `internal/driver`
- diagnostics, graph, phase, module identity, source, toolchain
- `internal/lsp`
- `cmd` and `cmd/cli`
- manifests, registry, remotes, distribution
- scripts, test helpers, and `x_test/`

Questions:

- Is module lifecycle and phase reset ownership clear?
- Are snapshots taken before concurrent work and conditionally published by generation/version?
- Are pipeline setup and source discovery duplicated across CLI/LSP/tests?
- Do command aliases share handlers instead of wrappers?
- Are dependency graph/cache operations canonical?
- Is repeated test setup hiding missing production APIs or only fixture noise?

Prior conversation says one subagent completed infrastructure/LSP, but report may not survive. Treat architecture maps as leads only and verify source.

### Scope E: cross-cutting repetition and extension cost

Search across repository for:

- repeated AST walks;
- repeated exhaustive node/type/effect switches;
- duplicate scope or symbol lookup;
- duplicate type text/relations;
- duplicate call argument shaping and default expansion;
- duplicate receiver adaptation;
- duplicate place/projection decomposition;
- duplicate name mangling and ABI rules;
- repeated semantic effect discovery;
- repeated cleanup logic;
- repeated target/layout decisions;
- repeated diagnostics or near-identical messages;
- repeated compiler pipeline setup in tests and entry points;
- broad result structs whose consumers reach through unrelated fields;
- interfaces with one implementation and no real boundary;
- functions that only forward, rename, or retain ignored parameters.

Measure extension cost using concrete recent features where useful: optional call iteration, structural iteration, match payloads, arrays/slices, interfaces, ownership, or cleanup. Do not infer cost from file count alone.

## Delegation protocol

When user requests a model class or exact model and discovery tooling exists, inspect available models before delegation. Do not invent model IDs or silently substitute an unavailable explicit model unless user allowed fallback. Without user preference, preserve environment's default routing.

Suggested independent delegations:

1. frontend + binding/type semantics;
2. CFG + analyses;
3. HIR/MIR + backend;
4. infrastructure + LSP;
5. CLI/packages/tests;
6. cross-cutting repetition.

Each subagent receives:

- repository path;
- exact scope and exclusions;
- read-only requirement;
- semantic constraints above;
- output format below;
- instruction to report only verified findings;
- instruction to include useful code that should **not** be refactored.

If usage limit stops agent:

1. record label, scope, model, and session ID if available;
2. mark scope incomplete in `architecture-audit.localplan.md` when writes are authorized;
3. otherwise include incomplete scope and resume context in response;
4. preserve any completed report verbatim in handoff notes or dedicated temporary report when writes are authorized;
5. resume same session when possible;
6. otherwise start replacement agent with this master prompt and incomplete scope;
7. never present partial coverage as complete audit.

Main agent must verify subagent findings. Subagent output is evidence lead, not final authority.

## Required finding format

Each finding must contain:

```text
ID: stable short ID
Severity: hard violation | high-value debt | medium debt | low-value cleanup
Confidence: high | medium
Classification: accidental duplication | leaky boundary | shotgun surgery | misplaced responsibility | unhelpful abstraction | missing abstraction | hidden state | test duplication | necessary extension point
Files/symbols: exact paths and names
Current behavior: factual description
Why it matters: maintenance/extension/correctness cost
Evidence: callers, consumers, repeated logic, or data-flow trace
Option A: smallest viable correction
Option B: alternative design
Recommendation: chosen option and reason
Semantic impact: must be none unless separately approved
Validation needed: exact tests/fixtures/contracts
Do not refactor: nearby code that should remain explicit
```

Omit finding when evidence cannot distinguish defect from intentional design. Put uncertainty in investigation notes, not final claims.

## Classification rules

### Necessary extension point

Keep explicit when phase has genuine distinct job, such as:

- parser recognizing syntax;
- typechecker defining semantics;
- CFG representing control topology;
- HIR/MIR lowering representation;
- backend emitting physical operations;
- exhaustive closed-world switches enforcing completeness.

### Accidental duplication

Report when same semantic decision is independently recomputed and can diverge, especially when earlier phase can publish evidence once.

### Leaky boundary

Report when consumer imports or traverses producer internals unrelated to its job rather than using narrow evidence/query contract.

### Unhelpful abstraction

Report wrappers, one-field structs, speculative interfaces, tiny files/modules always edited together, or helpers that hide simple control flow without reducing concepts.

### Missing abstraction

Recommend only when repeated domain logic has at least two real consumers or protects non-obvious invariant. Prefer function/query/result-field over interface unless multiple implementations or ownership boundary exists.

## Final synthesis

Final report must include:

1. **Executive assessment**
   - current architecture health;
   - strongest existing boundaries;
   - top risks;
   - no unsupported score or claim.

2. **Documentation and rulebook assessment**
   - claims confirmed by source and required behavior;
   - stale or incorrect claims;
   - rules that encode a current design rather than durable engineering guidance;
   - recommended moves between rules, specifications, architecture maps, and design proposals.

3. **Current implementation map**
   - source-to-output data flow reconstructed from code;
   - current owner, input, output, identity, and consumers without assuming they should remain;
   - infrastructure and LSP lifecycle.

4. **Verified findings**
   - ordered by severity and leverage;
   - exact file/symbol references;
   - multiple options for major findings;
   - recommended choice.

5. **Repetition inventory**
   - repeated mechanics;
   - repeated semantic decisions;
   - intentional exhaustive handling;
   - test setup repetition.

6. **Coupling and extension-cost map**
   - packages changed for representative features;
   - which edits are legitimate;
   - which indicate shotgun surgery.

7. **Target architecture alternatives**
   - compare multiple viable shapes before choosing;
   - retain only boundaries that earn keep;
   - avoid enterprise DI and speculative plugin systems unless evidence establishes real need.

8. **Ranked remediation plan**
   - Stage 0: correctness or hard-rule violations;
   - Stage 1: safe deletions/direct reuse/local cleanup;
   - Stage 2: evidence and query-boundary improvements;
   - Stage 3: larger representation or phase changes;
   - dependency order, risk, expected payoff, validation, stop condition for each step.

9. **Do not refactor list**
   - explicit switches/walks/boundaries already serving clarity or completeness;
   - reasons to keep them.

10. **Outdated documentation/issues list**
   - remaining `Next()` assumptions;
   - distinguish docs cleanup from compiler architecture work.

11. **Implementation recommendation**
    - first single step worth implementing;
    - why it has best confidence/payoff ratio;
    - exact behavior and invariants to preserve;
    - ask user approval before source edits.

## Search and verification commands

Use repository-aware tools first. Useful terminal checks:

```sh
git status --short --branch
git log -5 --oneline
rg -n 'Next\(|\.Next\b|iterator|iteration' .
rg -n 'Inspect|Walk|Visit|forEachChild|switch .*\.\(type\)' internal cmd pkg
rg -n 'TypeText|ABI|mangle|receiver|EffectiveCallArguments|ValueUse|ReferenceArgument' internal
rg -n 'BuildQueries|Result struct|type .*Result struct|Validate\(' internal
rg -n 'CompileFile|pipeline\.Run|DiscoverSourceFiles|NewWithConfig' cmd internal pkg
```

Scope searches to relevant directories after discovery. Read callers and consumers, not only declarations.

Do not run expensive full test suites during read-only audit unless needed to verify disputed behavior. No validation result should be claimed unless command was run and passed.

## Resume protocol

At session start:

1. read audit inputs without treating them as unquestioned design authority;
2. inspect Git state;
3. read `architecture-audit.localplan.md` when present;
4. list completed reports and missing scopes;
5. state which scope you will inspect;
6. remain read-only.

Before stopping:

1. when writes are authorized, update local plan with completed coverage, verified findings, unresolved questions, and exact next step;
2. when writes are forbidden, include same handoff state in response;
3. record subagent session IDs when available;
4. state files read and commands run;
5. mark partial work partial;
6. leave source tree unchanged.

When all scopes finish, main agent performs independent final review and removes duplicates/false positives from report before presenting it.
