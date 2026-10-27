#!/usr/bin/env bash
# Builds the compiler from source into build/: build/bin/peeper, with the
# bundled libraries copied to build/libs and the runtime library built into
# build/runtime, where the compiler looks for them (next to its bin folder).
set -euo pipefail
cd "$(dirname "$0")/.."

cargo build --release
mkdir -p build/bin
cp target/release/peeper build/bin/peeper
rm -rf build/libs
mkdir -p build/libs
cp -R _builtin_library/. build/libs/

rm -rf build/runtime
cargo run --release --quiet -p llvm --bin build-runtime -- build/runtime
# `-target i686-linux` needs the 32-bit C library (Fedora: glibc-devel.i686);
# without it only that target is missing, and building for it says so.
if ! cargo run --release --quiet -p llvm --bin build-runtime -- build/runtime i686-linux; then
    echo "skipped the i686-linux runtime: install the 32-bit C library to build for -target i686-linux"
fi

echo "built build/bin/peeper with libraries in build/libs and the runtime in build/runtime"
