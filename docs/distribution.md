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

Normal CI detects change categories first. Formatting and vet run once on Linux
amd64, unit and race tests run independently, and non-documentation changes fan
out to all six native hosts. Each native host builds compiler through
`bash scripts/build.sh`, runs `peeper doctor`, and executes source fixtures with
that compiler. Documentation-only changes skip native matrix.

Tag workflow performs these additional gates:

1. Validate tag, version, public key, source lock, required source IDs, duplicate
   IDs, and focused distribution/toolchain tests in cheap Linux preflight.
2. Fan out to six independent target flows. Each builds Peeper, obtains verified
   pinned toolchain inputs, builds runtime/profile, runs doctor and source
   fixtures, generates SPDX SBOM, and creates deterministic final packs on same
   native runner.
3. Require all six target flows, sign release manifest, generate `SHA256SUMS`,
   create GitHub provenance attestation, and upload draft release.

Build jobs receive no signing secrets. Only protected `release` publication job
receives Ed25519 private key.

## Toolchain production and bootstrap

`distribution/toolchain-sources.lock.json` pins upstream LLVM source and Linux
archives, musl source, and llvm-mingw archives with exact size and SHA-256.
Dispatch-only `Build Peeper toolchains` workflow can manufacture `linux`,
`darwin`, or `windows` family independently. Linux toolchains always configure
musl with `--disable-shared` for both architectures and compile/link/run static
smoke binary. macOS LLVM builds set both `MACOSX_DEPLOYMENT_TARGET` and
`CMAKE_OSX_DEPLOYMENT_TARGET` to Peeper minimum macOS version.

No immutable Peeper-produced toolchain assets are published yet. Producer
uploads short-lived workflow artifacts for validation only, while release target
uses same canonical producer script as functional bootstrap. Complete migration:

1. Dispatch producer for each affected family and inspect validation results.
2. Publish validated toolchain archives as immutable repository GitHub Release
   assets under dedicated toolchain release/tag policy.
3. Record real asset URLs, byte sizes, SHA-256 values, versions, and licenses in
   new `distribution/toolchains.lock.json`.
4. Change release target to download and verify finished assets from that lock.
5. Remove release-side source-build fallback only after all six finished assets
   exist and one release candidate passes.

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
5. Inspect draft assets: six bootstraps, 18 packs, six SBOMs, signed manifest,
   checksums, and provenance attestation.
6. Verify checksums and `gh attestation verify` on downloaded assets.
7. Install on clean host for each supported pair; run `peeper doctor`, then
   compile and run source project with network unavailable.
8. Publish draft only after all checks pass.

Failed platform, signing, SBOM, manifest, or attestation gate
prevents draft creation. Existing published release is never overwritten by
workflow rerun.
