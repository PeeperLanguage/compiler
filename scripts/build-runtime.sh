#!/usr/bin/env bash
# Builds the runtime library, libpeeper_rt_v1.a, from runtime/peeper_rt.c into
# the folder given, where the compiler looks for it (<root>/runtime beside its
# bin folder). Used by scripts/build.sh and the fixture test's bundle.
set -euo pipefail
out=$1
checkout="$(dirname "$0")/.."

mkdir -p "$out"
clang -std=c11 -O2 -c "$checkout/runtime/peeper_rt.c" -o "$out/peeper_rt.o"
archiver=$(command -v llvm-ar || command -v ar)
"$archiver" rcs "$out/libpeeper_rt_v1.a" "$out/peeper_rt.o"
rm "$out/peeper_rt.o"
