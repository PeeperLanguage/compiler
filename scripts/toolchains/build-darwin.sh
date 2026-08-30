#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: build-darwin.sh <arch> <llvm-target> <output-root> <minimum-macos>" >&2
  exit 2
fi

release_arch="$1"
llvm_target="$2"
output_root="$3"
minimum_macos="$4"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/common.sh"

case "$release_arch/$llvm_target" in
  amd64/X86|arm64/AArch64) ;;
  *) echo "unsupported macOS architecture/backend $release_arch/$llvm_target" >&2; exit 2 ;;
esac

temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT
mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd)"
export MACOSX_DEPLOYMENT_TARGET="$minimum_macos"

toolchain_download_asset llvm-source "$temporary_root/llvm-source.tar.xz"
mkdir -p "$temporary_root/llvm-source"
tar -xJf "$temporary_root/llvm-source.tar.xz" -C "$temporary_root/llvm-source" --strip-components=1
cmake -G Ninja \
  -S "$temporary_root/llvm-source/llvm" \
  -B "$temporary_root/llvm-build" \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$output_root" \
  '-DLLVM_ENABLE_PROJECTS=clang;lld' \
  "-DLLVM_TARGETS_TO_BUILD=$llvm_target" \
  -DLLVM_INCLUDE_TESTS=OFF \
  -DLLVM_INCLUDE_EXAMPLES=OFF \
  -DLLVM_INCLUDE_BENCHMARKS=OFF \
  -DLLVM_ENABLE_TERMINFO=OFF \
  -DLLVM_ENABLE_ZLIB=OFF \
  -DLLVM_ENABLE_ZSTD=OFF \
  "-DCMAKE_OSX_DEPLOYMENT_TARGET=$minimum_macos"
cmake --build "$temporary_root/llvm-build" --target install -j 2

mkdir -p "$output_root/licenses"
cp "$temporary_root/llvm-source/LICENSE.TXT" "$output_root/licenses/LLVM-LICENSE.txt"
rm -rf "$output_root/include" "$output_root/share" "$output_root/lib/cmake"
find "$output_root/lib" -maxdepth 1 -type f -name '*.a' -delete

test -x "$output_root/bin/clang"
test -x "$output_root/bin/llvm-ar"
test -x "$output_root/bin/llvm-ranlib"
find "$output_root/bin" -maxdepth 1 -type f \( -name 'ld.lld' -o -name 'ld64.lld' -o -name 'lld' \) -print -quit | grep -q .
"$output_root/bin/clang" --version
