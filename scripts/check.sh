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
# The runtime is no workspace crate, so the two above do not see it.
check "runtime format" rustfmt --edition 2024 --check runtime/peeper_rt.rs
check "runtime clippy" clippy-driver --edition=2024 -Cpanic=abort --crate-type=staticlib \
    --emit=metadata --out-dir target/runtime-clippy -D warnings runtime/peeper_rt.rs
check "tests" cargo test --workspace --no-fail-fast
check "bundle" bash scripts/build.sh
check "fixtures on the bundle" env PEEPER="$PWD/build/bin/peeper" \
    cargo test -p peeper --test fixtures
if [ "$(uname -s)" = Linux ]; then
    check "browser build" cargo build -p browser --profile wasm --target wasm32-wasip1
    check "browser smoke" node crates/driver/browser/smoke.mjs \
        target/wasm32-wasip1/wasm/browser.wasm
fi
check "whitespace" git diff --check

exit $failed
