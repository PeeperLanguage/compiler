# Effect stream migration

Status: **milestone 1 complete**. Definite initialization consumes published effects
and no longer imports `ast`. Ownership is the next consumer and is not yet planned.

This document is the executable plan for publishing semantic effects once and migrating
dataflow consumers onto them. It is tracked so that anyone — human or agent — picking the
work up cold has the whole context in the repository.

Read [`change-paths.md`](change-paths.md) first for how a change travels through the
compiler today, and [`README.md`](README.md) for the framework workstreams this extends.

## Why

Adding a language feature should need parser, scope, and type rules, and nothing else.
Today every later phase re-derives the same meaning from AST shape. `change-paths.md`
records nine statement dispatch sites and four expression sites for one new node kind.

Four of the nine were pure mechanism — they rediscovered read/write meaning the
typechecker already decided:

```
ownership.applyStmt · ownership.symbolUseSequence
definiteinit.checkReads · typechecker.applyConditionEdge
```

`definiteinit.checkReads` is gone; the producer took its place in the contract. The other
three remain.

Two concrete failures this causes today:

- **Silent omission.** `internal/semantics/definiteinit/initialization.go` switches over AST
  kinds in three places and none has a `default:`. A new statement kind is dropped without
  error or panic; the result is a false "used before initialized" or a missed one, depending
  on which switch missed it. Contrast `cfg.buildStmt`, which panics on an unhandled
  statement. The producer is total; the consumers silently are not.
- **Two walks that must agree, and don't.** `ownership.checkExpr` and
  `ownership.symbolUseSequence` both enumerate value uses. `applyStmt` handles
  `ForStmt.Iterable`; `symbolUseSequence` never visits it. Liveness and borrow-ending see a
  different program than the effect analysis does.

## The shape

One producer translates AST into ordered semantic effects, keyed by CFG site. Consumers read
effects, never syntax. A construct that maps onto existing effects needs no case in any
consumer.

This shares **evidence**, not a solver. `COMPILER_GUIDELINES.md` §6 forbids extracting a
generic dataflow framework from similar-looking worklists. Each analysis keeps its own
lattice, join, direction, and diagnostics. Only the facts are shared.

### Package

`internal/semantics/effect/` — `model.go`, `build.go`, `validate.go`, plus tests.

Model and builder live together, following `internal/ir/cfg`, not the
`typechecker`→`typecheckresult` split, because this producer is a normalization pass rather
than a phase with its own analysis.

### Vocabulary

Three operations. `Define` brings a binding into existence and records whether it is also
initialized; `Write` stores to a binding that already exists; `Use` reads one. Each carries
its `*symbols.Symbol` and the `ast.NodeID` it came from. `Use` also carries a
`*source.Location`, so a consumer reports against a read without resolving the node back to
syntax. `Define` and `Write` carry none, because no current diagnostic anchors on them.

`Op` is sealed by an unexported marker method, the same idiom as `cfg.Terminator` and
`typecheckresult.IterationPlan`. Go cannot make a consumer's type switch exhaustive, so
`internal/contracts` carries that half.

### Deliberately absent

Adding a channel before a consumer needs it is what commit `7ec06e9` had to delete. Each of
these has a recorded trigger instead:

| Absent | Add when |
| --- | --- |
| `Borrow` | ownership migrates and needs shared/mutable borrow distinct from read |
| `Discard` | ownership migrates and needs the `DiscardedValue` cleanup channel |
| `Use.Kind` (read/copy/move) | ownership migrates **and** `typecheckresult.ValueUses` covers more than call arguments. Today it covers only those, so a `Kind` field now would carry false data for roughly twenty constructs |
| `Place` with projections | field- or index-level initialization tracking is wanted. Definite initialization tracks whole symbols only |
| `Region` (deferred/repeated body) | a construct exists whose body does not execute at its syntactic position — a lambda. CFG back-edges already give "runs 0..N times" for loops, so a loop does not justify it |

### Result and phase

`Result` is `map[ir.NodeID]SiteOps` and `SiteOps` is `map[cfg.SiteID][]Op` — function
identity outer, site inner, following `flowresult.Result.SiteFacts`. A `cfg.SiteID` is
`{Block, Index}` and is only meaningful relative to one graph, so the outer key is
required. It is a bare map type rather than a struct with one field, matching
`ownershipresult.Result`, because `RULES.md` §1 forbids the single-field wrapper.

New phase `Effects`, placed after `FlowTyped` and before `DefiniteInit`:

```
CFG → FlowTyped → Effects → DefiniteInit → Ownership → Usage
```

After `FlowTyped` so the producer may use `module.EffectiveExprType`, as ownership already
does. Any reset clearing `CFG` also clears everything later, so no stale `SiteID` can
outlive its graph.

## Milestone 1 — pilot on definite initialization

Ownership is roughly 1900 lines with loans, NLL borrow-ending, and fifteen diagnostics.
The vocabulary is proved against one small consumer first.

`usage` is **not** part of this and needs no migration: it has no AST switch and no state,
it scans `sym.Used` flags.

Each step ends with its gate green and one commit. Do not start a step before the previous
gate passes. Prefix every command with
`GOCACHE=/tmp/peeper-maintainability-go-cache CCACHE_DISABLE=1`.

### Step 1 — vocabulary and artifact slot — **done**

`effect/model.go` with `Op`, `Define`, `Write`, `Use`, `Result`, `SiteOps` and `At`. Add
`phase.Effects` with its `String` case; add `Module.Effects` with an ownership comment; add
the `resetToPhase` clause in phase order.

The phase transitions deliberately land in step 2, not here: without a production block a
module advances through `Effects` while actually running definite init, and its diagnostics
get tagged with the wrong phase.

Gate: `go build ./...`, `go vet ./...`, tests for `internal/project`, `internal/pipeline`,
`internal/phase`.
Commit: `Add semantic effect artifact and phase slot`

### Step 2 — producer — **done**

`effect/build.go` with `Build(graphs, nodes, BuildQueries)`. Passing a `*project.Module`
would make `project` and this package import each other; `cfg.BuildQueries` establishes the
alternative, so the producer declares the narrow accessors it needs and the owning artifacts
supply matching methods. `typecheckresult.ArmBindings` was added for that reason, joining
`MatchCases` and `ForLoopGuaranteedEntry`.

Emit in evaluation order: parameters as initialized defines at the entry site; `LetDecl` and
`ConstDecl` as reads of the value then a define; `AssignStmt` as reads of the value then a
write for an ident target, or reads of both for a projection target; `ExprStmt` and
`ReturnStmt` as reads; a `*cfg.Branch` terminator site as reads of its condition; match arm
bindings as initialized defines at the arm body's entry site.

**References** resolve through `Bindings.NodeSymbols`. **Definitions do not**: the
resolver indexes references only, so a declaration name and an assignment target are absent
from that map and resolve through the site's scope, exactly as definite initialization did.
Reproducing that split is what keeps the step free of behavior change. Unifying it needs
separate approval, because scope lookup by name walks parents and can bind a shadowed
symbol.

Intercept `*ast.CallExpr` and walk `Typechecking.CallArgumentsOrSource(call)` so
default-expanded arguments are covered.

Effects a site inherits on entry — parameters, match payload bindings — are published in a
leading pass, because a site's operations are read in evaluation order and an arm body may
read the payload it binds.

The producer is now the single place that inspects syntax for this meaning, so it takes
`cfg.buildStmt`'s policy: `default: panic`.

Gate: full `go test -count=1 ./...`. Nothing consumes the artifact yet, so nothing may change.
Commit: `Publish semantic effects for every CFG site`

### Step 3 — migrate definite initialization — **done**

`Check(graphs *cfg.Module, effects effect.Result, diag *diagnostics.DiagnosticBag)`.
Delete the three AST switches.

Preserve exactly, all pinned by existing tests: the set lattice with **intersection** join
(a must-analysis); optimistic first arrival, intersect afterwards, requeue only on change;
the FIFO worklist; the report loop iterating site order rather than worklist order;
reachability by both `block.Reachable` and non-arrival; `tracked` as the diagnosable
universe; and the single `T0039` diagnostic verbatim, anchored at the reading identifier,
with no deduplication.

One representation change: effects are replayed in evaluation order within a site rather
than every read being checked against the state before the whole site. That is what lets
parameters and match bindings arrive as ordinary defines instead of being seeded
separately. It agrees with the old behavior everywhere, because reads are always published
before the define or write they precede. The visible consequence is that a match binding
lands in a site's `Out` rather than its `In`.

Gate: focused tests, then full suite, then `go run ./scripts/bundle.go` and the `x_test`
fixtures. **Zero diagnostic changes.**
Commit: `Consume published effects in definite initialization`

### Step 4 — contract and validator — **done**

Add the producer to `statementSites` in `internal/contracts/node_dispatch_test.go` with
`inertDeclarations: true`; remove the `checkReads` entry, whose function no longer exists.
Add `effect/validate.go` following `cfg/validate.go` literally — accumulate, sort, truncate
at ten — and state in its doc comment what it does not re-derive.

Mutation-prove both: delete a producer case and confirm the named contract failure; corrupt
an op and confirm the validator message. Restore both, and record the outputs in the commit
body. Update the counts in `change-paths.md`.

Gate: full suite plus race on `internal/project`, `internal/pipeline`, `internal/lsp`.
Commit: `Require a phase decision for every published effect`

## Behavior changes found, not fixed here

`RULES.md` §10 forbids mixing a behavior change into a refactor. Both of these are recorded
for separate approval and must **not** be corrected inside this migration.

- **Match subjects are never read-checked.** `definiteinit` attaches a site condition only
  for `*cfg.Branch`; `*cfg.SwitchVariant` is not handled, so a match on an uninitialized
  value is not diagnosed. The effect stream makes emitting that read natural, which would
  start rejecting code that compiles today. Step 2 must reproduce the gap. Closing it needs
  its own approval and an `x_test` fixture.
- **`ForStmt.Iterable` is invisible to `symbolUseSequence`** while `applyStmt` handles it.
  Ownership only; out of scope.

## Success test — met

`definiteinit/initialization.go` contains no `switch` over AST types and does not import
`ast` at all. Diagnostics are unchanged: the full suite, race on project, pipeline and
lsp, the bundle, and the `x_test` fixtures all pass without modification.

What a contributor adding a statement kind now sees, in order:

1. `cfg.buildStmt` panics if the kind reaches CFG construction unplaced.
2. `contracts.TestEveryStatementKindHasAPhaseDecision` fails with
   `publishStmt makes no decision about ast.YourStmt`.
3. `effect.Result.Validate` raises `ICE0002` if what is published is malformed.

Definite initialization needs no change at any point.

## Picking this up cold

1. `git status --short --branch`, and record HEAD.
2. `git log --oneline -5`. Steps commit in order with the subjects above, so the last
   subject tells you which step finished.
3. Read `AGENTS.md`, `RULES.md`, and `go-style.md` before editing. Re-run the `AGENTS.md` §2
   pre-patch gate before every patch and the §7 post-patch audit before every stop.
4. Re-verify this document against live source. It was accurate when written; line numbers
   and counts rot.
5. Never start a step before the current gate is green.
6. Never push, merge, open a pull request, or rewrite history without explicit approval.
7. If a gate fails for a reason this document does not predict, stop and report it. Do not
   improvise around a failing gate.

## After milestone 1

Ownership is the next consumer. It needs `Borrow`, `Discard`, and `Use.Kind`; `Use.Kind`
first needs `ValueUses` extended past call arguments, with a matching extension to
`ownershipresult.validateValueUses`, which today enforces only the call-argument case.
`UseCopy` is never published, so the two `UseCopy` diagnostics in `ownership/expr.go` are
presently dead and would activate for the first time — they need tests before that.
Ownership's loans, liveness, and borrow-ending stay local to ownership.
