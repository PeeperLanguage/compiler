#!/bin/bash
set -euo pipefail

if [ "$#" -ne 6 ]; then
  echo "usage: package-release.sh <install-root> <bootstrap-root> <os> <arch> <version> <output-root>" >&2
  exit 2
fi

install_root="$1"
bootstrap_root="$2"
release_os="$3"
release_arch="$4"
release_version="$5"
output_root="$6"

format=tar.gz
executable_suffix=""
if [ "$release_os" = windows ]; then
  format=zip
  executable_suffix=.exe
fi

test -f "$install_root/bin/peeper$executable_suffix"
test -d "$install_root/libs/core"
test -f "$install_root/toolchains/native/profile.json"
test -d "$install_root/targets"
test -f "$bootstrap_root/peeper-installer$executable_suffix"

package_root="$(mktemp -d)"
trap 'rm -rf "$package_root"' EXIT
compiler_root="$package_root/compiler"
target_root="$package_root/target"
toolchain_root="$package_root/toolchain"
mkdir -p "$compiler_root/bin" "$target_root" "$toolchain_root" "$output_root"
cp "$install_root/bin/peeper$executable_suffix" "$compiler_root/bin/"
cp -R "$install_root/libs" "$compiler_root/"
cp "$install_root/LICENSE" "$compiler_root/"
cp -R "$install_root/targets" "$target_root/"
cp -R "$install_root/toolchains" "$toolchain_root/"

for kind in compiler target toolchain; do
  component_id="$kind-$release_os-$release_arch-v$release_version"
  archive="$output_root/$component_id.$format"
  go run ./cmd/distpack \
    -source "$package_root/$kind" \
    -output "$archive" \
    -format "$format" \
    -kind "$kind" \
    -id "$component_id" \
    -version "$release_version" \
    -os "$release_os" \
    -arch "$release_arch" \
    > "$archive.json"
done

cp "$bootstrap_root/peeper-installer$executable_suffix" "$output_root/peeper-installer-$release_os-$release_arch$executable_suffix"
