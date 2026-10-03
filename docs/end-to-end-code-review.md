# Compiler Review — Completed Work and Remaining Tasks

## Review boundary

This document is a fresh-start work list built from the earlier whole-codebase
review and `final-code-review.md`. It is not a new whole-codebase audit. Each
finding below was checked against the current source before being listed as
open, resolved, or documentation-only.

Current checkout used for this update:

- branch: `main`
- HEAD: `ee3be84` (`Merge pull request #148 from PeeperLanguage/fix/shared-reference-projections`)
- worktree: clean
- production source/fixture changes, resets, commits, pushes, and GitHub
  tracking: none

The earlier note about uncommitted candidates in
`internal/ir/mir/expr_lower.go` and `internal/backend/llvm/instruction_emit.go`
and the associated rvalue/shift tests and fixtures does not reproduce in this
checkout. No files were removed to establish this baseline.

Historical runtime results remain historical evidence. This update checked
current source, tests, fixtures, workflows, and documentation paths; it did
not claim to rerun the historical end-to-end reproductions.

## Completed and revalidated work

- [x] **Shared-reference and owner mutability:** committed in `1ae82933`.
  Owning pointers do not independently grant mutation; immutable owners and
  enclosing shared references cannot grant write access. Mutable owners and
  `&mut` remain valid. Moving, freeing, and dropping an immutable owner remain
  consumption operations and do not require `mut`.
- [x] **Nested/deferred semantic cases:** `1ae82933` covers optional reference
  permissions, qualified constant mutable-borrow rejection, deferred
  enum-field assignment compatibility, and durable late conversions consumed
  by lowering. Go regressions and Peeper fixtures are included in that work.
- [x] **Artifact and AST cleanup:** `58a60595` and `76fbabc2` hardened
  symbol-index snapshots and declaration ordering, sealed cloning, removed
  unused `ast.Index`, and strengthened clone identity/isolation tests.
- [x] **LSP rename snapshot consistency:** rename uses one captured compiler
  context instead of rereading `LastCtx` during background replacement.
- [x] **Transitive lockfile edge selection:** `71bdeef` records the recursive
  resolver's selected package ID on parent edges. Regression covers competing
  versions and persisted lockfiles.
- [x] **Structural type identity and layout:** `fbdbb52b` makes anonymous
  struct field order part of identity and keeps named structs nominal. Distinct
  struct types no longer convert; contextual literals still lower in
  declaration order.
- [x] **THIR lookup experiment:** reverted. `Module.Node` uses its
  module-owned index rather than repeated function scans.
- [x] **Lockfile direct-alias consistency:** current
  `pkg/manifest/lockfile.go:SetDirectDependency` publishes the replacement
  alias before demoting the old package and checks `isStillDirect` first.
  Current tests cover one-alias demotion and preserving a package referenced
  by another alias. This item is resolved in the current source; it is not an
  open implementation task.
- [x] **CI toolchain classifier coverage:** current
  `scripts/detect-changes.sh` includes `cmd/release-profile/*` in the
  toolchain classification. The earlier omission is resolved.

## Remaining compiler correctness work

### 1. High — Capture rvalues before later operand side effects

**Status:** open. Current source confirms the defect shape; no candidate is
present in this checkout.

**Owner:** `internal/ir/mir/expr_lower.go`, with the source-to-IR place/value
boundary in `internal/ir/exprlower/lower.go`.

`exprlower.LowerIdent` currently produces `ir.Ident`. MIR lowering of an
identifier produces a `RefName`, while LLVM `emitRef` loads that name when it
is emitted. A call argument or binary operand can therefore retain storage
identity until a later nested operand has already mutated the storage. MIR
lowering also emits nested call side effects while constructing the outer
call's arguments.

Historical reproduction:

```peep
#[extern("memset")]
fn Memset(p: rawptr, value: i32, size: usize) -> rawptr;
fn Clear(p: rawptr) -> i32 { Memset(p, 0, 4); return 0; }
fn Sum(a: i32, b: i32) -> i32 { return a + b; }

fn main() {
    let mut x: i32 = 7;
    println(Sum(x, Clear(@x)));
}
```

Historical result was `0`; expected result under left-to-right evaluation is
`7`. The same shape was reproduced with `println(x + Clear(@x))`.

Required result:

- materialize rvalue reads at their evaluation point using existing MIR
  mechanisms;
- retain storage identity for assignment, address, and borrow destinations;
- do not blindly snapshot place roots;
- distinguish MIR instruction behavior from language-level ownership
  consumption.

Regression coverage must include call arguments, binary operands, aggregate
elements, index evaluation, preserved writes and borrows, and existing
move/drop/ownership behavior. The historical candidate paths
`internal/ir/mir/expr_lower_order_test.go`,
`x_test/runtime_mir_rvalue_eval_order_call_capture/`, and
`x_test/runtime_mir_rvalue_eval_order_binary_capture/` are absent here.

### 2. High — Make narrow shift guards width-safe

**Status:** open. Current source still contains the lossy guard.

**Owner:** `internal/backend/llvm/instruction_emit.go`.

Current shift lowering computes operand width, then compares `right` against
`bits` using `right.Layout`:

```go
invalid := b.compare("icmp", "uge", right,
    b.value(strconv.Itoa(bits), right.Layout))
```

For a narrow count such as `u5`, the bound `32` is not representable in the
count type. LLVM therefore receives an invalid or wrapped bound instead of a
comparison in a type that can represent both values. The later cast to the
shift operand type happens only after this guard.

Historical reproduction:

```peep
fn Shift(count: u5) -> u32 { return 1u32 << count; }
fn main() { println(Shift(1u5)); }
```

Required result: validate signed negative and oversized counts in a width that
can represent the count and operand-width bound, then convert to the shift
operand width only after validation. Cover both directions, signed and
unsigned counts, zero and highest valid counts, the exact bound, wide counts
that would truncate to valid values, odd widths, and 32-/64-bit target
layouts. Historical candidate files are absent here.

### 3. Medium — Publish cached objects across filesystems safely

**Status:** open. Current source still stages in system temporary storage and
renames across filesystems.

**Owner:** `cmd/build.go`.

`buildExecutable` creates `artifactDir` with `os.MkdirTemp("", ...)`, then
publishes each compiled object with `os.Rename(objectPath, cachePath)`. A
managed build whose temporary directory and project cache use different
filesystems can fail with `invalid cross-device link`.

Required result: stage the object on the cache destination filesystem and
atomically publish it there. Incomplete objects must remain invisible to cache
readers. Test differing temporary/cache filesystems and cleanup on compiler or
publish failure.

### 4. Medium — Preserve negative zero in runtime floating negation

**Status:** open. Current source still emits `fsub` from positive zero for
floating unary minus.

**Owner:** `internal/backend/llvm/instruction_emit.go`.

The floating branch of unary `-` currently emits:

```go
b.arithmetic("fsub", b.value("0.0", arg.Layout), arg)
```

This differs from constant folding for signed zero. Historical reproduction
used `1.0f64 / Neg(0.0f64)` and observed `inf` instead of `-inf`.

Required result: emit LLVM `fneg` and prove constant/runtime parity for signed
zero in both `f32` and `f64`.

### 5. Medium — Reconcile floating remainder with the specification

**Status:** open. Current source is inconsistent across specification,
typechecking, constant folding, and backend emission.

`docs/language-spec.md` says floating division and remainder keep IEEE
behavior. LLVM emission already selects `frem` for floating `%`, but
`internal/semantics/typechecker/check_expr.go` accepts `%` only for integral
types, and `internal/constvalue/value.go` has no floating `%` case.

Required result: confirm the intended language rule, then make typing,
constant evaluation, and runtime behavior agree. Do not remove specified
behavior merely to avoid implementation work. Cover floating boundary cases
and constant/runtime parity.

### 6. Medium — Discover new files in existing empty directories

**Status:** open. Current source retains the incomplete discovery shortcut.

**Owner:** `internal/lsp/workspace.go`.

The fast path returns cached module paths when `directoriesUnchanged` is true.
`captureDirectoryStamps` records the workspace root, source directory, and
directories containing discovered files. It does not record every directory
traversed during discovery, including pre-existing empty nested directories.
Creating a source file in one of those directories can therefore leave the
directory stamps unchanged and omit the new file when no open-buffer overlay
exists.

Required result: track all relevant traversed directories for invalidation, or
remove the incomplete shortcut. Add an unopened-file regression while
preserving existing discovery and incremental behavior. LSP workspace
concurrency checks are required for any implementation change.

## Broader repository findings to recheck before fixing

### 7. Lockfile alias/direct-dependency consistency

**Status:** resolved on current source; retained here as revalidated evidence.

`SetDirectDependency` now uses `isStillDirect` before clearing the previous
package's `IsDirect` flag. Tests cover replacement, shared aliases, and
dependency-edge `UsedBy` rewiring. Any later lockfile work must still audit
forward edges, reverse `UsedBy` edges, direct flags, pruning, and persistence
without inventing another mirrored representation.

### 8. Installer release-signature verification

**Status:** open.

The release workflow currently builds an unsigned manifest and publishes
`SHA256SUMS`. Both `scripts/install.sh` and `scripts/install.ps1` authenticate
the manifest and components only by checksums downloaded from the same
release. The repository has signature-related source metadata paths, but the
installers do not use authenticated release signatures.

Required result: confirm the intended trust model and use authenticated
release metadata consistently on Unix and Windows. Checksums alone do not
establish publisher authenticity. Cover tampered and unsigned metadata under
the selected policy.

### 9. CI change classification

**Status:** resolved in current source. `scripts/detect-changes.sh` includes
`cmd/release-profile/*` in the toolchain case, matching toolchain fingerprint
inputs. Keep a regression if classifier rules are changed later.

### 10. Current contributor documentation

**Status:** open documentation-only work.

Current spot checks still find stale architecture claims:

- `Code-tour.md` names `internal/project` as the owner of `Module` and lists
  `hir` under `internal/ir`, while current ownership is in `internal/module`
  and the repository has no HIR artifact.
- `docs/architecture/ir.md` names removed
  `internal/ir/thir/build.go`; current THIR materialization is owned by
  `internal/semantics/typechecker/thir_build.go`.
- Adjacent phase and infrastructure descriptions must be checked against live
  source, including current `HasProvidedContent` and parser/workspace
  fingerprint ownership.

Required result: repair inaccurate paths, artifacts, phase descriptions, and
ownership claims only after source verification. Keep `FlowAnalyzer`,
`EffectBuilder`, and existing filenames. Do not repeat the rejected terminology
migration or add a documentation framework.

## Architecture and performance follow-ups

These are investigations, not proven fixes:

- **Workspace rebuild cost:** `bdd87ec` shared project context setup and
  `f18a320` reused one index pass across diagnostic components. Discovery,
  manifest, hash, and graph work remains per refresh. Measure real rebuild work
  before adding cache representations.
- **Safe per-function reuse:** identify one independently produced artifact,
  every producer and consumer, and all semantic inputs: body/declaration,
  scopes/imports, constants/defaults, generic instances, target/configuration,
  diagnostics, and topology. FunctionID is identity, not cache validity.
  Require demonstrated skipped work, clean-build parity, dependency-change
  miss detection, and a full-module fallback when uncertain. Persistent
  caching follows a proven in-memory boundary.
- **`TypeTable.Type` backing slices:** shallow returned descriptors still expose
  mutable backing slices. No production mutation was found in the earlier
  audit, so this remains a latent ownership hazard rather than a demonstrated
  active defect. Recheck before changing APIs.

## Validation and next step

For each source fix, validate the affected package first, then affected
packages and the full suite. Language behavior changes require focused Go
tests, positive and applicable negative `x_test/` fixtures, bundled executable
validation, affected target widths, and backend output checks. The repository
validation set is:

```sh
env CCACHE_DISABLE=1 GOCACHE=/tmp/peeper-go-cache go test -count=1 ./...
env CCACHE_DISABLE=1 GOCACHE=/tmp/peeper-go-cache go vet ./...
env CCACHE_DISABLE=1 GOCACHE=/tmp/peeper-go-cache go run ./scripts/bundle.go
env CCACHE_DISABLE=1 GOCACHE=/tmp/peeper-go-cache PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test
git diff --check
```

Next implementation step: reproduce finding 1 or 2 against the current source,
then make one focused fix in its canonical owner. Do not discard or silently
overwrite unrelated work if a later checkout contains candidate changes.

Preserve unrelated local artifacts and review inputs, including
`docs/superpowers/plans/2026-09-29-function-level-incremental-reuse.md`,
`final-code-review.md`, `review.md`, and `lsp.test` when they exist.
