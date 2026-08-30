#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: toolchain-fingerprint.sh <os> <arch> [minimum-macos]" >&2
  exit 2
fi

target_os="$1"
target_arch="$2"
minimum_macos="${3:-13.0}"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/toolchains/common.sh"

case "$target_os/$target_arch" in
  linux/amd64) source_ids='["llvm-linux-amd64","musl-source"]'; llvm_target=X86; family_recipe=scripts/toolchains/build-linux.sh ;;
  linux/arm64) source_ids='["llvm-linux-arm64","llvm-source","musl-source"]'; llvm_target=AArch64; family_recipe=scripts/toolchains/build-linux.sh ;;
  darwin/amd64) source_ids='["llvm-source"]'; llvm_target=X86; family_recipe=scripts/toolchains/build-darwin.sh ;;
  darwin/arm64) source_ids='["llvm-source"]'; llvm_target=AArch64; family_recipe=scripts/toolchains/build-darwin.sh ;;
  windows/amd64) source_ids='["llvm-mingw-windows-amd64"]'; llvm_target=X86; family_recipe=scripts/toolchains/build-windows.sh ;;
  windows/arm64) source_ids='["llvm-mingw-windows-arm64"]'; llvm_target=AArch64; family_recipe=scripts/toolchains/build-windows.sh ;;
  *) echo "unsupported toolchain target $target_os/$target_arch" >&2; exit 2 ;;
esac

normalized_sources="$(jq -cS --argjson ids "$source_ids" '[.assets[] | select(.id as $id | $ids | index($id))] | sort_by(.id)' "$toolchain_sources_lock")"
fingerprint="$({
  printf 'schema=1\nos=%s\narch=%s\nllvm_target=%s\n' "$target_os" "$target_arch" "$llvm_target"
  if [ "$target_os" = linux ]; then
    printf 'musl_shared=false\nlink_mode=static\ncompiler_rt_builtins=true\n'
  elif [ "$target_os" = darwin ]; then
    printf 'minimum_macos=%s\n' "$minimum_macos"
  fi
  printf 'sources=%s\n' "$normalized_sources"
  for recipe in scripts/toolchain-fingerprint.sh scripts/toolchains/common.sh "$family_recipe" cmd/release-profile/*.go internal/toolchain/*.go; do
    printf 'recipe=%s:%s\n' "$recipe" "$(toolchain_sha256 "$recipe")"
  done
} | toolchain_sha256_stream)"
short_fingerprint="${fingerprint:0:12}"

case "$target_os" in
  linux)
    llvm_version="$(toolchain_asset_field "llvm-linux-$target_arch" version)"
    musl_version="$(toolchain_asset_field musl-source version)"
    version="llvm${llvm_version}-musl${musl_version}-r${short_fingerprint}"
    ;;
  darwin)
    version="llvm$(toolchain_asset_field llvm-source version)-r${short_fingerprint}"
    ;;
  windows)
    version="llvm-mingw-$(toolchain_asset_field "llvm-mingw-windows-$target_arch" version)-r${short_fingerprint}"
    ;;
esac

id="toolchain-$target_os-$target_arch-$version"
tag="toolchain-$target_os-$target_arch-$fingerprint"
jq -cn \
  --arg fingerprint "$fingerprint" \
  --arg version "$version" \
  --arg id "$id" \
  --arg tag "$tag" \
  --arg llvm_target "$llvm_target" \
  '{fingerprint:$fingerprint,version:$version,id:$id,tag:$tag,llvm_target:$llvm_target}'
