# Peeper Backlog

Baseline: `origin/main` at `9e0fe281`  
Reviewed: 2026-10-15 (GitHub issue state and source checked; items not re-checked are marked)  
Source of truth: current source, tests, architecture docs, and GitHub issue state.

This file is the durable, tracked backlog. Ignored local plans and notes are evidence, not task status.

Status markers:

- `[ ]` planned or open
- `[D]` decision required before implementation
- `[x]` complete and verified on current main
- `[H]` historical/reference only

## Current state

- `origin/main` is at `9e0fe281` (PRs #159-#164 merged since the old baseline).
- Open work branch: `feature/checks-on-mir-baseline` (checks-on-MIR migration plus
  diagnostics cleanup). It is not merged; its progress is in the ignored local file
  `branch-review.localplan.md`.
- No open pull requests when last checked (2026-10-15).
- Untracked `*.md` notes in the repo root are the author's local notes, not backlog.

## Now

### [ ] Backend-independent MIR invariant validation

Type: implementation; no GitHub issue currently linked  
Owner: `internal/ir/mir/validate.go` and pipeline validation boundary  
Evidence: `internal/pipeline/pipeline.go`, `validateArrayLiteral` in
`internal/ir/mir/validate.go` (checked 2026-10-15)

Complete validation before backend emission. Current gap, still open: fixed-array
literals validate element types but do not compare value count with declared array length.
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
Evidence: `check_call.go` (default-argument substitution), `docs/default-parameters.md` ("Deferred Work")

Decide whether omitted defaults observe staged, once-per-call values of earlier
arguments or continue using source-expression substitution. Current code rejects
effectful argument reuse and intentionally defers staged evaluation.

Do not implement until argument order, mutation, ownership/borrow behavior, imported
defaults, and full-arity ABI semantics are explicitly selected and covered by tests.

### [D] Installer publisher-authentication policy

Type: release/security policy decision before implementation  
Owner: release workflow, manifest, `scripts/install.sh`, `scripts/install.ps1`  
Evidence: `.github/workflows/release.yml`,
`pkg/distribution/release.go`, installer scripts

Choose checksum-only acceptance or signed release-manifest verification. Current
release behavior uses HTTPS plus SHA-256 checksums; no signing algorithm, trust-root
storage, rotation/revocation policy, or PowerShell-compatible verifier is selected.

No signing implementation is authorized by this backlog item alone.

### [ ] Documentation/source-contract reconciliation

Type: documentation  
Owner: each document's live source owner

Correct only concrete contradictions (rechecked 2026-10-15; the others listed earlier are fixed):

- `docs/architecture/cli-and-packages.md` says `pkg/distribution` defines "signed release
  metadata" while the current workflow publishes unsigned metadata plus `SHA256SUMS`.
- `Code-tour.md` shows `cfg.BuildQueries` (around line 309); no such type exists in source.
  Reconcile the tour against current source before editing it.

Each edit needs source/path verification and documentation diff validation.

## Next

### [ ] #122 — Windows installer end-to-end validation

Issue: https://github.com/PeeperLanguage/compiler/issues/122  
Run `install.ps1` on a Windows runner and record actual release/install behavior.

### [ ] Remove redundant origin copies after measurement

Type: measured cleanup  
Evidence: origin copies in `internal/semantics/analysis` (`module.go`, `flow_thir.go`,
`ownership.go`); the old line numbers no longer match, re-find them before starting.

Prove alias isolation first, measure allocation/copy cost, then remove only copies
that are not required by detached-storage ownership.

### [ ] Simplify the `Check`/`runCheck` test boundary

(`runCheck` still exists on main, 2026-10-15.)

Type: test-boundary cleanup  
Evidence: `internal/semantics/typechecker/typechecker.go:103-108`,
`internal/semantics/typechecker/typechecker_test.go`

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
Add completion for current `Type.{...}` syntax using existing required/defaulted
field rules.

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
- [x] Workspace/LSP remediation.
- [x] #157 — Qualified range endpoints (PR #159).
- [x] MIR typed-nil validation hardening (PR #160).
- [x] PRs #161, #162 (clean-code audit, code-hint target name); #163, #164 (toolchain automation).
- [x] TypeTable detached-copy ownership boundary; historical finding no longer open.
- [x] Workspace rebuild-cost measurement; no function-level reuse justified by current
  measurements.

## Fresh-session start

1. Read `TODO.md`, `RULES.md`, `AGENTS.md`, and the selected issue. If the ignored local file `branch-review.localplan.md` is present, read it first: it holds the current branch's state and decisions.
2. Check `git status --short --branch`, `HEAD`, and `origin/main`.
3. Re-read source evidence named by selected item; do not trust old plan status.
4. Work one item only; record validation and stop at its review condition.
