# Compiler distribution

Peeper v1 distribution model is native-only. Compiler builds and runs programs
for its own operating system and architecture; release availability does not
claim cross-compilation support.

## Supported hosts

| Host | Architecture | Generated program ABI | Managed toolchain |
| --- | --- | --- | --- |
| Linux | amd64 | `x86_64-unknown-linux-musl`, static | LLVM plus musl sysroot |
| Linux | arm64 | `aarch64-unknown-linux-musl`, static | LLVM plus musl sysroot |
| macOS | amd64 | `x86_64-apple-darwin` | LLVM plus local Apple SDK from `xcrun` |
| macOS | arm64 | `aarch64-apple-darwin` | LLVM plus local Apple SDK from `xcrun` |
| Windows | amd64 | `x86_64-w64-windows-gnu`, UCRT | llvm-mingw |
| Windows | arm64 | `aarch64-w64-windows-gnu`, UCRT | llvm-mingw |

Apple SDK is never packaged. macOS users need Apple Command Line Tools. Other
toolchain and standard-library dependencies are included in release packs.

## Release contents

Each host install combines exactly two independently verified packs:

- compiler: native `peeper` executable, bundled Peeper libraries, versioned
  `peeper_rt_v1` runtime archive for host target triple, and license;
- toolchain: managed Clang/linker, required sysroot, licenses, and
  `toolchains/native/profile.json`.

Bootstrap install scripts are separate release assets. Signed release manifest
binds each pack ID, version, URL, archive format, byte length, and SHA-256
digest to one host install set. Installer verifies Ed25519 signature before
parsing manifest, downloads only HTTPS assets, rejects unsafe archive paths and
links, stages on destination filesystem, validates managed profile, then
atomically activates or rolls back.

Installer intentionally leaves `PATH` changes to user. Default install root is
`~/.peeper` on Unix-like systems and `%LOCALAPPDATA%\Peeper` on Windows.
Bootstrap install scripts persist the binary directory in the user PATH
idempotently after installation.

## CI and release gates

Normal CI detects compiler, runtime, distribution, toolchain, workflow, and
documentation changes first. Formatting and vet run once on Linux amd64.
Compiler/runtime changes run unit and race tests independently and fan out to
all six native hosts. Distribution, toolchain, and workflow changes run focused
configuration checks. Documentation-only changes skip native hosts.

Tag workflow performs these additional gates:

1. Validate tag, version, public key, finished toolchain lock, and focused
   distribution/toolchain tests in cheap Linux preflight.
2. Fan out to six host jobs in parallel. Each host job builds the compiler,
   fetches and verifies its published immutable toolchain once, builds the
   native runtime, packages one compiler pack, then extracts the pack and
   toolchain into a fresh root and runs `peeper doctor` and source fixtures.
   Host jobs do not build LLVM, musl, or llvm-mingw.
3. Require all six host jobs, then assemble the unsigned manifest and copy
   host packs, native installers, and bootstrap scripts into release assets.
4. One protected finalization job signs the manifest, generates `SHA256SUMS`,
   and creates or updates the draft release.

Build and assembly jobs receive no signing secrets. Only protected `release`
finalization job receives Ed25519 private key.

## Toolchain production and bootstrap

`toolchains/toolchain-sources.lock.json` pins upstream LLVM source and Linux
archives, musl source, and llvm-mingw archives with exact size and SHA-256.
These files are repository workflow configuration, not compiler package data.

`Check toolchain updates` runs each Monday at 03:23 UTC, but scheduled checks
stop after the cheap activity gate when compiler code has had no commit in the
previous 30 days. Manual dispatch bypasses that gate. The checker reads stable
official LLVM and llvm-mingw release metadata, discovers the latest stable musl
tag, validates exact asset names, URLs, sizes, and SHA-256 digests, and opens a
source-lock pull request only when metadata changed. An already-open update pull
request suppresses duplicate proposals. Unchanged musl is not downloaded.

`Build Peeper toolchains` runs on relevant main changes or dispatch. Its planner
maps musl to both Linux outputs, architecture-specific Linux/llvm-mingw sources
to one output, and macOS LLVM source/recipe changes to both macOS outputs.
Before allocating native runners, the planner compares each selected desired
fingerprint with the finished lock and skips identities already selected.
Each selected native job produces `stage/toolchains/native`, generates its
managed `profile.json`, validates it, distpacks it, and publishes/reuses one
immutable prerelease asset tagged by full fingerprint. The job uploads only a
small metadata record to lock fan-in; it never transports its toolchain tree.

Linux toolchains always configure musl with `--disable-shared` for both
architectures, copy compiler-rt builtins, and compile/link/run static smoke
binary. macOS LLVM builds set both `MACOSX_DEPLOYMENT_TARGET` and
`CMAKE_OSX_DEPLOYMENT_TARGET` to Peeper minimum macOS version.

`toolchains/toolchains.lock.json` selects exactly one published immutable
toolchain for each supported host. Toolchain production updates it through a
generated pull request containing real URLs, byte sizes, SHA-256 values, and
component metadata. Release preflight rejects incomplete or invalid selections
before any expensive target job starts. Failed toolchain candidates cannot
replace this lock, so releases continue consuming the last validated toolchain.

## Repository configuration

Set repository variable:

- `PEEPER_RELEASE_PUBLIC_KEY`: 32-byte Ed25519 public key as lowercase hex.

Configure protected `release` environment with:

- `PEEPER_RELEASE_PRIVATE_KEY`: 64-byte Ed25519 private key as lowercase hex.

Public/private key pair must match. Restrict environment approval and secret
access to release maintainers. Keep private key offline outside GitHub secret
copy.

## Creating release candidate

1. Update `pkg/peeper.CompilerVersion` and lockfile intentionally.
2. Run full local validation and merge clean review.
3. Create and push signed tag `v<CompilerVersion>`.
4. Approve protected release environment.
5. Inspect draft assets: six host packs, six native installers, two bootstrap
   scripts, signed manifest, manifest signature, and `SHA256SUMS`. Toolchains
   remain referenced immutable component assets, not duplicate release assets.
6. Verify checksums on downloaded assets.
7. Install on clean host for each supported pair; run `peeper doctor`, then
   compile and run source project with network unavailable.
8. Publish draft only after all checks pass.

Failed platform, signing, manifest, or checksum gate prevents draft creation.
Existing published release is never overwritten by workflow rerun.
