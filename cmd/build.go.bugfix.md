# Bugfix: object-cache publication

Source reports: conversation code review P1/P2 and follow-up validation;
`docs/end-to-end-code-review.md`, finding 3.

Date: 2026-10-03
Mode: applied directly

## Results

1. **P1 — Non-atomic backup-first cache publication — Fixed.**
   `compileObject` stages compiler output on the cache filesystem, then publishes
   with direct `os.Rename`. Existing cache paths are never moved aside. When
   replacement fails, another publisher's regular cache object can be reused;
   without one, the original rename error is returned with `%w`.
2. **P2 — Missing production failure-cleanup coverage — Fixed.**
   Compiler/publication failure tests exercise the production object lifecycle,
   assert errors and stage cleanup, and verify incomplete output stays hidden.
   Cross-device probes require `EXDEV`. A bundled Peeper fixture tests real
   managed compilation/linking across filesystems and subsequent cache reuse.

## Evidence and validation

- Before fix: Linux filesystem-event test detected published cache object moved
  away; concurrent reader observed missing file. Both pass after fix.
- Focused `cmd` and `internal/toolchain` tests pass, including cache hits,
  compiler failures, publication failures, conflicting directories, winner
  reuse, output permissions, and per-object cleanup.
- `go test -race -count=1 -run '^TestCompileObject' ./cmd` passes.
- Full `go test -count=1 ./...`, `go vet ./...`, fresh bundle, complete executable
  `x_test` suite, formatting check, and `git diff --check` pass.
- Windows/amd64 and macOS/arm64 test binaries cross-compile. Native execution
  also passed in PR #151 CI run 37146240861, including Windows locked-reader
  regression. Linux native cache Go tests passed; Linux integration fixture
  runtime exposed the test setup ABI mismatch described below.

## Linux CI fixture follow-up

Both Ubuntu architectures linked the synthetic managed fixture for musl with
system Clang, then failed execution because musl interpreter was absent.
Test-local Clang launchers now select `target.SystemLLVMTriple` as final tool
target while leaving installed-profile validation and production code intact.
Real compilation/linking, cross-filesystem assertions, executable checks, and
cache-hit proof remain enabled.

Targeted fixture, full executable `x_test`, `internal/target` tests, `go vet
./x_test`, formatting, and diff checks pass locally. Clang dry-runs confirm glibc
interpreter selection for amd64 and arm64. Follow-up CI rerun pending publication.

## Files changed

- `.github/workflows/ci.yml`
- `cmd/build.go`
- `cmd/build_test.go`
- `cmd/build_linux_test.go`
- `cmd/build_windows_test.go`
- `x_test/cache_linux_test.go`
- `x_test/managed_object_cache/peeper.toml`
- `x_test/managed_object_cache/src/main.peep`
- This summary and ignored `review-followups.localplan.md`.

## Rules check

`compileObject` replaces existing loop/staging logic and owns a real per-object
cleanup lifetime. It has a current production caller. Obsolete `stageCachedObject`
removed. No pass-through wrapper, stale alias, ignored parameter, duplicated
cache decision, or production test hook added. Cache keys, compiler arguments,
diagnostics, error identity, and permissions preserved; publication and staging
lifetime intentionally repaired.
