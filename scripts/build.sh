#!/usr/bin/env bash
# Builds the compiler from source into build/: build/bin/peeper, with the
# bundled libraries copied to build/libs, the LLVM 23 toolchain linked as
# build/toolchains/native and the runtime library built with it into
# build/runtime, where the compiler looks for them (next to its bin folder).
set -euo pipefail
cd "$(dirname "$0")/.."

# The unpacked LLVM 23 release peeper uses; it never uses a clang on the PATH.
llvm="target/llvm-23/LLVM-23.1.4-Linux-X64"
if [ ! -x "$llvm/bin/clang" ]; then
    echo "missing $llvm: unpack the LLVM 23.1.4 release from github.com/llvm/llvm-project there" >&2
    exit 1
fi

cargo build --release
mkdir -p build/bin
rm -f build/bin/peeper # a running peeper (the language server) keeps the old file
cp target/release/peeper build/bin/peeper
rm -rf build/libs
mkdir -p build/libs
cp -R _builtin_library/. build/libs/

mkdir -p build/toolchains
ln -sfn "$PWD/$llvm" build/toolchains/native

rm -rf build/runtime
cargo run --release --quiet -p llvm --bin build-runtime -- build
# `-target i686-linux` needs the 32-bit C library (Fedora: glibc-devel.i686);
# without it only that target is missing, and building for it says so.
if ! cargo run --release --quiet -p llvm --bin build-runtime -- build i686-linux; then
    echo "skipped the i686-linux runtime: install the 32-bit C library to build for -target i686-linux"
fi

echo "built build/bin/peeper with libraries in build/libs, the toolchain in"
echo "build/toolchains/native and the runtime in build/runtime"
