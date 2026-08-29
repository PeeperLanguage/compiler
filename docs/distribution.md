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

Normal CI uses six native GitHub-hosted runners. Every host runs Go tests,
builds compiler through `bash scripts/build.sh`, runs `peeper doctor`, and
executes source fixtures. Race detector remains Linux amd64 gate.

Tag workflow performs these additional gates:

1. Build pinned LLVM/musl/llvm-mingw inputs from
   `distribution/toolchains.lock.json` with exact size and SHA-256 checks.
2. Compile target runtime with managed toolchain and validate complete staged
   install on native runner.
3. Sign every distributed Mach-O or PE executable before packing. macOS payload
   and bootstrap also pass Apple notarization.
4. Produce deterministic packs and per-host SPDX JSON SBOMs.
5. Require all 18 packs, sign release manifest, generate `SHA256SUMS`, and
   create GitHub provenance attestation.
6. Create or update draft release only. Human publishes after clean-host review.

Build jobs receive no signing secrets. Signing and publication jobs use separate
protected GitHub environments.

## Repository configuration

Set repository variable:

- `PEEPER_RELEASE_PUBLIC_KEY`: 32-byte Ed25519 public key as lowercase hex.

Configure protected `release-signing` environment with:

- `MACOS_CERTIFICATE_P12`: base64 Developer ID Application PKCS#12;
- `MACOS_CERTIFICATE_PASSWORD`;
- `MACOS_SIGNING_IDENTITY`;
- `MACOS_NOTARY_APPLE_ID`;
- `MACOS_NOTARY_TEAM_ID`;
- `MACOS_NOTARY_PASSWORD`: app-specific password;
- `WINDOWS_CERTIFICATE_PFX`: base64 Authenticode PKCS#12;
- `WINDOWS_CERTIFICATE_PASSWORD`.

Configure protected `release` environment with:

- `PEEPER_RELEASE_PRIVATE_KEY`: 64-byte Ed25519 private key as lowercase hex.

Public/private key pair must match. Restrict environment approval and secret
access to release maintainers. Keep private key offline outside GitHub secret
copy.

## Creating release candidate

1. Update `pkg/peeper.CompilerVersion` and lockfile intentionally.
2. Run full local validation and merge clean review.
3. Create and push signed tag `v<CompilerVersion>`.
4. Approve protected signing and release environments.
5. Inspect draft assets: six bootstraps, 18 packs, six SBOMs, signed manifest,
   checksums, and provenance attestation.
6. Verify checksums and `gh attestation verify` on downloaded assets.
7. Install on clean host for each supported pair; run `peeper doctor`, then
   compile and run source project with network unavailable.
8. Publish draft only after all checks pass.

Failed platform, signing, notarization, SBOM, manifest, or attestation gate
prevents draft creation. Existing published release is never overwritten by
workflow rerun.
