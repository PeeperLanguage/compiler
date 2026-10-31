# TODO

What is done and what is left, so a new session can pick the work up.
Last updated 2026-10-31. The entry-point rule moved into `compile::run`, and
`lsp/src/server.rs` was split by job, after 2026-10-28's `main` = `70f9e8b6`.

## Toolchain packs and distribution

### How it works now

- Peeper uses only its own LLVM toolchain, in `<installation>/toolchains/native`,
  never a `clang` on the PATH.
- A developer runs `scripts/setup.sh` (or `scripts/setup.ps1` on Windows) once.
  It fills `build/toolchains/native` in one of two ways:
  1. It downloads the pack CI built for this machine, pinned in
     `toolchains/toolchains.lock.json`, and checks its size and sha256.
  2. Otherwise it uses an installed LLVM 23 or newer, or installs the upstream
     release pinned in `toolchains/toolchain-sources.lock.json`.
- `crates/tools/toolchain` is the tool behind it, with three commands:
  - `pack` builds and checks a pack, and writes it with the `tar` and `flate2` crates.
  - `lock` writes the pack records into the lock.
  - `update-sources` looks for new upstream releases.
- A pack id reads `toolchain-<os>-<arch>-<family>-<version>-r<hash>`. A pack holds
  `toolchains/native` and the source's license file. The `SOURCES` table names
  that file per source; llvm-mingw's is `LICENSE.txt`.
- `.github/workflows/build-toolchains.yml` packs on `ubuntu-24.04`,
  `ubuntu-24.04-arm`, `macos-15` and `windows-2025`. It publishes each pack as a
  pre-release, and `peeper-bot` opens a PR that updates the lock. After CI passes
  and the owner approves, `auto-merge-toolchain.yml` merges it. Agents never
  approve bot PRs.
- `check-toolchain-updates.yml` runs every Monday at 03:23 UTC. It opens a PR
  when LLVM or llvm-mingw has a new release.

### Done

- [x] Linux x86-64 and ARM64 packs.
- [x] macOS ARM64: `ld64.lld` and the SDK lookup, with CI and pack rows. Merged
  in `387f2c01`.
- [x] Windows x86-64, merged in `70f9e8b6`:
  - [x] CI row `windows-2025`, and `cargo test --no-fail-fast` so every test
        crate runs.
  - [x] Tests link folders through a cfg-gated `link_folder`: a symlink on
        Unix, `symlink_dir` on Windows.
  - [x] `project::path_text` spells source paths with `/` on every system,
        and the path tests work everywhere.
  - [x] `.gitattributes`: `* text=auto eol=lf`.
  - [x] The runtime writes program output in binary mode on Windows.
  - [x] The pack is the whole llvm-mingw release (family `llvm-mingw`).
        `setup.ps1` unpacks it with `System32\tar.exe`.
  - [x] The pack step fails when the pack fails.
  - [x] The lock update was merged as PR #171.
- [x] CI, the pack build and CodeQL all pass on `main`.

### Left

1. [ ] **Windows ARM64 pack.** CI has no row for it (a `shortcut:` note in
   `build-toolchains.yml` says so). The lock still pins the Go-era pack
   `toolchain-windows-arm64-llvm-mingw-20261006-r5a1b9f49047b`, a `.zip` built
   before Windows packs became the whole llvm-mingw release. Windows x86-64
   needed that change to work, so this pack likely fails the same way.
   - Fix: add a `windows-11-arm` row to `build-toolchains.yml`, run the
     workflow, and merge the bot's lock PR. Add the matching CI row too.
2. [ ] **macOS Intel (darwin-amd64).** LLVM publishes no macOS x86-64 release,
   so CI cannot pack it. The lock keeps the Go-era pack
   `toolchain-darwin-amd64-llvm23.1.3-r0f00f3a79817`. It still downloads and has
   the `toolchains/native` layout, but it will never update.
   - Owner decision needed: build LLVM from source for it, rely on an installed
     LLVM there (setup's way 2), or drop Intel Mac support.
3. [x] **A release of the Rust compiler.** `release.yml` builds one on a `v*`
   tag, and `v0.2.0` (2026-10-31) is out with `install.sh`, `install.ps1`, one
   archive per machine and `SHA256SUMS`. `install-check.yml` installs it with
   the README's commands after each release.
4. [ ] **Cleanup, owner approval needed, as it cannot be undone:**
   - About 37 old toolchain pre-releases that the lock no longer pins, under the
     old id formats (`llvm23.1.x-r…`, `…-musl1.2.x-r…`, older llvm-mingw dates).
     Keep every release the lock pins.
   - Stale remote branches from the old automation. Each holds 1 to 3 commits
     that never went into `main`; check each before deleting:
     `automation/toolchain-sources-36559425468`,
     `automation/toolchain-sources-36562350067`,
     `automation/toolchains-33495235059`, `feature/toolchain-update-automation`,
     `fix/release-sbom-directory`, `fix/toolchain-source-lock-migration`,
     `fix/windows-arm64-sbom`.

### Notes

- The scheduled update check failed on 2026-10-26: the old jq script wanted a
  `.sig` asset that LLVM 23.1.4 did not have. `eae9ad6f` replaced that script
  with `toolchain update-sources`, and the manual run on 2026-10-28 passed.
  Check the next Monday run.

## Language and compiler

The next steps of the other work in progress:

- **Slices:** step 1 (`[&]T`, `[&mut]T`, slicing, loops, spreads) is done.
  Step 2, variadic `...T` parameters, waits on the owner's decision about
  `...mut T`.
- **Features:** the allocator, enums and `match` (with `Option` as `?T`), strings,
  methods, interfaces and generics.
- **Ownership:** drop insertion, for containers, the only owners of heap
  memory: there is no owning pointer type, and `*T` stays raw. Also one function in `check` that answers
  "where can this type appear".
- **Types:** destructuring. Stage 2: each module's MIR lists the types it uses,
  and `Types::extend_from` goes away. Stage 3: stable type keys for incremental
  builds.
- **Architecture:**
  - Check and lower bodies in parallel.
  - Per-kind declaration ids.
- **Dependencies:** `[dependencies]` in `peeper.toml` and `peeper.lock`, waiting
  on the owner's design answers.

## Browser (wasm) build

A web page should highlight code, underline its errors and run it.

What was seen on 2026-10-31:
- The website already highlights Peeper with Shiki and
  `lib/peeper.tmLanguage.json`, so highlighting needs no compiler work.
- A reference for the page: the playground in `Ferret-Language/website`
  (`src/components/Playground.svelte`, `src/lib/compiler.ts`, Monaco). Its
  compiler returns `diagnostics` with line and column, but the page never gives
  them to Monaco, so it shows no underlines, only an output panel. It compiles
  on the Run button, on the main thread.

1. [x] **A wasm program that checks text.** `crates/driver/browser` reads the
   page's files from stdin as JSON and writes one entry per diagnostic to
   stdout: severity, code, full message with help, its place (file, start and
   end line and UTF-16 column), and its related places. It targets `wasm32-wasip1` and assumes
   `x86_64-linux`. About 1 MB, 340 KB gzipped; a small program checks in 25-50 ms.
   - Build: `cargo build -p browser --profile wasm --target wasm32-wasip1`, then
     `node crates/driver/browser/smoke.mjs target/wasm32-wasip1/wasm/browser.wasm`.
     CI runs both on Linux.
   - Wasm's stack is small: 1,000 nested parentheses pass, 2,000 crash the
     instance. A 64 MiB wasm stack made Node segfault, cause unknown. Calls at
     compile time stop at 500 deep on wasm (`eval`'s `CALL_LIMIT`), so a
     constant that recurses forever is T0044, as on native.
   - Native `peeper check` overflows its stack on 60,000 nested parentheses.
2. [ ] **The page** (in the Website repo). An editor that highlights with the
   grammar the site already has, runs the check in a Web Worker a short time
   after typing stops, so typing never waits for it, and draws each diagnostic
   as an underline with its message on hover. It needs a small WASI shim, and a
   new instance per check so a crash loses nothing.
3. [ ] **Run code in the browser.** The backend needs clang and LLVM, which do
   not run in a page, so this needs a new backend from MIR to wasm, or a MIR
   interpreter. The runtime must then work in wasm too, printing to the page.

## Website

The site lives in its own repo at `~/Dev/Peeper/Website` (fumadocs, Next.js).
Its `main` has commits, but nothing is pushed and there is no GitHub repo yet;
the docs' edit links assume `PeeperLanguage/website`.

The docs show program outputs as saved text. The plan is to highlight, check and
run code in the browser (see Browser (wasm) build).
