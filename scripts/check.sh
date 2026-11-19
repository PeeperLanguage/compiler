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
# The phases after lowering read only MIR (docs/compiler-layout.md, rule
# 6): `model`'s MIR files name none of the modules before MIR, and eval,
# flow and llvm name only the MIR files. llvm's tests load and compile a
# whole program, so they may.
# shortcut: this is a grep; a crate of its own for MIR would let Cargo
# check it.
before_mir='semantic_types|thir|program|layout'
check "MIR readers read only MIR" bash -c "
    ! grep -rnE 'crate::($before_mir)' crates/model/src/{mir,machine,constant}.rs &&
    ! grep -nE '^use crate::' crates/model/src/lib.rs &&
    ! grep -rnE 'model::($before_mir)' crates/eval crates/flow &&
    ! grep -rnE --exclude=tests.rs 'model::($before_mir)' crates/backend/llvm &&
    ! grep -rnE '\btree::' crates/model/src/{mir,machine,constant,lib}.rs \
        crates/{eval,flow} crates/backend/llvm"
# Rust skips a source file no `mod` line names, so a concept file left out
# of its crate's list builds clean and does nothing.
unlisted_files() {
    local file dir stem parent found=0
    for file in $(find crates -path '*/src/*' -name '*.rs' ! -path '*/src/bin/*' \
        ! -name lib.rs ! -name main.rs); do
        dir=${file%/*} stem=${file##*/} stem=${stem%.rs}
        if [ "$stem" = mod ]; then stem=${dir##*/} dir=${dir%/*}; fi
        if [ "${dir##*/}" = src ]; then parent="$dir/lib.rs $dir/main.rs"
        else parent="$dir.rs $dir/mod.rs"; fi
        # shellcheck disable=SC2086
        parent=$(ls $parent 2>/dev/null)
        # A `mod` line, not one in a comment: `// mod x;` names nothing.
        [ -n "$parent" ] && grep -qE "^\s*(pub(\([a-z ]+\))? )?mod $stem;" $parent ||
            { echo "$file: no \`mod $stem;\` names it"; found=1; }
    done
    return $found
}
check "every source file in a module" unlisted_files
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
