# Compiler Framework Direction

## Why this work was requested

Framework discussion started during `for` loop implementation. Loop itself was not only concern. Work exposed broader development risk: adding one language construct required finding and coordinating many scattered types, functions, maps, switches, and phase-specific assumptions.

A feature could appear complete in parser or typechecker while one downstream phase was silently missed. Missing work might only surface later as:

- incorrect control flow;
- missing ownership cleanup;
- malformed HIR or MIR;
- backend failure;
- editor/LSP crash on incomplete source;
- stale incremental state;
- production-only behavior discovered long after implementation.

Peeper already had one strong framework-like pattern: structural traversal through APIs such as `Inspect` and node-owned child walking. Central traversal reduced repeated recursion and made child handling easier to audit. It did not yet make a newly added child field impossible to omit from manual `forEachChild` code; framework must close that remaining gap through generated traversal or an equivalent mechanical check.

Request was to apply same principle to rest of compiler:

> Make required work mechanically visible. When syntax or semantics changes, compiler should immediately reveal every phase, invariant, artifact, and test that must also change.

Primary goal is **code as executable safety guard**. Documentation explains architecture, but codebase must enforce it. Adding a node should break compilation until participating phases make explicit decisions. Adding a child field should fail generated-code or static consistency checks until traversal includes it. Publishing incomplete ownership/type/CFG evidence should fail at phase boundary before HIR, MIR, backend, or production use.

Goal was not a generic language-generator product. Goal was a clean, logically safe compiler architecture that teaches its own flow through package ownership, function signatures, interface requirements, generated traversal, typed results, and validators. Someone should be able to replace syntax or selected language rules while retaining these safety checks.

## Problems that motivated proposal

### 1. Semantic state was scattered

Related facts could live in broad project-level structures, while producers and consumers were spread across collector, binder, resolver, typechecker, CFG, ownership, HIR, MIR, backend, and LSP code.

This obscured basic questions:

- Which phase owns this fact?
- When is it complete?
- Which later phases may consume it?
- When must incremental compilation discard it?
- Is missing evidence valid recovery behavior or compiler bug?

### 2. Structural traversal was safer than semantic dispatch

`Inspect`/walk functions helped every consumer reach nested nodes. They did not guarantee that every semantic phase had consciously handled a newly introduced node kind.

A new statement could be traversed structurally while resolver, typechecker, CFG, ownership, or lowering silently lacked required semantics.

### 3. Invalid artifacts failed too late

Many cross-phase invariants existed only as assumptions. Bad semantic evidence could survive until HIR, MIR, or backend code, where resulting failure was far from actual producer.

Desired behavior:

```text
producer creates invalid result
        ↓
producer boundary rejects it immediately
```

Not:

```text
producer creates invalid result
        ↓
several phases accept it
        ↓
backend crashes on unrelated-looking operation
```

### 4. Naming and generated artifact construction were mixed with orchestration

Functions such as generated binding/identifier construction looked small enough to resemble wrappers, while module lowering also owned source lowering, hidden loop state, symbol naming, callable mangling, and generated assignments.

Concern was valid: small functions are useful only when they own a real invariant. A helper that merely forwards data is noise. A constructor that consistently couples symbol ID, lowered type, name, and source location protects a real lowering invariant—but it should live in a purposeful subsystem and be named accordingly.

### 5. Loop metadata exposed unclear ownership

Types such as iteration evidence and loop target stacks raised two different questions:

- Is this temporary builder state, semantic evidence, CFG topology, or project-wide state?
- Does its file/package location match its owner?

Proposal was to place each type beside phase that produces and validates it, instead of using broad files such as `project/modules.go` as a general storage location.

### 6. Multiple identities and rediscovery increased drift risk

Compiler concepts could be identified differently by filesystem path, import key, symbol owner, graph node, or mangled name. Multiple representations invite scans, conversion helpers, stale aliases, collisions, and mismatched incremental invalidation.

Framework direction therefore included one canonical typed identity per concept, with string serialization only at boundaries that require strings.

## Proposed framework

Framework is a set of explicit subsystem contracts, not one giant abstraction.

```text
source
  ↓
parser-owned AST
  ↓ validated phase boundary
binding/resolution result
  ↓ validated phase boundary
typechecker result
  ↓ validated phase boundary
CFG + flow result
  ↓ validated phase boundary
ownership result
  ↓ validated phase boundary
HIR
  ↓ validated phase boundary
MIR
  ↓ validated phase boundary
backend IR
```

Each phase should answer:

| Contract | Required answer |
| --- | --- |
| Owner | Which package owns decision and result? |
| Inputs | Which exact earlier artifacts are valid inputs? |
| Output | Which explicit result or artifact is published? |
| Invariants | What is guaranteed after successful completion? |
| Diagnostics | Which invalid source conditions are reported here? |
| Consumers | Which later phases may read result? |
| Invalidation | Which edit/reset discards result? |
| Mutation | Which shared state may phase mutate? |
| Concurrency | Can modules execute phase in parallel? |
| Failure policy | User diagnostic, recoverable invalid artifact, or compiler bug? |
| Verification | Which validator and tests enforce contract? |

## Primary guarantee: codebase guides and rejects incomplete work

Framework succeeds only when compiler source itself leads developer through required integration points.

Desired failure chain for new syntax:

```text
add node kind
  ↓
Go interface satisfaction identifies phases missing node decision
  ↓
 generated traversal/static check identifies unclassified child fields
  ↓
typed phase APIs show required inputs and result owner
  ↓
artifact validator rejects incomplete or inconsistent evidence
  ↓
source/property tests reject logically wrong semantics
```

This distinguishes omissions from semantic mistakes:

| Developer mistake | Earliest intended guard |
| --- | --- |
| New node omitted by resolver/typechecker/ownership/HIR | Go compile error through exhaustive phase interface |
| New child field omitted from structural walk | generated traversal consistency or custom static-analysis failure |
| Node intentionally irrelevant to phase | explicit `ignore` implementation with reviewed reason |
| Required ownership/type evidence not published | phase-result validator failure |
| Evidence points to wrong CFG site, symbol, type, or scope | cross-artifact validator failure |
| Implementation compiles but language semantics are wrong | invariant, property, and source-fixture failure |

Go compiler can prove interface satisfaction and type compatibility. It cannot prove arbitrary ownership semantics or detect a field omitted inside an otherwise valid handwritten method. Framework therefore combines Go interfaces with generated code/static checks and executable validators. Calling all of this “compile-time safety” would be imprecise; target is earliest mechanical failure available for each mistake class.

### Exhaustive phase interfaces

Canonical AST family defines complete phase-facing visitor contract:

```go
type StmtVisitor interface {
	VisitBlock(*BlockStmt)
	VisitIf(*IfStmt)
	VisitWhile(*WhileStmt)
	VisitFor(*ForStmt)
	VisitReturn(*ReturnStmt)
}
```

Every participating phase proves satisfaction:

```go
var _ ast.StmtVisitor = (*ownershipChecker)(nil)
var _ ast.StmtVisitor = (*hirLowerer)(nil)
```

Adding `VisitDefer(*DeferStmt)` then produces direct Go errors in every incomplete phase. No default/no-op visitor implementation may hide missing methods.

Separate interfaces should cover real AST families—statements, expressions, declarations, and type syntax—rather than one universal visitor that forces meaningless methods on every phase.

### Generated child traversal

Interface satisfaction cannot catch this handwritten omission:

```go
type ForStmt struct {
	Iterable Expr
	Body     *BlockStmt
	Else     *BlockStmt // new field
}

func (s *ForStmt) forEachChild(yield func(Node) bool) {
	yield(s.Iterable)
	yield(s.Body)
	// Else forgotten; Go still compiles.
}
```

Preferred framework derives traversal from AST struct definitions:

```go
func (s *ForStmt) forEachChild(yield func(Node) bool) bool {
	return yield(s.Iterable) &&
		yield(s.Body) &&
		yield(s.Else)
}
```

Generator classifies node-compatible fields and lists. Metadata fields such as IDs, tokens, positions, and primitive values are known non-children. Any unknown composite field must require explicit classification instead of silently defaulting to ignored.

Normal repository validation should run generator in check mode:

```bash
go run ./scripts/generate-ast --check
```

Adding or changing AST field then fails until generated traversal and node-kind contracts are current. Equivalent custom `go vet` analyzer is acceptable if it provides same deterministic guarantee with less complexity.

### Ownership completeness accounting

Ownership phase should not merely expose `VisitFor`; it should prove every ownership-relevant typed expression received decision.

Conceptual result:

```go
type ValueOwnership uint8

const (
	OwnershipInvalid ValueOwnership = iota
	OwnershipCopy
	OwnershipMove
	OwnershipBorrow
	OwnershipOwnedTemporary
)

type Result struct {
	Values map[ast.NodeID]ValueOwnership
}
```

Boundary validator walks typed expressions and requires non-invalid ownership classification whenever expression type needs ownership handling:

```go
if typeinfo.RequiresOwnershipDecision(typ) && result.Values[expr.ID()] == OwnershipInvalid {
	return fmt.Errorf("expression %d has no ownership decision", expr.ID())
}
```

Exact representation should follow existing ownership model rather than this conceptual map. Required invariant remains: compiler can mechanically account for every ownership-relevant element, and missing analysis cannot silently reach lowering.

### Types and signatures explain compiler flow

Phase dependencies should be visible from result ownership and APIs. Functions that accept broad mutable `Module` access should be narrowed where practical, without creating decorative parameter structs.

Conceptual ownership boundary:

```go
type ownership.Input struct {
	AST      *ast.Module
	Bindings *bindingresult.Result
	Types    *typecheckresult.Result
	CFG      *cfg.Module
}

func Analyze(input Input) ownershipresult.Result
```

This input type earns its place only if it is actual ownership phase contract. Reading signature should tell developer what ownership consumes, what it publishes, and which earlier phase must change when evidence changes.

## Main workstreams

### 1. Phase-owned semantic results

Every meaningful phase output should have one owner and one storage location.

Examples:

```text
bindingresult.Result
constantresult.Result
typecheckresult.Result
flowresult.Result
ownershipresult.Result
```

These are justified boundaries because they represent real compiler phases or distinct lifetimes. They are not decorative wrappers.

Rules:

- one fact has one producer;
- one fact has one canonical storage location;
- later phases consume published evidence instead of rediscovering it;
- result lifetime matches incremental reset boundary;
- no compatibility maps or forwarding accessors remain after migration;
- result packages contain phase data, not scheduler orchestration.

### 2. Exhaustive node-handling contracts

Structural walking solves recursion. Separate phase contracts should solve omitted semantics.

For every relevant node kind, each participating phase must explicitly choose one:

```text
handle   — phase owns distinct semantics
traverse — canonical child walk is sufficient
ignore   — intentionally irrelevant, with reason
reject   — invalid at this phase boundary
```

Important constraint: no visitor base type with default no-op methods. Defaults would recreate omission bug by silently accepting new nodes.

Preferred implementation starts with compile-time visitor interfaces requiring every node-kind method and compile-time satisfaction assertions for participating phases. Generated node-kind registries and completeness tests may supplement interfaces where Go cannot express closed sets directly.

Child-field completeness is a separate problem. Generate `forEachChild` implementations from AST structs or enforce them with a custom analyzer; do not assume visitor interfaces can detect a forgotten field inside a valid method.

Choose least boilerplate mechanism that makes omissions fail Go compilation, generated-code checks, static analysis, or normal tests—before feature can reach production.

### 3. Canonical artifact validators

Each phase result should have one validator at real boundary.

Validators check artifact shape and published invariants. They do not repeat semantic analysis.

Examples:

- AST recovery invariants;
- symbol/type identity validity;
- required typechecker evidence;
- CFG edge/predecessor symmetry and terminators;
- ownership cleanup-site validity;
- HIR symbol/type/location consistency;
- MIR operand and block validity;
- backend physical type compatibility.

Invalid source remains source diagnostics. Validator failure indicates compiler implementation bug.

### 4. Canonical CFG queries and descriptors

CFG remains owner of control-flow topology. Consumers should not infer loops or structured control flow from incidental block IDs and shapes.

Before adding metadata, inspect consumers and prove existing typed blocks/sites/edges are insufficient. If shared construct metadata is needed, publish validated descriptors once from CFG instead of rediscovering loops in ownership or MIR.

Example conceptual result:

```go
type LoopDescriptor struct {
    Header BlockID
    Body   BlockID
    Latch  BlockID
    Exit   BlockID
}
```

Builder-only state such as active `break` target, `continue` target, and lexical scope depth should remain private CFG construction context. It is not semantic evidence and should not live in project-wide module state.

### 5. Separate mangling from artifact construction

Two different responsibilities must not share misleading names.

**Mangler owns names:**

- callable/linkage names;
- module identity components;
- receiver identity;
- generic/symbol instance suffixes;
- collision-safe framing.

**Phase-local artifact construction owns generated nodes:**

- hidden symbol name and ID;
- lowered type ID;
- source location;
- generated binding/identifier/place/expression shape.

Do not create compiler-wide generic builder. AST, HIR, MIR, and backend artifacts have different invariants and lifetimes. Builder earns its place only inside phase that owns generated representation.

### 6. Validated semantic variants

Semantic plans should make invalid combinations impossible or immediately rejectable.

A tag plus many nullable fields is difficult to audit:

```go
type ForIteration struct {
    Kind    ForIterationKind
    Carrier *Symbol
    Cursor  *Symbol
    End     *Symbol
    Ordinal *Symbol
}
```

Range and sequence iteration do not require same hidden state. Cleaner design gives each variant its required fields and validates it before publication.

```go
type RangeIteration struct {
    Cursor  *Symbol
    End     *Symbol
    Ordinal *Symbol
}

type SequenceIteration struct {
    Carrier *Symbol
    Cursor  *Symbol
}
```

Exact Go representation can vary. Core requirement is stable: downstream phases should not repeatedly reconstruct which nullable combinations are legal.

### 7. Canonical identities

Use one comparable typed identity for module registry, imports, symbol ownership, semantic fingerprints, invalidation, and graph membership.

```go
type ID struct {
    Origin     string
    Namespace  string
    Dependency string
    ImportPath string
}
```

Filesystem path remains secondary lookup because logical module identity should survive relocation. String encoding exists only for string-only boundaries such as generic graph or diagnostic grouping APIs, and must be collision-safe.

No duplicate `Module.Key`, symbol-owner key type, scan-based owner lookup, or compatibility accessor should survive migration.

### 8. Recovered-AST and editor safety contracts

Interactive compiler receives incomplete source continuously. Parser recovery output therefore needs deliberate invariants.

Preferred model:

- parser defines which fields may be absent;
- required recovered fields use explicit missing/synthetic nodes where practical;
- downstream phases consume documented recovered shape;
- LSP/compiler boundary contains ordinary frontend panics;
- failed analysis snapshots are discarded, never published;
- stale document revisions cannot overwrite newer results.

`recover()` is panic containment, not process isolation. It protects server from ordinary compiler panics but not OOM, deadlock, `os.Exit`, or corrupted shared state. Subprocess isolation remains possible future escalation, not present requirement.

### 9. Workflow enforcement

Architecture only helps when normal workflow enforces it.

For every new language construct, contributor should be able to mechanically check:

```text
parser
AST node + child traversal
binding/resolution
base typechecking evidence
constant evaluation, when relevant
CFG
flow typing
definite initialization
ownership
HIR
MIR
backend
LSP/editor recovery
artifact validators
positive and negative source fixtures
```

CI should include:

- phase coverage/completeness tests;
- artifact validator tests;
- source fixtures;
- malformed-source regression tests;
- progressive typing/prefix tests;
- frontend fuzzing with invariant “arbitrary editor source must not panic.”

## Before and after examples

Examples are intentionally short and conceptual. Exact production names may differ.

### Before: broad shared semantic storage

```go
type Module struct {
    ExprTypes   map[NodeID]Type
    CaseTests   map[NodeID]CaseTest
    MatchInfo   map[NodeID]Match
    ConstValues map[SymbolID]Value
}
```

### After: explicit phase ownership

```go
type Module struct {
    Bindings    *bindingresult.Result
    Constants   *constantresult.Result
    Typechecking *typecheckresult.Result
    Flow        *flowresult.Result
    Ownership   ownershipresult.Result
}
```

Result tells reader who produced data and when it is valid.

---

### Before: later phase rediscovers semantic meaning

```go
func lowerCall(call *ast.CallExpr) hir.Expr {
    sym := lookup(call.Callee)
    args := expandDefaults(sym, call.Args)
    return lowerResolvedCall(sym, args)
}
```

### After: consume typechecker evidence

```go
func lowerCall(call *ast.CallExpr) hir.Expr {
    plan := module.Typechecking.Calls[call.ID()]
    return lowerResolvedCall(plan.Symbol, plan.Arguments)
}
```

HIR lowers a decision; it does not repeat typechecking.

---

### Before: new node silently falls through

```go
switch stmt := stmt.(type) {
case *ast.IfStmt:
    checkIf(stmt)
case *ast.WhileStmt:
    checkWhile(stmt)
}
```

### After: phase contract must classify every kind

```go
var statementContract = map[ast.StmtKind]Decision{
    ast.StmtIf:       Handle,
    ast.StmtWhile:    Handle,
    ast.StmtFor:      Handle,
    ast.StmtBad:      Ignore,
}
```

Completeness test compares this table with canonical statement-kind registry. Adding node without decision fails immediately.

---

### Before: nullable evidence requires scattered checks

```go
if plan.Kind == Range && plan.End != nil {
    // lower range
}
if plan.Kind == Sequence && plan.Carrier != nil {
    // lower sequence
}
```

### After: explicit validated variants

```go
switch plan := plan.(type) {
case RangeIteration:
    lowerRange(plan)
case SequenceIteration:
    lowerSequence(plan)
default:
    panic("invalid iteration plan")
}
```

Required state travels with variant that needs it.

---

### Before: multiple module identities and scan lookup

```go
type Module struct {
    Key        string
    ImportPath string
}

for _, module := range ctx.modules {
    if module.DefiningModuleKey() == owner {
        return module
    }
}
```

### After: one typed identity and direct lookup

```go
type Module struct {
    ID       moduleid.ID
    FilePath string
}

module := ctx.modules[symbol.DefiningModule]
```

Logical lookup becomes direct and identity conversion disappears.

---

### Before: generated node helpers look like wrappers

```go
func generatedIdent(ctx *Context, mod *Module, sym *Symbol, loc *Location) *ir.Ident {
    return &ir.Ident{
        Name: symbolName(mod, sym),
        Type: loweredTypeID(ctx, mod, sym.Type),
    }
}
```

### After: phase-local artifact constructor owns invariant

```go
type artifactBuilder struct {
    ctx    *Context
    module *Module
}

func (b artifactBuilder) ident(sym *Symbol, loc *Location) *ir.Ident {
    return &ir.Ident{
        Name:       b.mangle(sym),
        Type:       b.lowerType(sym.Type),
        SymbolID:   sym.ID,
        SourceInfo: ir.SourceInfo{Location: loc},
    }
}
```

This boundary is justified only if several generated artifacts must preserve same name/type/symbol/location invariant. If used once or only forwarding, inline it instead.

---

### Before: invalid result fails downstream

```go
hir := Lower(module)
mir := LowerMIR(hir) // panic here
```

### After: fail at producing boundary

```go
result := typechecker.Check(module)
if err := result.Validate(); err != nil {
    return internalError("typecheck result", err)
}
module.Typechecking = result
```

Failure points at producer that violated contract.

## What “cleaner” means

Framework does not promise fewer named types in every package. Some types are necessary because phases represent genuinely different facts. Cleanliness means fewer ambiguous and duplicated concepts.

Desired reduction:

- fewer broad “miscellaneous semantic info” structs;
- fewer compatibility accessors;
- fewer pass-through wrappers;
- fewer repeated lookups and semantic rediscovery;
- fewer nullable combinations;
- fewer identity conversions;
- fewer files that act as unrelated storage bins;
- fewer production bugs discovered only after downstream failure.

Useful types remain when they make ownership and invariants explicit. Decorative types disappear.

A clean compiler should let contributor answer quickly:

```text
Where is this decision made?
Where is its result stored?
Who may consume it?
How is it validated?
When is it invalidated?
Which test fails if I forget a phase?
```

## Anti-goals and guardrails

Do not turn framework into abstraction tax.

Avoid:

- generic pass manager hiding real scheduler barriers;
- one universal visitor with no-op defaults;
- one universal artifact builder;
- wrappers added only to rename existing calls;
- old and new semantic maps kept together;
- validators that rerun compiler semantics;
- project package becoming dumping ground for phase-owned types;
- backend naming moved into source semantics when physical ABI layout matters;
- subprocess compiler split before actual isolation need exists.

Every new boundary must own at least one real phase, lifetime, invariant, policy, or independently reused operation.

## Expected development experience

Ideal feature workflow:

1. Add syntax node.
2. Canonical child traversal test identifies missing structural registration.
3. Phase contract tests list every semantic phase needing explicit decision.
4. Typechecker publishes validated evidence.
5. CFG, ownership, and lowering consume evidence directly.
6. Artifact validators catch malformed handoff at producer boundary.
7. Source fixtures prove accepted and rejected behavior end to end.
8. Prefix/fuzz tests prove incomplete editor source does not crash frontend.

Result should be compiler that is not only correct today, but difficult to extend incorrectly tomorrow.

## Final objective

Peeper framework goal is executable omission safety through explicit ownership:

> One phase owns each decision. One artifact carries each result. One validator protects each boundary. One canonical identity names each concept. Go interfaces expose missing node handling. Generated traversal exposes missing child fields. Normal tests expose invalid semantics.

Compiler codebase—not contributor memory—should be primary implementation guide. Adding syntax or semantic behavior should cause compiler, generator, analyzer, validators, and fixtures to enumerate unfinished work immediately.

That architecture keeps codebase readable, makes compiler journey safer, and gives future contributors a clear map for extending language without relying on accidental production discovery.
