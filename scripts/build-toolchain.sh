#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  echo "usage: build-toolchain.sh <os> <arch> <llvm-target> <output-root> [minimum-macos]" >&2
  exit 2
fi

release_os="$1"
release_arch="$2"
llvm_target="$3"
output_root="$4"
minimum_macos="${5:-13.0}"
lock_file="${TOOLCHAIN_SOURCES_LOCK:-distribution/toolchain-sources.lock.json}"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

asset_field() {
  jq -er --arg id "$1" --arg field "$2" '.assets[] | select(.id == $id) | .[$field]' "$lock_file"
}

download_asset() {
  local id="$1" destination="$2"
  local url size sha256 actual_sha256
  url="$(asset_field "$id" url)"
  size="$(asset_field "$id" size)"
  sha256="$(asset_field "$id" sha256)"
  curl --fail --location --retry 3 --output "$destination" "$url"
  test "$(wc -c < "$destination")" -eq "$size"
  if command -v shasum >/dev/null 2>&1; then
    actual_sha256="$(shasum -a 256 "$destination" | awk '{print $1}')"
  else
    actual_sha256="$(sha256sum "$destination" | awk '{print $1}')"
  fi
  test "$actual_sha256" = "$sha256"
}

mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd)"

case "$release_os/$release_arch" in
  linux/amd64) llvm_id=llvm-linux-amd64 ;;
  linux/arm64) llvm_id=llvm-linux-arm64 ;;
  darwin/amd64|darwin/arm64) llvm_id=llvm-source ;;
  windows/amd64) llvm_id=llvm-mingw-windows-amd64 ;;
  windows/arm64) llvm_id=llvm-mingw-windows-arm64 ;;
  *) echo "unsupported target $release_os/$release_arch" >&2; exit 2 ;;
esac

case "$release_os" in
  linux)
    triple="$(printf '%s' "$release_arch" | sed -e 's/^amd64$/x86_64/' -e 's/^arm64$/aarch64/')-unknown-linux-musl"
    download_asset "$llvm_id" "$tmp_root/llvm.tar.xz"
    tar -xJf "$tmp_root/llvm.tar.xz" -C "$output_root" --strip-components=1
    download_asset musl-source "$tmp_root/musl.tar.gz"
    mkdir -p "$tmp_root/musl-source"
    tar -xzf "$tmp_root/musl.tar.gz" -C "$tmp_root/musl-source" --strip-components=1
    clang="$output_root/bin/clang"
    archiver="$output_root/bin/llvm-ar"
    ranlib="$output_root/bin/llvm-ranlib"
    (
      cd "$tmp_root/musl-source"
      CC="$clang --target=$triple" AR="$archiver" RANLIB="$ranlib" ./configure --prefix=/ --target="$triple" --disable-shared
      make -j2
      make DESTDIR="$output_root/sysroot" install
    )
    mkdir -p "$output_root/licenses"
    cp "$tmp_root/musl-source/COPYRIGHT" "$output_root/licenses/musl-COPYRIGHT"
    rm -rf "$output_root/include" "$output_root/share" "$output_root/lib/cmake"
    find "$output_root/lib" -maxdepth 1 -type f -name '*.a' -delete

    sysroot="$output_root/sysroot"
    for object in libc.a crt1.o crti.o crtn.o; do
      test -f "$sysroot/lib/$object"
    done
    compiler_runtime="$("$clang" -rtlib=compiler-rt --print-libgcc-file-name)"
    test -f "$compiler_runtime"
    cp "$compiler_runtime" "$sysroot/lib/libclang_rt.builtins.a"
    smoke="$tmp_root/smoke.c"
    printf '%s\n' '#include <stdio.h>' 'int main(void) { return printf("%Lf\n", (long double)1.0) < 0; }' > "$smoke"
    "$clang" --target="$triple" --sysroot="$sysroot" -c "$smoke" -o "$tmp_root/smoke.o"
    "$clang" --target="$triple" --sysroot="$sysroot" -static -nostdlib \
      "$sysroot/lib/crt1.o" "$sysroot/lib/crti.o" "$tmp_root/smoke.o" \
      -lc -lclang_rt.builtins "$sysroot/lib/crtn.o" -o "$tmp_root/smoke"
    "$tmp_root/smoke"
    ;;
  darwin)
    export MACOSX_DEPLOYMENT_TARGET="$minimum_macos"
    download_asset "$llvm_id" "$tmp_root/llvm-source.tar.xz"
    mkdir -p "$tmp_root/llvm-source"
    tar -xJf "$tmp_root/llvm-source.tar.xz" -C "$tmp_root/llvm-source" --strip-components=1
    cmake -G Ninja \
      -S "$tmp_root/llvm-source/llvm" \
      -B "$tmp_root/llvm-build" \
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
    cmake --build "$tmp_root/llvm-build" --target install -j 2
    mkdir -p "$output_root/licenses"
    cp "$tmp_root/llvm-source/LICENSE.TXT" "$output_root/licenses/LLVM-LICENSE.txt"
    rm -rf "$output_root/include" "$output_root/share" "$output_root/lib/cmake"
    find "$output_root/lib" -maxdepth 1 -type f -name '*.a' -delete
    ;;
  windows)
    download_asset "$llvm_id" "$tmp_root/llvm-mingw.zip"
    mkdir -p "$tmp_root/llvm-mingw"
    unzip -q "$tmp_root/llvm-mingw.zip" -d "$tmp_root/llvm-mingw"
    extracted="$(find "$tmp_root/llvm-mingw" -mindepth 1 -maxdepth 1 -type d -print -quit)"
    test -n "$extracted"
    cp -R "$extracted"/. "$output_root"/
    mkdir -p "$output_root/licenses"
    cp "$extracted/LICENSE.TXT" "$output_root/licenses/LLVM-MINGW-LICENSE.txt"
    rm -rf "$output_root/share"
    ;;
esac

case "$release_os" in
  windows)
    suffix=.exe
    test -x "$output_root/bin/clang$suffix"
    test -x "$output_root/bin/llvm-ar$suffix"
    test -x "$output_root/bin/llvm-ranlib$suffix"
    find "$output_root/bin" -maxdepth 1 -type f \( -name 'ld.lld.exe' -o -name 'lld-link.exe' \) -print -quit | grep -q .
    ;;
  darwin)
    test -x "$output_root/bin/clang"
    test -x "$output_root/bin/llvm-ar"
    test -x "$output_root/bin/llvm-ranlib"
    find "$output_root/bin" -maxdepth 1 -type f \( -name 'ld.lld' -o -name 'ld64.lld' -o -name 'lld' \) -print -quit | grep -q .
    ;;
  linux)
    test -x "$output_root/bin/clang"
    test -x "$output_root/bin/llvm-ar"
    test -x "$output_root/bin/llvm-ranlib"
    test -x "$output_root/bin/ld.lld"
    ;;
esac

"$output_root/bin/clang${suffix:-}" --version
