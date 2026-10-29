#!/bin/sh
# Installs the latest Peeper release in ~/.peeper: downloads this machine's
# archive (.github/workflows/release.yml), checks it against the release's
# SHA256SUMS, unpacks it beside the old installation and only then swaps
# the two, and adds ~/.peeper/bin to the PATH of the user's shell.
#   curl -fsSL https://github.com/PeeperLanguage/compiler/releases/latest/download/install.sh | sh
set -eu

base_url="https://github.com/PeeperLanguage/compiler/releases/latest/download"
install_root="$HOME/.peeper"

fail() {
    echo "peeper install: $1" >&2
    exit 1
}

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported system $(uname -s)" ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported machine $(uname -m)" ;;
esac
# Under Rosetta uname says x86_64 on an arm64 Mac; the native build is the one.
if [ "$os-$arch" = darwin-amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
    arch=arm64
fi
case "$os-$arch" in
    linux-amd64 | darwin-arm64) ;;
    *) fail "no release for $os-$arch yet: build from source (README.md)" ;;
esac
archive="peeper-$os-$arch.tar.gz"

# In $HOME, so the swap below is a rename on one file system.
work=$(mktemp -d "$HOME/.peeper-install-XXXXXX")
trap 'rm -rf "$work"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL "$base_url/SHA256SUMS" -o "$work/SHA256SUMS"
curl --proto '=https' --tlsv1.2 -fL --progress-bar "$base_url/$archive" -o "$work/$archive"
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$work/SHA256SUMS")
[ -n "$expected" ] || fail "SHA256SUMS has no $archive"
if command -v sha256sum >/dev/null; then
    actual=$(sha256sum "$work/$archive" | awk '{ print $1 }')
else
    actual=$(shasum -a 256 "$work/$archive" | awk '{ print $1 }')
fi
[ "$actual" = "$expected" ] || fail "$archive is not the file SHA256SUMS names"

mkdir "$work/peeper"
tar -xzf "$work/$archive" -C "$work/peeper"
[ -x "$work/peeper/bin/peeper" ] || fail "$archive holds no bin/peeper"

rm -rf "$install_root.old"
[ ! -e "$install_root" ] || mv "$install_root" "$install_root.old"
if ! mv "$work/peeper" "$install_root"; then
    [ ! -e "$install_root.old" ] || mv "$install_root.old" "$install_root"
    fail "cannot move the new installation to $install_root"
fi
rm -rf "$install_root.old"
echo "installed Peeper in $install_root"

# Adds $2 to the shell startup file $1 unless the file names the bin folder.
add_to_path() {
    mkdir -p "$(dirname "$1")"
    if [ ! -f "$1" ] || ! grep -Fq "$install_root/bin" "$1"; then
        printf '\n%s\n' "$2" >>"$1"
        echo "added $install_root/bin to the PATH in $1; open a new terminal to use peeper"
    fi
}
case "${SHELL##*/}" in
    zsh) add_to_path "$HOME/.zshrc" "export PATH=\"$install_root/bin:\$PATH\"" ;;
    bash) add_to_path "$HOME/.bashrc" "export PATH=\"$install_root/bin:\$PATH\"" ;;
    fish) add_to_path "$HOME/.config/fish/conf.d/peeper.fish" "fish_add_path \"$install_root/bin\"" ;;
    *) add_to_path "$HOME/.profile" "export PATH=\"$install_root/bin:\$PATH\"" ;;
esac
