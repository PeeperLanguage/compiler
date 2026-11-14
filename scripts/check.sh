#!/usr/bin/env bash
# All CI checks, here and in CI, which runs this. It goes on past a failure, so
# one run shows all of them. A step prints one line; a failed step also prints
# all it wrote. Exits with 1 when any step failed.
set -uo pipefail
cd "$(dirname "$0")/.."

failed=0

check() {
    local name=$1
    shift
    local output
    if output=$("$@" 2>&1); then
        echo "ok      $name"
    else
        echo "FAILED  $name"
        printf '%s\n' "$output"
        failed=1
    fi
}

check "format" cargo fmt --check
check "clippy" cargo clippy --workspace --all-targets -- -D warnings
# The runtime is no workspace crate, so the two above do not see it. Its
# root file, edition and panic flag copy `source` and `flags` in
# runtime/runtime.toml; change them together.
check "runtime format" rustfmt --edition 2024 --check runtime/peeper_rt.rs
check "runtime clippy" clippy-driver --edition=2024 -Cpanic=abort --crate-type=staticlib \
    --emit=metadata --out-dir target/runtime-clippy -D warnings runtime/peeper_rt.rs
# The phases after lowering read only the MIR of core concepts
# (docs/feature-front.md section 8): `model`'s core files name no front
# module, and eval, flow and llvm name only those. llvm's tests load and
# compile a whole program, so they may.
# shortcut: this is a grep; replace it with the crate split of section 9,
# which Cargo checks.
front='semantic_types|thir|program|layout'
check "middle boundary" bash -c "
    ! grep -rnE 'crate::($front)' crates/middle/model/src/{mir,machine,constant}.rs &&
    ! grep -nE '^use crate::' crates/middle/model/src/lib.rs &&
    ! grep -rnE 'model::($front)' crates/middle/eval crates/middle/flow &&
    ! grep -rnE --exclude=tests.rs 'model::($front)' crates/backend/llvm &&
    ! grep -rnE '\bsyntax::' crates/middle/model/src/{mir,machine,constant,lib}.rs \
        crates/middle/{eval,flow} crates/backend/llvm"
# A phase names no feature: only its tests may use the `features` crate
# (docs/feature-front.md section 7); Cargo refuses the cycle for the crates
# `features` depends on, this the others. And a feature's file names no
# other feature: of its own crate, only the list's `Peeper` and enums, so
# no module's path (`crate::bools`).
check "feature boundary" bash -c "
    for manifest in crates/{base,core,frontend,middle,backend}/*/Cargo.toml; do
        sed '/^\[dev-dependencies\]/,\$d' \$manifest | grep -H --label=\$manifest '^features\b' && exit 1
    done
    ! grep -nE '\bcrate::[a-z_]' crates/features/src/*.rs |
        grep -vE '^crates/features/src/(lib|testing)\.rs:'"
check "tests" cargo test --workspace --no-fail-fast
check "bundle" bash scripts/build.sh
check "test programs on the bundle" env PEEPER="$PWD/build/bin/peeper" \
    cargo test -p peeper --test test_programs
# Nothing else reads the examples, so a language change would leave them stale.
check "examples" bash -c '
    for example in examples/*.peep examples/modules; do
        build/bin/peeper check "$example" || exit 1
    done'
if [ "$(uname -s)" = Linux ]; then
    # shortcut: run on Linux only, where its `lli` (calling printf from the
    # JIT) was tried; move it out of this block once CI shows it on the others.
    check "llvm parity" cargo test -p check --test llvm_parity -- --ignored
    check "browser build" cargo build -p browser --profile wasm --target wasm32-wasip1
    check "browser smoke" node crates/driver/browser/smoke.mjs \
        target/wasm32-wasip1/wasm/browser.wasm
fi
check "whitespace" git diff --check

exit $failed
