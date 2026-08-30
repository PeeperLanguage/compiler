#!/bin/sh
# Peeper bootstrap installer: downloads the native installer for the detected
# platform, verifies it against the published SHA256SUMS, runs it, and
# persists the binary directory in the user PATH.
set -eu

repository="PeeperLanguage/compiler"
base_url="https://github.com/${repository}/releases/latest/download"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "peeper install: unsupported operating system: $(uname -s)" >&2; exit 1 ;;
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
[ -n "$arch" ] || { echo "peeper install: unsupported architecture: $machine" >&2; exit 1; }

installer="peeper-installer-${os}-${arch}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL "$base_url/$installer" -o "$work/$installer"
curl --proto '=https' --tlsv1.2 -fsSL "$base_url/SHA256SUMS" -o "$work/SHA256SUMS"

expected=$(grep "  ${installer}\$" "$work/SHA256SUMS" | cut -d' ' -f1 || true)
case "$expected" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) echo "peeper install: checksum for $installer not found in SHA256SUMS" >&2; exit 1 ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "$expected" "$work/$installer" | sha256sum -c - >/dev/null
else
  printf '%s  %s\n' "$expected" "$work/$installer" | shasum -a 256 -c - >/dev/null
fi

chmod +x "$work/$installer"
output="$("$work/$installer")"
printf '%s\n' "$output"

bin_dir=$(printf '%s\n' "$output" | sed -n 's/^Add \(.*\) to PATH\.$/\1/p')
[ -n "$bin_dir" ] || { echo "peeper install: could not determine binary directory" >&2; exit 1; }

persist_path() {
  file=$1
  line=$2
  mkdir -p "$(dirname "$file")"
  if [ ! -f "$file" ] || ! grep -Fq "$bin_dir" "$file"; then
    printf '\n%s\n' "$line" >> "$file"
    echo "Added $bin_dir to $file. Restart your terminal to apply."
  fi
}

case "${SHELL##*/}" in
  zsh) persist_path "$HOME/.zshrc" "export PATH=\"$bin_dir:\$PATH\" # peeper" ;;
  bash) persist_path "$HOME/.bashrc" "export PATH=\"$bin_dir:\$PATH\" # peeper" ;;
  fish) persist_path "$HOME/.config/fish/conf.d/peeper.fish" "fish_add_path \"$bin_dir\"" ;;
  *) persist_path "$HOME/.profile" "export PATH=\"$bin_dir:\$PATH\" # peeper" ;;
esac
