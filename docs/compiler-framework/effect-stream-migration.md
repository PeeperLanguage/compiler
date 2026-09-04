# Effect stream migration

Status: **in progress**. Definite initialization is fully migrated. Ownership consumes
published effects for use enumeration, liveness definitions and discarded values; its
expression walk still reads syntax, blocked on two facts nobody publishes yet.

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

It started at three operations and grew to seven, each time because a consumer needed the
distinction. `Define` brings a binding into existence; `Write` stores to one that already
exists; `Use` reads a place; `Borrow` takes a reference to one; `Discard` throws a value
away; `CallBegin` and `CallEnd` bracket a call. The section below records what each
addition bought.

Every operation carries the `ast.NodeID` it came from. `Use`, `Borrow` and `Discard` also
carry a `*source.Location`, so a consumer reports against them without resolving the node
back to syntax. `Define` and `Write` carry none, because no current diagnostic anchors on
them.

`Op` is sealed by an unexported marker method, the same idiom as `cfg.Terminator` and
`typecheckresult.IterationPlan`. Go cannot make a consumer's type switch exhaustive, so
`internal/contracts` carries that half.

### What the vocabulary grew, and why

Adding a channel before a consumer needs it is what commit `7ec06e9` had to delete, so
each of these was held back until a consumer actually asked. All were added by migrating
ownership, and each arrived with the consumer that needed it in the same change:

| Added | Because |
| --- | --- |
| `Use.Kind` | ownership decided read/copy/move at forty-four hardcoded literals. The kind now comes from the position, and for a call argument from the typechecker's published decision |
| `Borrow` | a reference is not a read, and shared versus mutable is what decides whether a second borrow conflicts |
| `Discard` | a value produced and thrown away dies where it is produced, when nothing owns it |
| `Place` with projections | moving out of `pair.left` is a different decision from moving `pair`, with its own diagnostic |
| `Place.Temporary` | ownership keys two policies on a value that lives in no binding, which a binding-rooted place could not name |
| `CallBegin` / `CallEnd` | a call is a lifetime: argument temporaries die when it completes, receiver reservations activate when it starts |
| `Define.OnEntry` | a parameter and a match payload binding exist before their site runs, and liveness must not treat that as a definition within the site |

Still absent, with its trigger recorded:

| Absent | Add when |
| --- | --- |
| `Region` (deferred or repeated body) | a construct exists whose body does not execute at its syntactic position — a lambda. CFG back-edges already give "runs 0..N times" for a loop, so a loop does not justify it |

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

- **Match subjects were never read-checked — now closed.** `definiteinit` attached a site
  condition only for `*cfg.Branch`, so a match on an uninitialized value was not
  diagnosed. Ownership's liveness *did* read the subject, so migrating it onto the shared
  producer forced the subject to be published, and publishing it closed the
  definite-initialization gap at the same time. Covered by
  `TestInitializationChecksMatchSubject`. No existing test, fixture or bundled program
  changed, so nothing that compiled before fails now.

  This is the coupling a shared stream creates: evidence published for one consumer
  strengthens every other consumer, whether or not that was the intent. Weigh it before
  publishing anything new.
- **`ForStmt.Iterable` disagreement — now moot for enumeration.** `symbolUseSequence`
  never visited it while `applyStmt` did. Both now read the same published stream, so
  they cannot disagree. The iterable's reads are still not published, which preserves
  today's behavior; `applyStmt` continues to handle it directly for the sequence-carrier
  loan.

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

## Vocabulary as it stands

```
Define{Place-less: Symbol, Node, Initialized, OnEntry}
Write{Symbol, Node}
Use{Place, Node, Location, Kind}
Borrow{Node, Location, Mutable}
Discard{Place, Node, Location}
CallBegin{Node, Location} / CallEnd{Node}

Place{Root *symbols.Symbol | Temporary ast.NodeID, Projections []place.OriginProjection}
```

Exactly one of `Place.Root` and `Place.Temporary` is set; the validator enforces it, and
checks that call brackets balance and do not cross.

## Four boundaries, found by attempting the work

Each was discovered by trying to migrate a consumer and watching a real test fail, not by
reading. Three are closed.

1. **Bindings established by an edge, not a site — closed.** A parameter exists before the
   entry site runs; a match payload binding is created by its case edge. Definite
   initialization never noticed, because it replays a site's operations in order.
   Liveness treats a site as a set, so counting those as definitions killed a borrow one
   site early. `Define.OnEntry` records the difference.
2. **A call is a lifetime, not a position — closed.** Temporaries created while computing
   an argument live until the call completes; a reservation for a receiver activates when
   the call starts. `CallBegin`/`CallEnd` bracket it, and nest.
3. **Temporaries are places too — closed.** A value that lives in no binding could not be
   named, but ownership keys two policies on exactly that case. `Place.Temporary` names
   the producing expression.
4. **Two facts nobody publishes — open.** See below.

## What blocks the expression walk

`ownership.checkExpr` cannot yet be replaced, because two decisions it makes are not
recoverable from the stream:

- **Reference-parameter position.** `Read(reference)` where the parameter is `&i32` takes
  a shared-borrow storage access, not a plain read, *because of the parameter type*. The
  stream publishes `Use{Kind: UseRead}`, which is also what an implicit-copy argument
  publishes, so the two cannot be told apart. Note this is not an implicit borrow: Peeper
  rejects `Read(value)` with `cannot implicitly convert i32 to &i32`. Implicit adaptation
  happens only for a method receiver or a piped first argument, and the typechecker
  already records that in `ImplicitCallArguments` — evidence that is published and unread.
- **Slicing.** `a[0..2]` forces a shared or mutable borrow access, decided by the index
  being a `RangeExpr`. A `Place` with an `OriginIndex` projection does not say whether the
  index was a range.

Closing either means publishing one more fact from the typechecker, which already knows
both. Until then, migrating `checkExpr` would leave ownership deriving some decisions from
the stream and some from syntax, which is the double-derivation this work exists to remove.

## Migrated so far

| Consumer | State |
| --- | --- |
| definite initialization | fully migrated; no AST switch, does not import `ast` |
| ownership use enumeration | `symbolUseSequence` reads the stream |
| ownership liveness definitions | `symbolUsesAndDefinitions` reads the stream |
| ownership discarded values | reads `Discard.Place`; `IsPlaceExpr` gone from `ownership.go` |
| ownership expression walk | **not migrated** — 20 AST cases, blocked above |
| ownership statement policy | 10 AST cases, and most is policy rather than enumeration |
| usage | needs no migration; it has no AST switch and no state |

## Behavior this surfaced

Publishing evidence for one consumer strengthens every other consumer. Two real gaps
closed as a side effect, each covered by a test and neither breaking an existing fixture:

- a `match` on an uninitialized subject was never diagnosed;
- a loop over an uninitialized bound or sequence was never diagnosed.

Weigh that before publishing anything new: it is a feature, but it is also a behavior
change that arrives without being asked for.

## Bugs introduced by this work, and fixed

Recorded because each was found by probing the real stream rather than by review, and the
same class will recur:

- match arm bindings were emitted while walking the match terminator, so they could land
  after the arm body's own operations at the same site;
- counting edge-established bindings as definitions ended a borrow one site early;
- a method callee was published as a field projection of the receiver, naming a field that
  does not exist.

## Picking up the expression walk

1. Publish reference-parameter position and slicing, from the typechecker, which knows both.
2. Port `checkIdent`, the partial-move diagnostics, storage-access classification and loan
   bookkeeping onto `applyUse` / `applyBorrow`, driven by the site's operations.
3. Delete `checkExpr`. Ownership keeps its statement policy; that is policy, not enumeration.

Do not start step 2 before step 1. The suite is a strong net — it caught every mistake
listed above — but a half-ported loan analysis is the worst state to hand over.
