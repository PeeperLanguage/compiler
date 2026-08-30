#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: build-windows.sh <arch> <output-root>" >&2
  exit 2
fi

release_arch="$1"
output_root="$2"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/common.sh"

case "$release_arch" in
  amd64) llvm_id=llvm-mingw-windows-amd64 ;;
  arm64) llvm_id=llvm-mingw-windows-arm64 ;;
  *) echo "unsupported Windows architecture $release_arch" >&2; exit 2 ;;
esac

temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT
mkdir -p "$output_root"
output_root="$(cd "$output_root" && pwd)"

toolchain_download_asset "$llvm_id" "$temporary_root/llvm-mingw.zip"
mkdir -p "$temporary_root/llvm-mingw"
unzip -q "$temporary_root/llvm-mingw.zip" -d "$temporary_root/llvm-mingw"
extracted="$(find "$temporary_root/llvm-mingw" -mindepth 1 -maxdepth 1 -type d -print -quit)"
test -n "$extracted"
cp -R "$extracted"/. "$output_root"/
mkdir -p "$output_root/licenses"
cp "$extracted/LICENSE.TXT" "$output_root/licenses/LLVM-MINGW-LICENSE.txt"
rm -rf "$output_root/share"

test -x "$output_root/bin/clang.exe"
test -x "$output_root/bin/llvm-ar.exe"
test -x "$output_root/bin/llvm-ranlib.exe"
find "$output_root/bin" -maxdepth 1 -type f \( -name 'ld.lld.exe' -o -name 'lld-link.exe' \) -print -quit | grep -q .
"$output_root/bin/clang.exe" --version
