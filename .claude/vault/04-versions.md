# Versions and environment

Recorded 2026-11-01 from the files named; re-check them before relying on a
number.

## Rust

- No `rust-toolchain` file: CI uses the runner's stable Rust plus
  `rustfmt` and `clippy` (`.github/workflows/ci.yml`). The work logged in
  `07-worklog.md` was built with `rustc 1.97.0`.
- Workspace (`Cargo.toml`): `edition = "2024"`, `resolver = "3"`, version
  `0.2.0`, `unsafe_code = "forbid"`, clippy `all` plus the `rust-style.md`
  lints, all denied by `-D warnings`. Profiles: `release` with
  line tables; `wasm` (stripped, for the browser build).
- Extra Rust targets: `i686-unknown-linux-gnu` (the `i686-linux` runtime
  and fixtures), `wasm32-wasip1` (browser build), and on Windows
  `x86_64-pc-windows-gnullvm` (`ci.yml`).

## Dependencies (`Cargo.lock`)

`unicode-general-category 1.1.0`, `stacker 0.1.25`, `lsp-server 0.7.9`,
`lsp-types 0.95.1`, `serde 1.0.229`, `serde_json 1.0.154`, `sha2 0.11.1`,
`tar 0.4.46`, `flate2 1.1.10`, `toml_edit 0.25.13`, `rustc-hash 2.1.3`.
Only `lsp` uses `lsp-server`/`lsp-types`; `toolchain` uses the archive and
hash crates.

## LLVM toolchain

- LLVM **23.1.4** pinned in `toolchains/toolchain-sources.lock.json`; packs
  per OS and arch in `toolchains/toolchains.lock.json` (one darwin-amd64 pack
  is still `llvm23.1.3`).
- LLVM 23 or newer is required: LLVM 22 crashes on float to wide integer
  casts (`scripts/setup.sh`, `docs/ir-design.md`).
- `scripts/setup.sh` puts the pack in `build/toolchains/native`; the compiler
  runs only that clang and `ld.lld`.

## Targets (`crates/middle/model/src/machine.rs`, `MACHINES`)

`x86_64-linux`, `aarch64-linux`, `i686-linux` (32-bit pointers),
`x86_64-macos`, `aarch64-macos`, `x86_64-windows`, `aarch64-windows`. CI runs
the tests on `x86_64-linux`, `aarch64-linux`, `aarch64-macos` and
`x86_64-windows`; `i686-linux` runs only on x86-64 Linux and needs the 32-bit
C library (Ubuntu `gcc-multilib`).

## CI (`.github/workflows/ci.yml`)

Matrix `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`, `windows-2025`; each runs
`scripts/setup.sh` (or `setup.ps1`), then `bash scripts/check.sh`. Other
workflows: toolchain builds and updates, install check, release, and the
`self-review-marker` check.

## Validation commands

| Command | What it proves |
| --- | --- |
| `bash scripts/check.sh` | the CI gate: fmt, clippy, runtime fmt and clippy, all tests, bundle, fixtures on the bundle, browser build and smoke, `git diff --check` |
| `bash scripts/setup.sh` | the toolchain is present (needed by bundle and fixtures) |
| `bash scripts/build.sh` | `build/bin/peeper` with libraries and runtimes |
| `scripts/bench-instructions.py` | instructions of one compile of the benchmark; needs `perf_event_open`. Without it, run the same benchmark under `valgrind --tool=callgrind` (runs=2 minus runs=1) |
