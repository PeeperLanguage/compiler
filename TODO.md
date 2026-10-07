# Peeper Backlog

Baseline: `main` at `03a32726f8a356a2b61c53232f4fecb28adc3350`  
Reviewed: 2026-10-06  
Source of truth: current source, tests, architecture docs, and GitHub issue state.

This file is the durable, tracked backlog. Historical review journals and ignored
local plans are evidence, not task status.

Status markers:

- `[ ]` planned or open
- `[D]` decision required before implementation
- `[x]` complete and verified on current main
- `[H]` historical/reference only

## Current state

- `main` and `origin/main` match at `03a3272`.
- PR #156 merged at `05092be`.
- PR #158 merged at `57e7995`.
- Post-merge native-Clang ABI test fix is merged in `03a3272`.
- Latest main CI passes Unit, Race, Quality, and native target jobs.
- No open pull requests.
- Roadmap project read is blocked by the GitHub token's missing `read:project` scope.
- Existing untracked `.bugfix.md`, `.nightmare.md`, and `todo.txt` files are local
  historical material. Do not treat them as canonical backlog or delete them without
  explicit cleanup approval.

## Now

### [ ] #157 — Qualified range endpoints before loop bodies

Type: implementation  
Owner: `internal/frontend/parser` and affected range semantic tests  
Issue: https://github.com/PeeperLanguage/compiler/issues/157  
Evidence: `internal/frontend/parser/parse_stmt.go`, `parse_expr.go`

Parse a bare qualified range endpoint such as `values::Zero..values::One` without
consuming the loop-body `{` as a legacy enum payload.

Acceptance:

- Parse bare qualified endpoints and retain endpoint bindings/source locations.
- Cover ascending, equal, reversed, and runtime ranges.
- Preserve explicit enum construction and malformed payload diagnostics outside
  control headers.
- Add focused parser/semantic tests and applicable `x_test/` fixtures.
- Validate with rebuilt `build/bin/peeper`.

### [ ] MIR typed-nil validation hardening

Type: implementation; no GitHub issue currently linked  
Owner: `internal/ir/mir/validate.go`  
Evidence: `constant-binding-identity.localplan.md` Step 5;
`internal/ir/mir/validate.go:182-184,397-413,562-624`

Replace nested plain-nil checks that call `TypeID()` on possible typed-nil MIR
values. Use the existing typed-nil contract at the canonical validator boundary.

Acceptance:

- Nested typed-nil branch conditions, call values, place roots/projections,
  interface bases/arguments, and expression operands return validation errors.
- No validator path panics on typed-nil values.
- Valid MIR shapes retain current diagnostics and lowering behavior.
- Add focused malformed-MIR tests and positive controls.
- Confirm pipeline refuses emission after validation failure.

### [ ] Backend-independent MIR invariant validation

Type: implementation; no GitHub issue currently linked  
Owner: `internal/ir/mir/validate.go` and pipeline validation boundary  
Evidence: `internal/pipeline/pipeline.go:470-500`,
`internal/ir/mir/validate.go:501-516`,
`constant-binding-identity.localplan.md` Step 6

Complete validation before backend emission. Current gap: fixed-array literals
validate element types but do not compare value count with declared array length.
Decide explicitly whether direct `llvm.GenerateLLVMIR` callers also require a
validation boundary or whether pipeline ownership is sufficient.

Acceptance:

- Fixed-array cardinality mismatch is rejected before emission.
- Backend-independent type/reference/shape invariants are checked once at the
  canonical boundary.
- Direct backend callers retain an explicit documented contract.
- Add malformed and valid MIR tests; preserve external/static legitimate cases.

### [D] Default-argument snapshot semantics

Type: language-design decision before implementation  
Owner: `internal/semantics/typechecker/check_call.go`  
Evidence: `check_call.go:525-612`, `docs/default-parameters.md:73-95,244-257`

Decide whether omitted defaults observe staged, once-per-call values of earlier
arguments or continue using source-expression substitution. Current code rejects
effectful argument reuse and intentionally defers staged evaluation.

Do not implement until argument order, mutation, ownership/borrow behavior, imported
defaults, and full-arity ABI semantics are explicitly selected and covered by tests.

### [D] Installer publisher-authentication policy

Type: release/security policy decision before implementation  
Owner: release workflow, manifest, `scripts/install.sh`, `scripts/install.ps1`  
Evidence: `review-followups.localplan.md:1782-1801`,
`.github/workflows/release.yml:123-180`,
`pkg/distribution/release.go`, installer scripts

Choose checksum-only acceptance or signed release-manifest verification. Current
release behavior uses HTTPS plus SHA-256 checksums; no signing algorithm, trust-root
storage, rotation/revocation policy, or PowerShell-compatible verifier is selected.

No signing implementation is authorized by this backlog item alone.

### [ ] Documentation/source-contract reconciliation

Type: documentation  
Owner: each document's live source owner

Correct only concrete contradictions found during review:

- `docs/default-parameters.md` says compiler behavior is unimplemented while
  trailing defaults already compile; retain explicit deferred staged semantics.
- `docs/architecture/cli-and-packages.md` describes signed release metadata while
  current workflow publishes unsigned metadata plus `SHA256SUMS`.
- `docs/compiler-architecture.md:265-268` assigns usage state to `symbols.Symbol`,
  while current owner is `symbols.Index`; align with
  `docs/architecture/semantics-bindings.md`.
- `Code-tour.md` contains stale path/function references such as `cfg.BuildQueries`;
  reconcile against current source before editing it.
- Mark `docs/end-to-end-code-review.md` as historical remediation scope rather than
  a claim that every later audit finding is resolved.

Each edit needs source/path verification and documentation diff validation.

## Next

### [ ] #133 — Explicit struct literals

Issue: https://github.com/PeeperLanguage/compiler/issues/133  
Review grammar publication, migration status, parser/typechecker behavior, and
language-support documentation. Coordinate with #157 before changing overlapping
control-header or literal grammar.

### [ ] #122 — Windows installer end-to-end validation

Issue: https://github.com/PeeperLanguage/compiler/issues/122  
Run `install.ps1` on a Windows runner and record actual release/install behavior.

### [ ] Remove redundant origin copies after measurement

Type: measured cleanup  
Evidence: `internal/semantics/analysis/module.go:179-220`,
`internal/semantics/analysis/flow_thir.go:238-242,567-574`,
`internal/semantics/analysis/ownership.go:604-608`

Prove alias isolation first, measure allocation/copy cost, then remove only copies
that are not required by detached-storage ownership.

### [ ] Simplify the `Check`/`runCheck` test boundary

Type: test-boundary cleanup  
Evidence: `internal/semantics/typechecker/typechecker.go:120-135`,
`internal/semantics/typechecker/typechecker_test.go:29-92`

Remove the test-only evidence bridge only if tests can assert durable THIR/public
results without exposing private production evidence or creating a second checking
lifecycle.

### [ ] #58 — Module version selection

Issue: https://github.com/PeeperLanguage/compiler/issues/58  
Design graph-wide version selection before dependent package tooling such as #74.

### [ ] #74 — Auto-import completion

Issue: https://github.com/PeeperLanguage/compiler/issues/74  
Depends on stable module/package identity decisions.

### [ ] #82 — String C/FFI bridge

Issue: https://github.com/PeeperLanguage/compiler/issues/82  
Requires explicit ownership, lifetime, allocator, and ABI decisions.

### [ ] #64 — Scoped/custom allocator behavior

Issue: https://github.com/PeeperLanguage/compiler/issues/64  
Requires lifetime/region ownership design.

### [ ] #134 — LSP struct-literal snippets

Issue: https://github.com/PeeperLanguage/compiler/issues/134  
Coordinate with #133 grammar and type-shape decisions.

## Later

- [ ] #30 — Optional niche layouts.
- [ ] #89 — Value-producing match expressions.
- [ ] #90 — Advanced generic functions/methods, constraints, inference, and
  monomorphization.
- [ ] #91 — Enum protocols and metadata operations.
- [ ] #92 — Compact tagged-variant payload storage.
- [ ] #93 — Location-sensitive loan analysis.
- [ ] #95 — Advanced enum match patterns.
- [ ] #96 — Explicit enum representation and foreign ABI.
- [ ] Incremental Stage C–F: function-owned evidence, measured function reuse, then
  persistent cache. Do not add cache/remapping layers before measurement and stable
  identity/lifetime design. Evidence: `docs/architecture/infrastructure.md:253-300`.
- [ ] Revisit deferred language-surface decisions from the old audit only when
  product direction selects them: wider formatting, arbitrary-width floats,
  compound bitwise assignment, optional convenience operations, and borrowed
  `&str` comparison semantics.
- [ ] Decide interface-method default parameters and function-value preservation.

## Completed

- [x] #154 — Wide constant indexes without truncation.
- [x] #155 — Checked numeric conversion/publication.
- [x] PR #156 — Binding identity repair.
- [x] PR #158 — Numeric publication and fixed-array/GEP repairs.
- [x] Native-Clang system-ABI test fix (`03a3272`).
- [x] Workspace/LSP remediation recorded in `internal/lsp/workspace.go.bugfix.md`.
- [x] TypeTable detached-copy ownership boundary; historical finding no longer open.
- [x] Workspace rebuild-cost measurement; no function-level reuse justified by current
  measurements.

## Historical sources, not active status

- `constant-binding-identity.localplan.md`
- `constant-numeric-publication.localplan.md`
- `peeper-code-review.localplan.md`
- `review-followups.localplan.md`
- `high-bit-gep.localplan.md`
- `task.md`
- `docs/end-to-end-code-review.md`
- `internal/semantics/typechecker/constant_eval.go.bugfix.md`
- `internal/semantics/typechecker/constant_eval.go.nightmare.md`

When a historical file conflicts with this backlog, re-check current source and
GitHub metadata before changing status.

## Fresh-session start

1. Read `TODO.md`, `RULES.md`, `AGENTS.md`, and the selected issue.
2. Check `git status --short --branch`, `HEAD`, and `origin/main`.
3. Re-read source evidence named by selected item; do not trust old plan status.
4. Work one item only; record validation and stop at its review condition.
