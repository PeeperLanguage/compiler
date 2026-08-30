#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: build-linux.sh <arch> <output-root>" >&2
  exit 2
fi

release_arch="$1"
output_root="$2"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/common.sh"

case "$release_arch" in
  amd64) llvm_id=llvm-linux-amd64; triple=x86_64-unknown-linux-musl ;;
  arm64) llvm_id=llvm-linux-arm64; triple=aarch64-unknown-linux-musl ;;
  *) echo "unsupported Linux architecture $release_arch" >&2; exit 2 ;;
esac

temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT
mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd)"

toolchain_download_asset "$llvm_id" "$temporary_root/llvm.tar.xz"
tar -xJf "$temporary_root/llvm.tar.xz" -C "$output_root" --strip-components=1
toolchain_download_asset musl-source "$temporary_root/musl.tar.gz"
mkdir -p "$temporary_root/musl-source"
tar -xzf "$temporary_root/musl.tar.gz" -C "$temporary_root/musl-source" --strip-components=1

clang="$output_root/bin/clang"
archiver="$output_root/bin/llvm-ar"
ranlib="$output_root/bin/llvm-ranlib"
(
  cd "$temporary_root/musl-source"
  CC="$clang --target=$triple" AR="$archiver" RANLIB="$ranlib" ./configure \
    --prefix=/ \
    --target="$triple" \
    --disable-shared
  make -j2
  make DESTDIR="$output_root/sysroot" install
)

mkdir -p "$output_root/licenses"
cp "$temporary_root/musl-source/COPYRIGHT" "$output_root/licenses/musl-COPYRIGHT"
rm -rf "$output_root/include" "$output_root/share" "$output_root/lib/cmake"
find "$output_root/lib" -maxdepth 1 -type f -name '*.a' -delete

sysroot="$output_root/sysroot"
for object in libc.a crt1.o crti.o crtn.o; do
  test -f "$sysroot/lib/$object"
done
compiler_runtime="$("$clang" -rtlib=compiler-rt --print-libgcc-file-name)"
test -f "$compiler_runtime"
cp "$compiler_runtime" "$sysroot/lib/libclang_rt.builtins.a"

smoke="$temporary_root/smoke.c"
printf '%s\n' '#include <stdio.h>' 'int main(void) { return printf("%Lf\n", (long double)1.0) < 0; }' > "$smoke"
"$clang" --target="$triple" --sysroot="$sysroot" -c "$smoke" -o "$temporary_root/smoke.o"
"$clang" --target="$triple" --sysroot="$sysroot" -static -nostdlib \
  "$sysroot/lib/crt1.o" "$sysroot/lib/crti.o" "$temporary_root/smoke.o" \
  -lc -lclang_rt.builtins "$sysroot/lib/crtn.o" -o "$temporary_root/smoke"
"$temporary_root/smoke"

test -x "$clang"
test -x "$archiver"
test -x "$ranlib"
test -x "$output_root/bin/ld.lld"
"$clang" --version
