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

Each host install combines exactly three independently verified packs:

- compiler: native `peeper` executable, bundled Peeper libraries, and license;
- target: versioned `peeper_rt_v1` runtime archive for host target triple;
- toolchain: managed Clang/linker, required sysroot, licenses, and
  `toolchains/native/profile.json`.

Native bootstrap is separate release asset. Signed release manifest binds each
pack ID, version, URL, archive format, byte length, and SHA-256 digest to one
host install set. Installer verifies Ed25519 signature before parsing manifest,
downloads only HTTPS assets, rejects unsafe archive paths and links, stages on
destination filesystem, validates managed profile, then atomically activates or
rolls back.

Installer intentionally leaves `PATH` changes to user. Default install root is
`~/.peeper` on Unix-like systems and `%LOCALAPPDATA%\Peeper` on Windows.

## CI and release gates

Normal CI detects compiler, runtime, distribution, toolchain, workflow, and
documentation changes first. Formatting and vet run once on Linux amd64.
Compiler/runtime changes run unit and race tests independently and fan out to
all six native hosts. Distribution, toolchain, and workflow changes run focused
configuration checks. Documentation-only changes skip native hosts.

Tag workflow performs these additional gates:

1. Validate tag, version, public key, finished toolchain lock, and focused
   distribution/toolchain tests in cheap Linux preflight.
2. Fan out to six compiler jobs and six runtime jobs in parallel. Runtime jobs
   download, size-check, SHA-256-check, and securely extract published
   toolchain components; they do not build LLVM, musl, or llvm-mingw.
3. Each target-specific verify job composes fresh compiler, target, and
   toolchain packs, runs doctor and source fixtures, and generates its SPDX SBOM.
4. Require all six verify jobs, then assemble, sign manifest, generate
   `SHA256SUMS`, attest provenance, and upload draft release.

Build jobs receive no signing secrets. Only protected `release` signing job
receives Ed25519 private key.

## Toolchain production and bootstrap

`pkg/distribution/toolchain-sources.lock.json` pins upstream LLVM source and Linux
archives, musl source, and llvm-mingw archives with exact size and SHA-256.
`Build Peeper toolchains` runs on relevant main changes or dispatch. Its planner
maps musl to both Linux outputs, architecture-specific Linux/llvm-mingw sources
to one output, and macOS LLVM source/recipe changes to both macOS outputs.
Each selected native job produces `stage/toolchains/native`, generates its
managed `profile.json`, validates it, distpacks it, and publishes/reuses one
immutable prerelease asset tagged by full fingerprint. The job uploads only a
small metadata record to lock fan-in; it never transports its toolchain tree.

Linux toolchains always configure musl with `--disable-shared` for both
architectures, copy compiler-rt builtins, and compile/link/run static smoke
binary. macOS LLVM builds set both `MACOSX_DEPLOYMENT_TARGET` and
`CMAKE_OSX_DEPLOYMENT_TARGET` to Peeper minimum macOS version.

`pkg/distribution/toolchains.lock.json` begins empty and is intentionally not a
release fallback. Initial bootstrap must run all six producer targets, verify
their published immutable assets, and merge the generated lock pull request.
Only real URL, byte size, SHA-256, and component metadata enter that lock.
Until then release preflight fails before any expensive target job starts.

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
5. Inspect draft assets: six bootstraps, 12 Peeper compiler/target packs, six
   SBOMs, signed manifest, checksums, and provenance attestation. Toolchains
   remain referenced immutable component assets, not duplicate release assets.
6. Verify checksums and `gh attestation verify` on downloaded assets.
7. Install on clean host for each supported pair; run `peeper doctor`, then
   compile and run source project with network unavailable.
8. Publish draft only after all checks pass.

Failed platform, signing, SBOM, manifest, or attestation gate
prevents draft creation. Existing published release is never overwritten by
workflow rerun.
