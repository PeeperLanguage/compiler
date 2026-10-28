#!/usr/bin/env bash
# Sets up this checkout for compiler development: puts the LLVM toolchain
# peeper runs in build/toolchains/native, where scripts/build.sh and the tests
# find it. The first way that works:
#   1. the pack CI built for this machine, pinned in
#      toolchains/toolchains.lock.json, downloaded and checked;
#   2. an installed LLVM 23 or newer, with clang, its linker (ld.lld; ld64.lld
#      on macOS) and llvm-ar side by side, linked in. With none, it offers to install the LLVM release
#      pinned in toolchains/toolchain-sources.lock.json into ~/.peeper-dev.
# On Windows, run scripts/setup.ps1.
set -euo pipefail
cd "$(dirname "$0")/.."

minimum=23 # LLVM 22 crashes on float to wide integer casts (docs/ir-design.md)
native=build/toolchains/native
stamp=build/toolchains/pack-id # the CI pack in $native, so a rerun skips it

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) echo "unsupported system $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
# The name clang's `-fuse-ld=lld` runs on this system.
linker=ld.lld
[ "$os" = darwin ] && linker=ld64.lld

sha256() {
    if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'
}

# Downloads the lock entry $1's file to $2 and checks its size and sha256.
download() {
    local url size sha
    url=$(jq -er .url <<<"$1")
    size=$(jq -er .size <<<"$1")
    sha=$(jq -er .sha256 <<<"$1")
    curl --fail --location --retry 3 --progress-bar --output "$2" "$url" &&
        [ "$(wc -c <"$2" | tr -d ' ')" -eq "$size" ] &&
        [ "$(sha256 "$2")" = "$sha" ]
}

# Way 1: unpacks the CI pack for this machine into $native. Called by `if`,
# where `set -e` is off, so each step that can fail returns on its own.
use_pack() {
    local entry id work
    command -v jq >/dev/null || { echo "way 1 needs jq" >&2; return 1; }
    entry=$(jq -ec --arg os "$os" --arg arch "$arch" \
        '.toolchains[] | select(.os == $os and .arch == $arch)' toolchains/toolchains.lock.json) ||
        { echo "no CI pack for $os-$arch" >&2; return 1; }
    id=$(jq -er .id <<<"$entry")
    if [ -d "$native" ] && [ ! -L "$native" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$id" ]; then
        echo "build/toolchains/native already holds $id"
        return 0
    fi
    work=build/.setup
    rm -rf "$work"
    mkdir -p "$work"
    echo "downloading $id"
    if ! download "$entry" "$work/pack.tar.gz"; then
        echo "cannot download $id, or it is not the file the lock pins" >&2
        rm -rf "$work"
        return 1
    fi
    tar -xzf "$work/pack.tar.gz" -C "$work" || { echo "cannot unpack $id" >&2; return 1; }
    rm -rf "$native" "$stamp"
    mkdir -p build/toolchains
    mv "$work/toolchains/native" "$native" || return 1
    echo "$id" >"$stamp"
    rm -rf "$work"
    echo "build/toolchains/native holds $id"
}

# Way 2: prints the folder of an installed LLVM $minimum or newer, the one
# whose bin holds clang, $linker and llvm-ar. ccache's clang is skipped.
installed_llvm() {
    local clang bin version
    for clang in $(type -ap clang) "$HOME"/.peeper-dev/llvm-*/bin/clang; do
        [ -x "$clang" ] || continue
        clang=$(realpath "$clang")
        bin=$(dirname "$clang")
        [ "$(basename "$clang")" != ccache ] || continue
        [ -x "$bin/$linker" ] && [ -x "$bin/llvm-ar" ] || continue
        version=$("$clang" -dumpversion) || continue
        [ "${version%%.*}" -ge "$minimum" ] || continue
        dirname "$bin"
        return 0
    done
    return 1
}

# Asks to install the LLVM release pinned for this machine into
# ~/.peeper-dev. Called in a condition too, so failures return on their own.
install_llvm() {
    local entry version folder archive answer
    command -v jq >/dev/null || { echo "installing LLVM needs jq" >&2; return 1; }
    entry=$(jq -ec --arg id "llvm-$os-$arch" '.assets[] | select(.id == $id)' \
        toolchains/toolchain-sources.lock.json) ||
        { echo "no LLVM release is pinned for $os-$arch" >&2; return 1; }
    version=$(jq -er .version <<<"$entry")
    folder="$HOME/.peeper-dev/llvm-$version"
    [ -t 0 ] || { echo "not asking to install LLVM: no terminal" >&2; return 1; }
    read -r -p "No LLVM $minimum or newer found. Install LLVM $version into $folder? [y/N] " answer
    [ "$answer" = y ] || [ "$answer" = Y ] || return 1
    mkdir -p "$HOME/.peeper-dev"
    archive="$HOME/.peeper-dev/llvm-$version.tar.xz"
    echo "downloading LLVM $version"
    if ! download "$entry" "$archive"; then
        echo "cannot download LLVM $version, or it is not the file the lock pins" >&2
        rm -f "$archive"
        return 1
    fi
    rm -rf "$folder.part"
    mkdir -p "$folder.part"
    tar -xJf "$archive" -C "$folder.part" --strip-components=1 ||
        { echo "cannot unpack LLVM $version" >&2; return 1; }
    rm -f "$archive"
    mv "$folder.part" "$folder" || return 1
}

if use_pack; then
    exit 0
fi
echo "trying an installed LLVM instead"
if llvm=$(installed_llvm) || { install_llvm && llvm=$(installed_llvm); }; then
    rm -rf "$native" "$stamp"
    mkdir -p build/toolchains
    ln -s "$llvm" "$native"
    echo "build/toolchains/native links $llvm"
    exit 0
fi
echo "no toolchain: install LLVM $minimum or newer with clang, $linker and llvm-ar, then run this again" >&2
exit 1
