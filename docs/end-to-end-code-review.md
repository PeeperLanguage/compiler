# End-to-End Code Review

## Scope

Review follows live execution paths from CLI/LSP entry points through dependency handling, compiler scheduling, semantic analysis, HIR/MIR lowering, LLVM emission, and user-facing documentation.

This document preserves original findings and reproduction snippets as baseline evidence. Remediation status below describes current implementation; snippets inside finding sections are historical unless marked otherwise.

## Remediation status

| Finding group | Status | Chosen implementation |
|---|---|---|
| Interface conversion evidence | Resolved | Default substitution deep-clones each occurrence with fresh `NodeID`s and separate declaration/caller provenance maps. Semantic metadata copies from owning module; HIR consumes unique per-occurrence interface evidence. |
| LSP diagnostics | Resolved | Compilation snapshots copy context, component files, generation, and versions under state lock. Publication is serialized, rejects stale generations, and writes without state lock. |
| CLI contract and artifacts | Resolved | One command registry drives dispatch/help/aliases. Compiler APIs are LLVM-only. `check` recursively discovers and groups roots by project. `_gen` publishes completed identity-based staging trees. |
| Dependency lifecycle | Resolved | Lock corruption propagates. `get`/`update` prepare in memory and publish coordinated manifest/lock state only after all work succeeds. Metadata commits before cache deletion. |
| Intrinsic and pipeline ownership | Resolved | One intrinsic definition slice drives compiler-owned free-function symbols, contextual discovery, operations, and signatures. Diagnostic-free scheduler completion requires every scheduled module at backend before runtime-symbol validation. |
| Documentation | Resolved | Interface ownership, target-sized carrier fields, README registry claim, and CLI diagrams now follow live owners. |

This report groups related defects by owning subsystem. Severity reflects impact, not amount of code needed:

- **P1:** valid input can crash, fail silently, corrupt state, or race.
- **P2:** architecture has multiple authorities, misleading abstractions, or silent failure paths that make extension unsafe.

| Group | Highest severity | Main risk |
|---|---:|---|
| CLI and dependency lifecycle | P1 | Incorrect success, ignored input, damaged project state |
| Semantic evidence and intrinsic ownership | P1 | Valid program reaches backend with wrong type evidence |
| Compiler pipeline and backend boundary | P2 | Silent incomplete compilation and fake extensibility |
| LSP state and concurrency | P1 | Data race and stale diagnostics |
| Documentation and architectural truth | P2 | Maintainers implement against contracts that no longer exist |

## 1. CLI and dependency lifecycle

These defects belong together because they define command behavior and mutation of user-owned project state.

### 1.1 `check` and `lint` do not implement their advertised input contract - P1

Help text says these commands typecheck a file or recursively check a folder. Actual command handling defaults to `.` and passes one path to the single-entry compiler path:

```go
path := "."
if len(args) > 0 {
	path = args[0]
}

return compileEntry(path, options)
```

This has two observable failures:

- `peeper check .` attempts to read the directory as a source file and fails.
- `peeper check valid.peep invalid.peep` ignores every path after the first and can return success despite invalid requested input.

Why wrong:

- Command accepts syntax it does not honor.
- Exit status no longer represents all user-requested work.
- Help text and implementation have different owners for the command contract.

What should replace it:

Resolve input shape once, before compilation. Either support all documented forms or reject unsupported forms explicitly.

```go
type CheckTarget struct {
	Files []string
}

func resolveCheckTarget(args []string) (CheckTarget, error) {
	// No args: discover project rooted at current directory.
	// One directory: discover project/source files recursively.
	// One or more files: validate and retain every file.
	// Unsupported mixtures: return a usage error.
}
```

Compiler driver should receive resolved files or a project, not reinterpret raw CLI arguments. One owner must define which files form a check operation.

Acceptance criteria:

- Directory input checks intended project files.
- Multiple explicit files are all checked, or extra arguments are rejected.
- Any failed requested input produces nonzero exit status.
- Help and argument validation derive from same command definition.

### 1.2 Corrupt lockfiles are treated as missing lockfiles - P1

`manifest.LoadLockfile` already distinguishes a missing file from other read or parse failures. `get` discards that distinction:

```go
lock, err := manifest.LoadLockfile(projectDir)
if err != nil {
	lock = manifest.NewLockfile()
}
```

A malformed or unreadable existing lockfile therefore becomes an empty lockfile. Later save can replace evidence needed to reproduce the existing dependency graph.

Why wrong:

- Recovery policy is duplicated outside the lockfile owner.
- Corruption becomes implicit destructive recovery.
- User receives no diagnosis before state changes.

What should replace it:

Trust `LoadLockfile` to own the missing-file case and propagate every remaining error:

```go
lock, err := manifest.LoadLockfile(projectDir)
if err != nil {
	return fmt.Errorf("load dependency lockfile: %w", err)
}
```

If explicit repair is desired, it should be a separately named operation with clear user intent-not normal `get` behavior.

### 1.3 `update` reports success after dependency installation failures - P1

Current loop prints an error and continues:

```go
if err := installUpdate(pkg); err != nil {
	fmt.Fprintf(stderr, "failed to update %s: %v\n", pkg.Name, err)
	continue
}
```

Command can then print an up-to-date message and exit successfully. A partially failed mutation is indistinguishable to scripts from complete success.

Why wrong:

- Process status contradicts emitted diagnostics.
- "No updates available" and "all updates failed" collapse into same result.
- CI and automation cannot safely consume command result.

What should replace it:

Collect failures while allowing independent work to continue, then return one failure result:

```go
var failures []error

for _, pkg := range updates {
	if err := installUpdate(pkg); err != nil {
		failures = append(failures, fmt.Errorf("%s: %w", pkg.Name, err))
		continue
	}
	updated++
}

if len(failures) != 0 {
	return errors.Join(failures...)
}
```

Summary output must separately represent updated, unchanged, and failed counts.

### 1.4 Dependency mutations are not transactional - P1

`get`, `remove`, and `update` modify several related resources:

- manifest
- lockfile
- package cache/install tree

These resources are saved or pruned in different orders. For example, pruning can happen before both metadata files are safely persisted. Individual atomic file writes do not make the whole operation atomic.

Why wrong:

- Failure between writes leaves manifest and lockfile describing different graphs.
- Early cache pruning can remove data still referenced by durable metadata.
- Retry behavior depends on where failure occurred.

What should replace it:

Use one dependency transaction owned by dependency management:

```go
type DependencyPlan struct {
	Manifest manifest.Manifest
	Lockfile manifest.Lockfile
	Installs []ResolvedPackage
	Prunes   []PackagePath
}

func ApplyDependencyPlan(plan DependencyPlan) error {
	// 1. Stage downloads and generated metadata.
	// 2. Validate complete staged graph.
	// 3. Commit manifest and lockfile as one coordinated operation.
	// 4. Prune obsolete cache entries only after durable commit.
}
```

Exact disk mechanism can vary, but invariant cannot: before commit, old state remains usable; after commit, both metadata files describe same dependency graph.

### 1.5 Command metadata has multiple authorities - P2

Command names, aliases, handlers, help, and backend policy are spread across separate switches and text blocks. Adding one command requires synchronized edits in several files. Alias and help drift already demonstrate this is not theoretical.

What should replace it:

One declarative registry should own command identity and dispatch metadata:

```go
type CommandDefinition struct {
	Name        string
	Aliases     []string
	Usage       string
	Run         func(CommandContext, []string) error
	BackendRule BackendRule
}

var commands = []CommandDefinition{
	{Name: "check", Aliases: []string{"lint"}, Usage: "check [path...]", Run: runCheck},
	{Name: "build", Usage: "build [path]", Run: runBuild, BackendRule: LLVMOnly},
}
```

Lookup, help generation, alias handling, and validation should derive from this registry. Do not add another wrapper around existing switches; remove split authorities when migrating.

### 1.6 Generated IR artifacts can collide or remain stale - P2

`-keep-gen` uses source basename for generated files. Different modules named `main.peep` can overwrite the same `main.hir`, `main.mir`, or `main.ll`. Old artifacts can also survive and appear current.

What should replace it:

- Derive artifact path from canonical module/import identity, not basename alone.
- Write into a clean per-build staging directory.
- Publish completed artifacts only after successful generation.

```text
build/generated/
  app/main.hir
  app/main.mir
  app/main.ll
  deps/example/main.hir
  deps/example/main.mir
  deps/example/main.ll
```

## 2. Semantic evidence and intrinsic ownership

These issues concern semantic facts that later compiler phases trust. Such facts need one owner and identity matching the operation they describe.

### 2.1 Interface-conversion evidence is keyed by expression, not conversion occurrence - P1

Typechecker stores interface implementation evidence using source expression `NodeID`:

```go
tc.project.InterfaceImplementations[expr.ID()] = implementations
```

Default-argument substitution can reuse one caller expression for multiple parameters. If two expanded defaults convert that expression to different interfaces, both conversions share the same `NodeID`. Second write overwrites first conversion's slot set.

HIR later asks for evidence by expression ID:

```go
implementations := l.project.InterfaceImplementations[expr.ID()]
```

By then target interface context is no longer sufficient to recover overwritten proof. Valid source can reach typed LLVM with wrong aggregate layout and panic, for example:

```text
call @use argument 1 is i32, want { i8*, i8* }
```

Why wrong:

- Evidence describes a conversion, but key identifies only reused input expression.
- Cache assumes one expression has at most one interface target.
- Later phase receives mutable last-writer state instead of exact semantic result.
- Backend panic is only symptom; ownership error begins in semantic handoff.

What should replace it:

Best model: make conversion explicit in typed semantic output so evidence travels with its occurrence:

```go
type InterfaceConversion struct {
	Expr            ast.Expr
	Target          typeinfo.Type
	Implementations []project.InterfaceImplementation
}
```

If current IR cannot represent explicit conversion nodes yet, use a conversion-site key that cannot collide across expanded arguments:

```go
type ConversionSite struct {
	ExpressionID ast.NodeID
	CallID       ast.NodeID
	Parameter    int
}

type InterfaceEvidence map[ConversionSite][]project.InterfaceImplementation
```

Key requirement: evidence identity must include conversion occurrence and target context. Adding target type alone may still be insufficient if same expression converts to same interface at separate sites with distinct ownership/lifetime facts later.

Required regression fixture:

```peeper
interface ReadA {
    read_a(self: &Self) -> i32
}

interface ReadB {
    read_b(self: &Self) -> i32
}

fn use[T](x: T, a: &ReadA = x, b: &ReadB = x) {
    // Both defaults reuse x but require distinct interface evidence.
}

use(&c)
```

Fixture must compile and execute through bundled `build/bin/peeper`, not stop at Go unit coverage.

### 2.2 Predeclared intrinsic registry still has two authorities - P2

One list says which operations are predeclared:

```go
var predeclaredOperations = []symbols.CompilerOp{
	// operation identities
}
```

A separate switch says which signatures those operations have:

```go
switch op {
case symbols.SomeOperation:
	return someSignature(target)
// ...
default:
	panic("missing intrinsic signature")
}
```

Adding an operation requires editing both. Missing one edit either omits symbol exposure or panics during predeclared-scope initialization.

Why wrong:

- Registry is unified by API name, not by data ownership.
- Startup correctness depends on synchronized declarations.
- Extension failure occurs far from operation declaration.

What should replace it:

One definition must contain identity, exposure, and signature factory:

```go
type intrinsicDefinition struct {
	Operation   symbols.CompilerOp
	Name        string
	Predeclared bool
	Receiver    receiverShape
	Signature   func(target.Info) *typeinfo.FuncType
}

var intrinsicDefinitions = []intrinsicDefinition{
	{
		Operation:   symbols.SomeOperation,
		Name:        "some_operation",
		Predeclared: true,
		Signature:   someOperationSignature,
	},
}
```

`PredeclaredSymbols`, symbol lookup, and operation enumeration should derive from this same collection. A consistency test is useful defense, but it does not replace removing duplicate authority.

## 3. Compiler pipeline and backend boundary

These problems concern phase scheduling and ownership after semantic analysis.

### 3.1 Pipeline scheduler can stop without reporting incomplete modules - P2

Scheduler breaks when no module is ready or no phase advances. It can then return without proving every requested module reached backend completion.

Conceptually, current control flow is:

```go
for {
	ready := findReadyModules()
	if len(ready) == 0 {
		break
	}

	advanced := advance(ready)
	if !advanced {
		break
	}
}

return nil
```

Later build code observes missing/empty LLVM IR, so diagnosis appears after scheduler lost information about blocked phase and prerequisite.

Why wrong:

- Loop termination is mistaken for successful compilation.
- Root cause is replaced by downstream empty-artifact failure.
- Adding a phase or dependency edge can silently create an unreported stall.

What should replace it:

Scheduler must establish terminal invariant before returning success:

```go
for _, module := range orderedModules {
	if module.Phase != project.PhaseBackend {
		return fmt.Errorf(
			"pipeline stalled: module %s stopped at %s",
			module.Path,
			module.Phase,
		)
	}
}
```

Production diagnostic should include unmet prerequisite or blocking diagnostic when available. Generic invariant error is fallback, not preferred user message.

### 3.2 Backend abstraction is declared but not owned at one boundary - P2

Project state defines LLVM and WASM backend identities, but pipeline imports LLVM directly, module state stores `LLVMIR`, CLI rejects non-LLVM backends, and build consumes LLVM-specific output.

Current effective flow:

```text
CLI backend flag
    -> project.TargetBackend
    -> pipeline ignores abstraction
    -> LLVM generator
    -> module.LLVMIR
    -> clang
```

Adding a backend requires edits across CLI, project core, scheduler, module representation, artifact handling, and build toolchain. `TargetBackend` therefore suggests an extension point that does not exist.

Two valid directions exist:

1. **Be honestly LLVM-only now.** Remove unsupported backend identities and generic configuration until a second backend is implemented.
2. **Create a real post-MIR backend boundary.** Pipeline selects one backend owner and stores a backend-neutral artifact.

```go
type Backend interface {
	Name() backend.Type
	Emit(*mir.Module, target.Info, *diagnostics.DiagnosticBag) (Artifact, error)
}

type Artifact struct {
	Kind backend.Type
	Text string
}
```

Do not add this interface merely to wrap `GenerateLLVMIR`. It is justified only when pipeline dispatch and artifact ownership actually move behind it. Until then, deleting false generality is simpler and more maintainable.

### Desired phase ownership

```text
Parser
  produces syntax
      |
Typechecker
  produces types + conversion evidence
      |
HIR lowering
  consumes semantic evidence; does not rediscover it
      |
MIR lowering
  owns target-independent executable representation
      |
Backend boundary
  owns target/backend-specific representation and emission
      |
CLI build
  owns external tool invocation and final artifact placement
```

Each arrow is a typed contract. Later phases should not recover facts that an earlier phase already knew, and earlier phases should not store backend-specific output.

## 4. LSP state and concurrency

### 4.1 Diagnostic workers read workspace state outside its lock - P1

Each changed file can schedule a diagnostic goroutine. Recompile locks shared state only while compilation runs. After unlock, worker computes component files from `state.workspace`. Another worker can rebuild or replace that workspace concurrently.

Simplified race:

```text
worker A: lock -> compile A -> unlock
worker B: lock -> rebuild workspace -> unlock
worker A: read workspace component files without lock
```

Consequences:

- Go data race on workspace state.
- Diagnostics from one compiler context can be published using file membership from another workspace generation.
- Older worker can overwrite newer diagnostics.

What should replace it:

Create immutable publication snapshot while holding state lock. Release lock before writing protocol output:

```go
type diagnosticSnapshot struct {
	Context *project.CompilerContext
	Files   []string
	Version uint64
}

func (s *ServerState) compileDiagnosticSnapshot(path string) (diagnosticSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, err := s.recompileLocked(path)
	if err != nil {
		return diagnosticSnapshot{}, err
	}

	return diagnosticSnapshot{
		Context: ctx,
		Files:   append([]string(nil), s.workspace.componentFiles(path)...),
		Version: s.version,
	}, nil
}
```

Before publishing, compare snapshot version with latest requested version for each document. This prevents slow old workers from replacing current diagnostics.

Do not hold state mutex while sending LSP messages. Output can block and should not serialize compilation state.

Acceptance criteria:

- Workspace reads and writes follow one locking policy.
- Publication operates on copied immutable data.
- Stale worker cannot publish over newer document version.
- Race-enabled tests pass in a Go installation with working race runtime.

## 5. Documentation and architectural truth

Documentation defects are grouped because each describes a contract maintainers may use when extending compiler.

### 5.1 Interface layout documentation is stale - P2

Ownership documentation says all interface carriers use `{rawptr, vtable}`. Backend layout for owned interfaces includes allocator/provenance state and is effectively `{data, dispatch, allocator}`.

Why wrong:

- ABI work based on two-field model can omit ownership metadata.
- Calls, returns, drops, comparisons, and conversions may use incompatible layouts.

Correct documentation should distinguish borrowed and owned carriers explicitly:

```text
borrowed interface: { data pointer, dispatch table }
owned interface:    { data pointer, dispatch table, allocator/provenance }
```

Field names and exact LLVM types should be copied from canonical type-lowering owner, not maintained as an independent guessed ABI.

### 5.2 Length/index documentation hardcodes `i64` - P2

Allocator documentation describes dynamic array and string lengths as `i64`. Live lowering uses target `IndexType`, so width follows target pointer/index width.

Correct contract:

```text
length, capacity, and index fields use target usize/IndexType
```

This matters on 32-bit targets. ABI documentation must state target-sized intent and validation must cover both 32-bit and 64-bit layouts.

### 5.3 Intrinsic registry documentation overstates unification - P2

README describes a unified intrinsic registry, while predeclared membership and signature construction remain separate declarations. Documentation should not claim architectural work is complete until all consumers derive from one definition.

### 5.4 CLI flow diagrams reference removed functions - P2

CLI diagrams still reference old parser and test-command functions. This makes code-flow navigation actively misleading.

What should replace current maintenance model:

- Update diagrams in same change that moves command ownership.
- Prefer function/module names verified from live source.
- Add a lightweight documentation check for referenced paths or symbols where practical.
- Mark conceptual nodes as conceptual, so they are not mistaken for real functions.

## Implemented order

1. Fixed default-expansion identity and semantic evidence ownership.
2. Made LSP diagnostic compilation and publication snapshot-safe.
3. Unified command contract, removed unused backend abstraction, fixed recursive checks, and staged generated artifacts.
4. Made dependency metadata returned-error atomic and cache cleanup post-commit.
5. Unified intrinsic ownership and enforced scheduler terminal state.
6. Aligned documentation with live compiler owners.

## Validation evidence

Remediation used live source and executable behavior, not diagrams as authority.

- `go test -count=1 ./...` passed.
- `go vet ./...` passed.
- `go test -race -count=1 ./internal/lsp` passed.
- `bash scripts/build.sh` rebuilt bundled `build/bin/peeper` successfully.
- Bundled `check` and `run` passed for `x_test/default_interface_evidence`; process exited zero.
- Bundled checks rejected `x_test/negative_default_parameter_effect` and `x_test/negative_default_parameter_move`.
- `TestPipelineLowersEveryRegisteredIntrinsic` passed for 32-bit and 64-bit targets, including clang assembly.
- CLI subprocess tests cover recursive/multiple paths, mixed valid/invalid exit status, aliases/help, and same-basename generated artifacts.
- Dependency tests cover malformed locks, multi-package rollback, coordinated publish rollback, registry-scan failure, graph-only pruning, and post-commit cache cleanup retry.
- D2 renderer was unavailable in validation environment; diagram symbols were checked against live source, but rendered output was not verified.

## Review boundary

Remediation implements all validated findings without commit, push, PR, or GitHub tracking changes. Suggested snippets in original finding sections remain historical design sketches, not current drop-in code. Coordinated dependency persistence guarantees restoration for returned publish errors; it does not claim journaled power-loss recovery or cross-process locking.
