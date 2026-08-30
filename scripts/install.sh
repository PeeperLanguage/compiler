#!/bin/sh
# Peeper bootstrap installer: downloads the release manifest, verifies it
# against the published SHA256SUMS, downloads the compiler and toolchain packs
# for the detected platform, verifies every SHA-256, extracts into a staging
# directory, activates atomically, and persists the binary directory in the
# user PATH.
set -eu

repository="PeeperLanguage/compiler"
base_url="https://github.com/${repository}/releases/latest/download"

fail() {
  echo "peeper install: $1" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

machine=$(uname -m)
arch=""
case "$machine" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
esac
# Under Rosetta, uname reports x86_64 while the hardware is arm64; prefer the native build.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi
[ -n "$arch" ] || fail "unsupported architecture: $machine"

# Staging lives in $HOME so activation is a same-filesystem rename.
work="$(mktemp -d "$HOME/.peeper-install-XXXXXX")"
trap 'rm -rf "$work"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL "$base_url/SHA256SUMS" -o "$work/SHA256SUMS"
curl --proto '=https' --tlsv1.2 -fL --progress-bar "$base_url/release-manifest.json" -o "$work/release-manifest.json"

expected=$(grep "  release-manifest.json\$" "$work/SHA256SUMS" | cut -d' ' -f1 || true)
case "$expected" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) fail "checksum for release-manifest.json not found in SHA256SUMS" ;;
esac
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/release-manifest.json" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$work/release-manifest.json" | cut -d' ' -f1)
fi
[ "$actual" = "$expected" ] || fail "release manifest checksum mismatch"

# Extract one field from the component object matching kind/os/arch. The
# manifest is generated with a fixed field order, one field per line.
component_field() {
  awk -v RS="}" \
      -v kind="\"kind\": \"$1\"" \
      -v os="\"os\": \"$2\"" \
      -v arch="\"arch\": \"$3\"" \
      -v field="\"$4\": " '
    index($0, kind) && index($0, os) && index($0, arch) {
      if (match($0, field "\"[^\"]*\"")) {
        value = substr($0, RSTART + length(field), RLENGTH - length(field))
        gsub(/^"|"$/, "", value)
        print value
        exit
      }
    }
  ' "$work/release-manifest.json"
}

compiler_url=$(component_field compiler "$os" "$arch" url)
compiler_sha=$(component_field compiler "$os" "$arch" sha256)
version=$(component_field compiler "$os" "$arch" version)
toolchain_url=$(component_field toolchain "$os" "$arch" url)
toolchain_sha=$(component_field toolchain "$os" "$arch" sha256)
[ -n "$compiler_url" ] && [ -n "$compiler_sha" ] && [ -n "$version" ] && [ -n "$toolchain_url" ] && [ -n "$toolchain_sha" ] \
  || fail "release manifest has no complete component set for $os/$arch"

download_component() {
  url=$1
  sha=$2
  output=$3
  case "$url" in
    https://*) ;;
    *) fail "component URL is not HTTPS: $url" ;;
  esac
  curl --proto '=https' --tlsv1.2 -fL --progress-bar "$url" -o "$output"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$output" | cut -d' ' -f1)
  else
    actual=$(shasum -a 256 "$output" | cut -d' ' -f1)
  fi
  [ "$actual" = "$sha" ] || fail "component checksum mismatch: $output"
}

download_component "$compiler_url" "$compiler_sha" "$work/compiler.tar.gz"
download_component "$toolchain_url" "$toolchain_sha" "$work/toolchain.tar.gz"

mkdir -p "$work/staging"
tar -xzf "$work/compiler.tar.gz" -C "$work/staging"
tar -xzf "$work/toolchain.tar.gz" -C "$work/staging"
test -x "$work/staging/bin/peeper" || fail "staged installation has no peeper executable"
test -f "$work/staging/toolchains/native/profile.json" || fail "staged installation has no managed toolchain profile"

install_root="$HOME/.peeper"
if [ -e "$install_root" ]; then
  rm -rf "$install_root.old"
  mv "$install_root" "$install_root.old"
fi
if mv "$work/staging" "$install_root"; then
  rm -rf "$install_root.old"
else
  if [ -d "$install_root.old" ]; then
    mv "$install_root.old" "$install_root"
  fi
  fail "could not activate installation at $install_root"
fi

echo "Installed Peeper $version in $install_root"
echo "Add $install_root/bin to PATH."

persist_path() {
  file=$1
  line=$2
  mkdir -p "$(dirname "$file")"
  if [ ! -f "$file" ] || ! grep -Fq "$install_root/bin" "$file"; then
    printf '\n%s\n' "$line" >> "$file"
    echo "Added $install_root/bin to $file. Restart your terminal to apply."
  fi
}

case "${SHELL##*/}" in
  zsh) persist_path "$HOME/.zshrc" "export PATH=\"$install_root/bin:\$PATH\" # peeper" ;;
  bash) persist_path "$HOME/.bashrc" "export PATH=\"$install_root/bin:\$PATH\" # peeper" ;;
  fish) persist_path "$HOME/.config/fish/conf.d/peeper.fish" "fish_add_path \"$install_root/bin\"" ;;
  *) persist_path "$HOME/.profile" "export PATH=\"$install_root/bin:\$PATH\" # peeper" ;;
esac
