# CLI, distribution, and support packages

This map records command-line, packaging, dependency, and support implementation observed in source. Verify mutable details against linked symbols. It describes current entry points around compiler work without prescribing phase ownership or future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## 1. Command entry points

### `cmd`

- `cmd/main.go`: process entry. Rejects `go run`-style execution when the
  executable path contains `go-build`; otherwise dispatches `os.Args[1:]` and
  prints top-level usage when no registered command matches.
- `cmd/dispatch.go`: command registry and common process behavior.
  - `programExitStatus` preserves a child program's exit code.
  - `commandDefinition` stores `Name`, `Aliases`, `Usage`, description, arity,
    and `Handler`.
  - `commandRegistry` is the single command/help table.
  - `lookupCommand` matches canonical names and aliases.
  - `parseAndRunCommand` looks up the first argument, validates arity through
    `commandDefinition.run`, and invokes the handler.
  - `exitOnCommandError` suppresses duplicate diagnostic output for
    `errAlreadyReported`, preserves `programExitStatus`, and prints other
    errors in red before exit.
  - top-level `-version`/`-v` and `-help`/`-h` are defined by
    `defineTopLevelFlags`; `printTopLevelUsage` renders registry entries.
- `cmd/command.go`: build/check/run command mechanics.
  - `emitAndCheckDiagnostics` emits pending compiler diagnostics once and
    returns `errAlreadyReported` on errors.
  - `addCommandCommonFlags` and `applyCommandCommonFlags` handle log format,
    `-m32`, `-target-os`, and `-target-arch`.
  - `parseCommandArgs` handles common flags and optional `-debug`.
  - `buildCommand` resolves an entry, compiles, optionally writes `_gen`, then
    links an executable.
  - `runCommand` uses the same compile/link path, creates a temporary native
    executable, forwards remaining arguments, and converts process failure to
    `programExitStatus`.
  - `resolveBuildTarget` accepts a source file or directory; directories use
    manifest discovery. `resolveManifestBuildTarget` requires `build =
    "program"`, selects `src/main.peep`, and derives the default output name.
  - `checkCommand` discovers `.peep` files, groups them by canonical project,
    compiles each group, emits diagnostics, and reports failure after all groups.
- `cmd/build.go`: compilation-to-native-output boundary.
  - `compileEntry` resolves source-file project context and calls
    `compiler.CompileFile` with `RequireEntrypoint`.
  - `buildExecutable` resolves a managed or PATH toolchain, writes each module's
    LLVM IR to temporary files, compiles objects, writes a response file, links,
    and atomically publishes the output.
  - `runCompilerTool` preserves tool output when a compiler or linker fails.
- `cmd/dump.go`: `-keep-gen` artifact publication. `saveIRs` writes MIR and
  LLVM IR under an identity-encoded tree; `moduleArtifactBase` includes
  origin, namespace, dependency, and import path so distinct modules cannot
  collide; `replacePath` stages and swaps the complete directory.
- `cmd/doctor.go`: installation report. `doctorReport` can be printed or JSON;
  `inspectInstallation` checks the core library, resolves the host toolchain,
  validates compiler/linker files, and checks managed runtime ABI/archive data.

### Registered top-level commands

The following list is the literal `commandRegistry` in `cmd/dispatch.go`.
Aliases dispatch to the same definition, not separate handlers.

| Command | Aliases | Handler and behavior |
| --- | --- | --- |
| `build [path]` | `build:llvm` | `buildCommand`; compile and link a program or manifest-selected `src/main.peep`. |
| `run [path] [args]` | `run:llvm` | `runCommand`; build a temporary executable and run it. |
| `check [path ...]` | `lint` | `checkCommand`; recursively discover and typecheck `.peep` files. |
| `init [name]` | — | `cli.InitCommand`; create a project skeleton and manifest. |
| `get [pkg ...]` | — | `cli.GetCommand`; install manifest dependencies or named packages. |
| `update [pkg ...]` | — | `cli.UpdateCommand`; resolve and install newer locked remote versions. |
| `sniff [pkg ...]` | — | `cli.SniffCommand`; report available updates without saving. |
| `remove <alias>` | `rm` | `cli.RemoveCommand`; remove manifest and direct lock dependency state. |
| `list` | `ls` | `cli.ListCommand`; print direct and transitive dependency state. |
| `cleanup` | `clean` | `cli.CleanupCommand`; delete orphan cache and lock entries. |
| `orphans` | — | `cli.OrphansCommand`; list orphan candidates without deleting them. |
| `doctor [--json]` | — | `doctorCommand`; inspect installation and native toolchain. |
| `lsp` | — | `lspCommand`; run `internal/lsp` over stdin/stdout. |

`commandDefinition.run` enforces `MinArgs`/`MaxArgs` before handlers run. The
registry tests check unique names, aliases, arity contracts, and help output.

## 2. `cmd/cli` dependency commands

`cmd/cli` owns user-facing dependency operations. It uses `manifest` for
project state, `registry` for package content and versions, `remotes` for host
validation, and `semver` for constraints. `ui.go` is a thin stdout vocabulary:
`printHeader`, `printSuccess`, `printInfo`, `printWarning`, `printError`,
`printUpdate`, `printPackage`, `printDim`, `printDownload`, `printCached`, and
`printTransitive`.

- `init.go`: `InitCommand` preflights `peeper.toml`, `src`, and `src/main.peep`,
  normalizes project name whitespace/hyphens to underscores and lowercase,
  validates it, writes a Hello World entry and manifest, and removes artifacts
  it created if a later write fails.
- `get.go`: `installContext` carries root, cache, manifest, lockfile, and dev
  remote configuration. `GetCommand` installs all manifest dependencies or
  explicit package specs, joins per-package errors, prunes unused lock entries,
  saves manifest plus lockfile, and removes pruned cache entries.
  `installManifestDependencies` preserves neighbor dependencies and recursively
  resolves remote dependencies. `installPackageRecursive` intersects version
  constraints, reuses compatible lock entries, downloads/verifies content,
  records direct and transitive edges, and updates `UsedBy`. `ensurePackageContent`
  reuses a matching checksum or downloads into a staged cache path.
  `installPackage` parses a named package spec and adds it to the manifest.
  `findBestLockedPackageID` and `resolvedDirectVersion` select canonical locked
  versions.
- `update.go`: `UpdateCommand` uses the shared scan context and update plans,
  installs each target with a constraint above the current version and at most
  the selected target, updates manifest constraints, prunes, then saves state.
- `sniff.go`: `SniffCommand` shares update scanning but only prints plans.
- `depscan.go`: `updateScanContext`, `updatePlan`, `orphanCandidate`, and
  `prunedDependency` are the dependency graph/cache records. `collectUpdatePlans`
  queries available versions and chooses the highest semver match. `updateConstraint`
  turns exact locked versions into `latest` for update scans. `listOrphanCandidates`
  combines unused lock entries with cached package directories not referenced by
  the lock graph. `pruneUnusedDependencies` cascades removal through `UsedBy`;
  `deletePrunedDependencies` removes corresponding registry paths.
- `remove.go`: `RemoveCommand` deletes an alias, removes its direct lock mapping,
  cascades unused entries, saves both state files, and deletes pruned cache paths.
- `list.go`: `ListCommand` loads manifest and lockfile, prints remote/neighbor
  direct dependencies, locked IDs, and non-direct transitive entries.
- `cleanup.go`: `CleanupCommand` applies `listOrphanCandidates`, removes lock
  orphans and stale cache trees, persists the lockfile, and reports count.
- `orphans.go`: `OrphansCommand` applies the same candidate discovery but only
  reports whether each item is stale cache or an unused lock dependency.

## 3. Distribution and release commands

### `pkg/distribution`

`pkg/distribution` defines deterministic component archives and signed release
metadata. Its main files are `pack.go`, `extract.go`, `release.go`, and
`toolchain_lock.go`.

- `Format` supports `tar.gz` and `zip`; `Format.Extension` maps them to
  `.tar.gz` and `.zip`.
- `Metadata` identifies `Kind`, immutable `ID`, `Version`, `OS`, and `Arch`.
- `FileRecord` records archive path, normalized mode, type, size, digest, and
  symlink target. `Manifest` records schema, metadata, format, inventory, size,
  and archive SHA-256.
- `WritePack` validates metadata, rejects output inside source, inventories
  directories/files/symlinks, embeds `pack-manifest.json`, writes deterministic
  tar-gzip or zip output, and hashes the published archive.
- `ExtractPack` reads and strictly decodes the embedded manifest before writing.
  `validatePackManifest` checks schema, expected metadata/format, inventory,
  modes, digests, limits, and safe relative paths. Tar and zip extraction then
  require archive entries to match the manifest, reject traversal and symlink
  parents, verify regular-file hashes, and require a complete inventory.
- `ReleaseComponent`, `InstallSet`, `ReleaseManifest`, and `ReleaseArtifact`
  model published compiler/toolchain components. `BuildReleaseManifest` accepts
  HTTPS artifacts, validates supported hosts, sorts components, and requires one
  compiler plus one toolchain for each supported host:
  darwin/linux/windows × amd64/arm64.
- `SignReleaseManifest` signs exact bytes with Ed25519 and base64. `VerifyReleaseManifest`
  verifies before strict JSON decoding, validates all install sets, then selects
  the host-specific ordered compiler/toolchain pair.
- `ToolchainLock` and `ReadToolchainLock` validate schema `1`, kind
  `peeper-toolchains`, unique component IDs, and exactly one valid toolchain per
  supported host. `Component` selects one host record.

### Distribution-oriented `cmd` programs

- `cmd/distpack`: flags a staged source root, output, format, kind, ID, version,
  OS, and arch; calls `distribution.WritePack`; emits the resulting JSON
  `Manifest`.
- `cmd/distunpack`: validates required archive/format/destination/metadata
  flags and calls `distribution.ExtractPack`.
- `cmd/release-profile`: creates a managed native toolchain profile with
  `toolchain.NewManagedProfile`, validates it through `toolchain.Load`, then
  atomically publishes `toolchains/native/profile.json` under a staged root.
- `cmd/toolchain-lock`: reads the finished lock and emits one selected host
  `ReleaseComponent` as JSON.
- `cmd/release-index`: strictly reads pack-result JSON files and a toolchain
  lock, calls `distribution.BuildReleaseManifest`, and emits release JSON.
- `cmd/sign-release`: reads `PEEPER_RELEASE_PRIVATE_KEY` as hex, signs the exact
  release-manifest bytes, and prints the base64 signature.

## 4. Registry, remotes, manifests, and format support

### `pkg/remotes`

`Provider` recognizes `github.com`, `gitlab.com`, and `bitbucket.org`. `Parse`
returns provider plus provider-local repository path, allowing nested GitLab
groups but exactly owner/repository for GitHub and Bitbucket. Segment validation
uses `ascii.IsAlnum` and permits `-`, `_`, and `.`. `IsRemotePath` is the boolean
validator; `StripProviderPrefix` returns the provider-local path when valid.

### `pkg/registry`

- `cache.go`: `GetModulePath` maps validated `provider/repository@version` identity
  into the project cache; `DeleteModule` removes that module.
- `checksum.go`: `ModuleChecksum` hashes sorted relative regular-file paths and
  framed path/size/content bytes as `sha256:<hex>`. Symlinks and unsupported file
  types are rejected.
- `download.go`: `DownloadRemotePackage` validates stable semver and checksum,
  then stages either a mock directory or provider archive. `ListAvailableVersions`
  dispatches to GitHub, GitLab, or Bitbucket tag APIs. Downloads use bounded
  response sizes/timeouts; pagination cannot change origin. `stageModule` loads
  the package manifest, computes checksum, compares any expected checksum, and
  replaces cache only after successful staging. Archive extraction requires one
  safe root and enforces entry and extracted-size limits.

### `pkg/manifest`

- `manifest.go`: `FileName` is `peeper.toml`; cache is `.peeper/modules`.
  `PackageInfo`, `BuildType` (`program`/`lib`), `DevConfig`, `Dependency`, `File`,
  `Project`, and `SourceFileProject` are the manifest models.
  `FindManifestPath` searches upward. `LoadProject` loads and validates a
  project. `ResolveSourceFileProject` applies the same manifest/source-boundary
  rule to CLI and LSP. `Load` parses package metadata, compiler constraint,
  build type, neighbor/remote dependencies, and optional mock-remote settings.
  `ValidatePackageName` and `ParseDependency` own package/dependency validation.
  `Save` and `RemoveDependency` serialize manifest state.
- `lockfile.go`: `LockfileEntry` stores version, resolved URL, checksum, direct
  bit, description, dependency edges, reverse `UsedBy`, and timestamp.
  `Lockfile` stores schema, alias-to-package `DirectDeps`, package entries, and
  generation time. `LoadLockfile` accepts a missing lockfile as a new v2 lock,
  migrates legacy shapes, validates `sha256:` checksums, and normalizes maps.
  Mutation methods (`SetDependency`, `RemoveDependency`, direct-dependency
  methods, `AddUsedBy`, `RemoveUsedBy`, `UpdateDependencyEdges`) maintain both
  forward and reverse graph state. `GetUnusedDependencies` identifies non-direct
  entries with no users. `PackageID` and `SplitPackageID` define `repo@version`.
- `write.go`: `WriteFileAtomic` stages, syncs, renames, and syncs the parent
  directory. `SaveDependencyState` stages manifest and lockfile together and
  rolls back when the second publication or directory sync fails. This is a
  returned-error transaction, not crash recovery across both renames.

### `pkg/toml`

The repository has a focused TOML reader rather than a general TOML dependency.
`ParseError` preserves line numbers. `ParseFile`, `ParseString`, `ParseBytes`,
and `Parse` build `Data`, `Table`, `Array`, and `Value` values while preserving
section/key order. The parser handles quoted strings, booleans, integers,
floats, arrays, inline tables, comments outside quotes, duplicate detection,
and balanced composites. `Lookup`/`LookupKey` use generic `As`; `Data.Decode`
uses reflection, section/default/inline tags, scalar conversion, maps, slices,
and nested structs.

### `pkg/semver`

`Version` contains major/minor/patch; `Parse` accepts optional `v` and strict
three-component versions. Constraints support `latest`, `*`, exact, `=`, `>=`,
`>`, `<=`, `<`, `~`, and `^`. `ValidateConstraint` checks syntax; `Match` tests
one version; `BestMatch` and `BestMatchAll` select the highest valid matching
candidate. Invalid candidate tags are skipped during selection.

## 5. Small shared packages

- `pkg/ascii/ascii.go`: `IsLetter`, `IsDigit`, and `IsAlnum` provide ASCII-only
  classifiers used by remote-path validation.
- `pkg/colors`: `COLOR` constants cover ANSI/basic/bright/bold/extended colors.
  `LogFormat` supports `ansi`, `normal`, and `html`; `Logger` is synchronized and
  renders ANSI, plain, or escaped styled HTML text. Package setters configure a
  process-wide default logger. `COLOR` print, writer, and string methods use it.
- `pkg/numeric/numeric.go`: owns numeric spelling and bounds. Regex classifiers
  cover decimal, hexadecimal, octal, binary, floats, separators, and postfixes.
  `Literal` separates cleaned value from explicit type. `ParseLiteral`,
  `ValidateLiteral`, `StringToBigInt`, `StringToFloat`, canonicalization, and
  `Fits*` helpers are shared by semantic and IR-facing numeric handling.
- `pkg/peeper`: constants define compiler version `0.1.0`, `.peep`, `src`, and
  `src/main.peep`. `InstallationRootForExecutable` resolves symlinks and returns
  the directory above `bin`.
- `pkg/typednil`: `IsNil` is the canonical typed-nil check for nil interfaces
  containing nil pointers; nil maps, slices, channels, and functions are not
  treated as nil by this policy.

## 6. Scripts and toolchain production

- `scripts/build.sh` runs `go run ./scripts/bundle.go` as the local bundle entry.
- `scripts/bundle.go` copies `_builtin_library` to `build/libs`, optionally
  compiles `runtime/peeper_rt.c` with clang and archives it, then builds
  `cmd/peeper` to `build/bin/peeper`.
- `scripts/detect-changes.sh` classifies changes as docs-only, compiler source,
  runtime, distribution, toolchain, or workflow for CI outputs.
- `scripts/install.sh` and `scripts/install.ps1` bootstrap the latest release:
  fetch and verify release-manifest checksum, select host compiler/toolchain
  components, verify component SHA-256, extract into staging, atomically activate
  the installation, and persist `bin` on PATH. Windows uses separate extraction
  directories because PowerShell 5.1 cannot expand into a non-empty directory.
- `scripts/fetch-toolchain.sh` obtains one validated component through
  `cmd/toolchain-lock`, checks downloaded size and SHA-256, then calls
  `cmd/distunpack` with expected metadata.
- `scripts/plan-toolchains.sh` selects all, one OS family, or changed targets;
  it compares desired immutable fingerprints with the finished lock and emits
  six selection outputs.
- `scripts/toolchain-fingerprint.sh` hashes target identity, source-lock data,
  recipes, compiler profile code, and platform build settings. It emits
  fingerprint, version, immutable ID/tag, and LLVM target.
- `scripts/update-toolchain-sources.sh` reads stable upstream releases, requires
  expected assets and SHA-256 metadata, rejects downgrades, updates the source
  lock canonically, and writes a change summary.
- `scripts/update-toolchain-lock.sh` merges successful selected target records,
  sorts them, writes a summary, and validates every supported target through
  `cmd/toolchain-lock`.

### `scripts/toolchains`

- `common.sh`: reads source-lock asset fields and downloads assets with size and
  SHA-256 checks.
- `build-linux.sh`: builds or validates curated static musl/LLVM toolchains for
  amd64 and arm64. Validation checks tools, sysroot, clang resources, ELF
  dependencies, and a static smoke program.
- `build-darwin.sh`: builds clang/lld for amd64 or arm64 from locked LLVM source,
  applies minimum macOS deployment target, removes unused payload, and checks
  required tools.
- `build-windows.sh`: extracts locked LLVM-MinGW amd64 or arm64 payload,
  retains required licenses/tools, and checks clang, archiver, and linker.
- `llvm-LICENSE.TXT`: bundled LLVM license copied into produced toolchains.
- `build_linux_test.go` tests rejection of incomplete managed Linux trees.

## 7. `x_test` fixture harness

`x_test/fixtures_test.go` is an external-package integration harness. It discovers
all immediate fixture directories containing `peeper.toml`, parses each `[test]`
table through `pkg/toml`, and validates required `mode` and
`outcome`. Modes are `check`, `build`, and `run`; outcomes are `success`,
`failure`, or `exit_code`. Optional fields are compiler arguments, program
arguments, stdout substrings, stderr substrings, and stderr exclusions.

With `PEEPER_BIN` unset, manifests are still parsed and contract-checked, then
execution is skipped. With it set, each case runs with a 30-second context:
`check` invokes `peeper check`; `build` invokes `peeper build -o <temp>`; `run`
builds, then executes the temporary program. Results check exit status and all
specified output assertions.

Fixture families visible in the tree cover positive type checks, runtime behavior,
entrypoint/build behavior, imports, ownership/borrows, arrays/slices, optionals,
enums/interfaces/iterators/strings, and usage warnings. `negative_*` fixtures
assert rejected programs and often pin diagnostic text or codes; `runtime_*`
fixtures assert execution; `type_*` fixtures assert successful checking; other
named fixtures cover focused language, LSP, and project-layout cases. The
harness is the executable contract around the compiler CLI; compiler pipeline
ownership remains documented in
[`docs/compiler-architecture.md`](../compiler-architecture.md).
