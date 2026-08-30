#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -eq 2 ]; then
  mode=build
  release_arch="$1"
  output_root="$2"
elif [ "$#" -eq 3 ] && [ "$1" = validate ]; then
  mode=validate
  release_arch="$2"
  output_root="$3"
else
  echo "usage: build-linux.sh [validate] <arch> <toolchain-root>" >&2
  exit 2
fi

script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/common.sh"

case "$release_arch" in
  amd64) triple=x86_64-unknown-linux-musl ;;
  arm64) triple=aarch64-unknown-linux-musl ;;
  *) echo "unsupported Linux architecture $release_arch" >&2; exit 2 ;;
esac

mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd)"

validate_linux_toolchain() {
  local root="$1"
  local clang="$root/bin/clang"
  local linker="$root/bin/ld.lld"
  local archiver="$root/bin/llvm-ar"
  local sysroot="$root/sysroot"
  local resource_dir validation_root smoke ldd_output llvm_dependencies

  for tool in "$clang" "$linker" "$archiver"; do
    test -x "$tool" || { echo "required managed Linux tool missing: $tool" >&2; return 1; }
  done
  for object in libc.a crt1.o crti.o crtn.o libclang_rt.builtins.a; do
    test -f "$sysroot/lib/$object" || { echo "required static sysroot file missing: $sysroot/lib/$object" >&2; return 1; }
  done

  resource_dir="$(env -i PATH="$root/bin" "$clang" -print-resource-dir)"
  case "$resource_dir" in
    "$root"/*) ;;
    *) echo "clang resource directory escapes managed toolchain: $resource_dir" >&2; return 1 ;;
  esac
  test -d "$resource_dir/include" || { echo "clang resource headers missing: $resource_dir/include" >&2; return 1; }

  command -v readelf >/dev/null 2>&1 || { echo "readelf is required to validate managed Linux tools" >&2; return 1; }
  command -v ldd >/dev/null 2>&1 || { echo "ldd is required to validate managed Linux tools" >&2; return 1; }
  for tool in "$clang" "$linker" "$archiver"; do
    readelf -h "$tool" >/dev/null
    readelf -d "$tool" 2>/dev/null | grep -E '\((NEEDED|RPATH|RUNPATH)\)' || true
    if grep -q 'Requesting program interpreter' < <(readelf -l "$tool"); then
      ldd_output="$(ldd "$tool")"
      printf '%s\n' "$ldd_output"
      if grep -q 'not found' <<<"$ldd_output"; then
        echo "managed Linux tool has unresolved shared-library dependency: $tool" >&2
        return 1
      fi
      llvm_dependencies="$(grep -E '^[[:space:]]*lib(LLVM|clang|lld)' <<<"$ldd_output" || true)"
      if [ -n "$llvm_dependencies" ] && grep -vF "=> $root/lib/" <<<"$llvm_dependencies"; then
        echo "managed Linux tool resolved LLVM dependency outside its component: $tool" >&2
        return 1
      fi
    fi
  done

  env -i PATH="$root/bin" "$clang" --version
  env -i PATH="$root/bin" "$linker" --version
  env -i PATH="$root/bin" "$archiver" --version

  validation_root="$(mktemp -d)"
  smoke="$validation_root/smoke.c"
  printf '%s\n' '#include <stdio.h>' 'int main(void) { return printf("%Lf\n", (long double)1.0) < 0; }' > "$smoke"
  env -i PATH="$root/bin" "$clang" \
    --target="$triple" \
    --sysroot="$sysroot" \
    -c "$smoke" \
    -o "$validation_root/smoke.o"
  env -i PATH="$root/bin" "$clang" \
    --target="$triple" \
    --sysroot="$sysroot" \
    -B"$root/bin" \
    -fuse-ld=lld \
    -static \
    -nostdlib \
    "$sysroot/lib/crt1.o" \
    "$sysroot/lib/crti.o" \
    "$validation_root/smoke.o" \
    -lc \
    -lclang_rt.builtins \
    "$sysroot/lib/crtn.o" \
    -o "$validation_root/smoke"
  "$validation_root/smoke"
  rm -rf "$validation_root"
}

if [ "$mode" = validate ]; then
  validate_linux_toolchain "$output_root"
  exit 0
fi

if [ -n "$(find "$output_root" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  echo "managed Linux toolchain output root is not empty: $output_root" >&2
  exit 1
fi
case "$release_arch" in
  amd64) llvm_id=llvm-linux-amd64 ;;
  arm64) llvm_id=llvm-linux-arm64 ;;
esac

temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT
llvm_full_root="$temporary_root/llvm-full"
musl_sysroot="$temporary_root/musl-sysroot"
mkdir -p "$llvm_full_root" "$musl_sysroot"

toolchain_download_asset "$llvm_id" "$temporary_root/llvm.tar.xz"
tar -xJf "$temporary_root/llvm.tar.xz" -C "$llvm_full_root" --strip-components=1
toolchain_download_asset musl-source "$temporary_root/musl.tar.gz"
mkdir -p "$temporary_root/musl-source"
tar -xzf "$temporary_root/musl.tar.gz" -C "$temporary_root/musl-source" --strip-components=1

echo "=== Full LLVM extracted size ==="
du -sh "$llvm_full_root"
echo "=== Full LLVM top-level sizes ==="
du -sh "$llvm_full_root"/* | sort -h

full_clang="$llvm_full_root/bin/clang"
full_archiver="$llvm_full_root/bin/llvm-ar"
full_ranlib="$llvm_full_root/bin/llvm-ranlib"
(
  cd "$temporary_root/musl-source"
  CC="$full_clang --target=$triple" AR="$full_archiver" RANLIB="$full_ranlib" ./configure \
    --prefix=/ \
    --target="$triple" \
    --disable-shared
  make -j2
  make DESTDIR="$musl_sysroot" install
)

mkdir -p "$output_root/bin" "$output_root/lib" "$output_root/licenses" "$output_root/sysroot"

copy_required_tool() {
  local name="$1"
  local source_path="$llvm_full_root/bin/$name"
  local resolved
  test -e "$source_path" || { echo "required LLVM tool missing: $source_path" >&2; return 1; }
  resolved="$(readlink -f "$source_path")"
  case "$resolved" in
    "$llvm_full_root/bin/"*) ;;
    *) echo "required LLVM tool resolves outside bin directory: $source_path -> $resolved" >&2; return 1 ;;
  esac
  install -m 0755 "$resolved" "$output_root/bin/$name"
}

declare -A copied_libraries=()
copy_bundled_dependencies() {
  local elf="$1"
  local soname source_path resolved target_name
  local -a candidates
  while IFS= read -r soname; do
    [ -n "$soname" ] || continue
    [ -z "${copied_libraries[$soname]:-}" ] || continue
    mapfile -t candidates < <(find "$llvm_full_root/lib" \( -type f -o -type l \) -name "$soname" -print)
    [ "${#candidates[@]}" -gt 0 ] || continue
    if [ "${#candidates[@]}" -ne 1 ]; then
      printf 'bundled LLVM dependency %s has ambiguous sources:\n' "$soname" >&2
      printf '  %s\n' "${candidates[@]}" >&2
      return 1
    fi
    source_path="${candidates[0]}"
    copied_libraries[$soname]=true
    resolved="$(readlink -f "$source_path")"
    case "$resolved" in
      "$llvm_full_root/lib/"*) ;;
      *) echo "bundled LLVM library resolves outside lib directory: $source_path -> $resolved" >&2; return 1 ;;
    esac
    target_name="$(basename "$resolved")"
    install -m 0755 "$resolved" "$output_root/lib/$target_name"
    if [ "$soname" != "$target_name" ]; then
      ln -sfn "$target_name" "$output_root/lib/$soname"
    fi
    copy_bundled_dependencies "$resolved"
  done < <(readelf -d "$elf" 2>/dev/null | sed -n 's/.*(NEEDED).*[[]\([^]]*\)[]].*/\1/p')
}

command -v readelf >/dev/null 2>&1 || { echo "readelf is required to curate managed Linux tools" >&2; exit 1; }
for tool in clang llvm-ar; do
  copy_required_tool "$tool"
  copy_bundled_dependencies "$(readlink -f "$llvm_full_root/bin/$tool")"
done

if [ "$release_arch" = arm64 ]; then
  toolchain_download_asset llvm-source "$temporary_root/llvm-source.tar.xz"
  mkdir -p "$temporary_root/llvm-source"
  tar -xJf "$temporary_root/llvm-source.tar.xz" -C "$temporary_root/llvm-source" --strip-components=1
  cmake -G Ninja \
    -S "$temporary_root/llvm-source/llvm" \
    -B "$temporary_root/llvm-build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_C_COMPILER="$llvm_full_root/bin/clang" \
    -DCMAKE_CXX_COMPILER="$llvm_full_root/bin/clang++" \
    -DLLVM_ENABLE_PROJECTS=lld \
    -DLLVM_TARGETS_TO_BUILD=AArch64 \
    -DLLVM_INCLUDE_TESTS=OFF \
    -DLLVM_INCLUDE_EXAMPLES=OFF \
    -DLLVM_INCLUDE_BENCHMARKS=OFF \
    -DLLVM_ENABLE_TERMINFO=OFF \
    -DLLVM_ENABLE_ZLIB=OFF \
    -DLLVM_ENABLE_ZSTD=OFF \
    -DLLVM_ENABLE_LIBXML2=OFF \
    -DLLVM_ENABLE_CURL=OFF \
    -DLLVM_ENABLE_LIBEDIT=OFF
  cmake --build "$temporary_root/llvm-build" --target lld -j 2
  install -m 0755 "$temporary_root/llvm-build/bin/lld" "$output_root/bin/ld.lld"
else
  copy_required_tool ld.lld
  copy_bundled_dependencies "$(readlink -f "$llvm_full_root/bin/ld.lld")"
fi

full_resource_dir="$("$full_clang" --target="$triple" -print-resource-dir)"
case "$full_resource_dir" in
  "$llvm_full_root"/*) ;;
  *) echo "upstream clang resource directory escapes LLVM tree: $full_resource_dir" >&2; exit 1 ;;
esac
resource_relative="${full_resource_dir#"$llvm_full_root"/}"
final_resource_dir="$output_root/$resource_relative"
mkdir -p "$final_resource_dir"
cp -a "$full_resource_dir/include" "$final_resource_dir/include"

compiler_runtime="$("$full_clang" -rtlib=compiler-rt --print-libgcc-file-name)"
test -f "$compiler_runtime" || { echo "target compiler-rt builtins missing: $compiler_runtime" >&2; exit 1; }
case "$compiler_runtime" in
  "$full_resource_dir"/*) ;;
  *) echo "target compiler-rt builtins escape clang resource directory: $compiler_runtime" >&2; exit 1 ;;
esac
compiler_runtime_relative="${compiler_runtime#"$llvm_full_root"/}"
mkdir -p "$output_root/$(dirname "$compiler_runtime_relative")"
cp "$compiler_runtime" "$output_root/$compiler_runtime_relative"

cp -a "$musl_sysroot/". "$output_root/sysroot/"
cp "$compiler_runtime" "$output_root/sysroot/lib/libclang_rt.builtins.a"
test "$(toolchain_sha256 "$script_root/llvm-LICENSE.TXT")" = 3340babe8ac7bc6ae294d93aa01c310a250d43d5b760e5c12954882d4e5c83c7
cp "$script_root/llvm-LICENSE.TXT" "$output_root/licenses/LLVM-LICENSE.txt"
cp "$temporary_root/musl-source/COPYRIGHT" "$output_root/licenses/musl-COPYRIGHT"

validate_linux_toolchain "$output_root"

echo "=== Curated Linux toolchain size ==="
du -sh "$output_root"
echo "=== Curated top-level sizes ==="
du -sh "$output_root"/* | sort -h
echo "=== Curated largest files ==="
find "$output_root" -type f -printf '%s %p\n' | sort -n | tail -n 100
