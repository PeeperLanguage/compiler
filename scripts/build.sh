#!/usr/bin/env bash
# Builds the compiler from source into build/: build/bin/peeper, with the
# bundled libraries copied to build/libs and the runtime library built into
# build/runtime with the toolchain scripts/setup.sh puts in
# build/toolchains/native, where the compiler looks for them (next to its bin
# folder).
set -euo pipefail
cd "$(dirname "$0")/.."

case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN*) exe=.exe ;; # Git Bash on Windows
    *) exe= ;;
esac

# Peeper runs only this toolchain, never a clang on the PATH.
if [ ! -x "build/toolchains/native/bin/clang$exe" ]; then
    echo "no toolchain in build/toolchains/native: run scripts/setup.sh first" >&2
    exit 1
fi

cargo build --release
mkdir -p build/bin
rm -f "build/bin/peeper$exe" # a running peeper (the language server) keeps the old file
cp "target/release/peeper$exe" "build/bin/peeper$exe"
rm -rf build/libs
mkdir -p build/libs
cp -R _builtin_library/. build/libs/

rm -rf build/runtime
cargo run --release --quiet -p llvm --bin build-runtime -- build
# `-target i686-linux` needs the 32-bit C library (Fedora: glibc-devel.i686);
# without it only that target is missing, and building for it says so.
if ! cargo run --release --quiet -p llvm --bin build-runtime -- build i686-linux; then
    echo "skipped the i686-linux runtime: install the 32-bit C library to build for -target i686-linux"
fi

echo "built build/bin/peeper with libraries in build/libs, the toolchain in"
echo "build/toolchains/native and the runtime in build/runtime"
