#!/usr/bin/env bash
set -euo pipefail

linux_amd64=false
linux_arm64=false
darwin_amd64=false
darwin_arm64=false
windows_amd64=false
windows_arm64=false

select_all() {
  linux_amd64=true
  linux_arm64=true
  darwin_amd64=true
  darwin_arm64=true
  windows_amd64=true
  windows_arm64=true
}

select_family() {
  case "$1" in
    linux) linux_amd64=true; linux_arm64=true ;;
    darwin) darwin_amd64=true; darwin_arm64=true ;;
    windows) windows_amd64=true; windows_arm64=true ;;
  esac
}

requested_family="${TOOLCHAIN_FAMILY:-auto}"
if [ "$requested_family" = all ]; then
  select_all
elif [ "$requested_family" = linux ] || [ "$requested_family" = darwin ] || [ "$requested_family" = windows ]; then
  select_family "$requested_family"
elif [ "$requested_family" != auto ]; then
  echo "unknown toolchain family: $requested_family" >&2
  exit 2
elif [ "${FORCE_ALL:-false}" = true ]; then
  select_all
else
  base="${EVENT_BEFORE:-}"
  if [ -z "$base" ] && git rev-parse HEAD^ >/dev/null 2>&1; then
    base=HEAD^
  fi
  if [ -z "$base" ] || [ "$base" = 0000000000000000000000000000000000000000 ] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
    select_all
  else
    mapfile -t changed_files < <(git diff --name-only "$base" HEAD)
    for file in "${changed_files[@]}"; do
      case "$file" in
        scripts/toolchains/common.sh|scripts/toolchain-fingerprint.sh|.github/workflows/build-toolchains.yml|.github/workflows/toolchain-target.yml|cmd/release-profile/*|internal/toolchain/*)
          select_all
          ;;
        scripts/toolchains/build-linux.sh) select_family linux ;;
        scripts/toolchains/build-darwin.sh) select_family darwin ;;
        scripts/toolchains/build-windows.sh) select_family windows ;;
        pkg/distribution/toolchain-sources.lock.json)
          old_lock="$(git show "$base:$file")"
          for id in llvm-linux-amd64 llvm-linux-arm64 musl-source llvm-source llvm-mingw-windows-amd64 llvm-mingw-windows-arm64; do
            old_entry="$(jq -cS --arg id "$id" '.assets[] | select(.id == $id)' <<< "$old_lock")"
            new_entry="$(jq -cS --arg id "$id" '.assets[] | select(.id == $id)' "$file")"
            [ "$old_entry" = "$new_entry" ] && continue
            case "$id" in
              llvm-linux-amd64) linux_amd64=true ;;
              llvm-linux-arm64) linux_arm64=true ;;
              musl-source) select_family linux ;;
              llvm-source) select_family darwin ;;
              llvm-mingw-windows-amd64) windows_amd64=true ;;
              llvm-mingw-windows-arm64) windows_arm64=true ;;
            esac
          done
          ;;
      esac
    done
  fi
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  output="$GITHUB_OUTPUT"
else
  output=/dev/stdout
fi
{
  echo "linux_amd64=$linux_amd64"
  echo "linux_arm64=$linux_arm64"
  echo "darwin_amd64=$darwin_amd64"
  echo "darwin_arm64=$darwin_arm64"
  echo "windows_amd64=$windows_amd64"
  echo "windows_arm64=$windows_arm64"
} >> "$output"
