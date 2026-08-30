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
        scripts/toolchain-fingerprint.sh|cmd/release-profile/*|internal/toolchain/*)
          select_all
          ;;
        .github/workflows/build-toolchains.yml|.github/workflows/toolchain-target.yml)
          # Producer orchestration does not change immutable payload identity.
          ;;
        scripts/toolchains/common.sh)
          old_common="$(git show "$base:$file")"
          normalized_old="$(sed 's#pkg/distribution/toolchain-sources\.lock\.json#toolchains/toolchain-sources.lock.json#g' <<< "$old_common")"
          normalized_new="$(sed 's#pkg/distribution/toolchain-sources\.lock\.json#toolchains/toolchain-sources.lock.json#g' "$file")"
          [ "$normalized_old" = "$normalized_new" ] || select_all
          ;;
        scripts/toolchains/build-linux.sh) select_family linux ;;
        scripts/toolchains/build-darwin.sh) select_family darwin ;;
        scripts/toolchains/build-windows.sh) select_family windows ;;
        toolchains/toolchain-sources.lock.json)
          old_lock_path="$file"
          if ! git cat-file -e "$base:$old_lock_path" 2>/dev/null; then
            old_lock_path=pkg/distribution/toolchain-sources.lock.json
          fi
          if ! git cat-file -e "$base:$old_lock_path" 2>/dev/null; then
            old_lock_path=distribution/toolchain-sources.lock.json
          fi
          if ! git cat-file -e "$base:$old_lock_path" 2>/dev/null; then
            select_all
            continue
          fi
          old_lock="$(git show "$base:$old_lock_path")"
          for id in llvm-linux-amd64 llvm-linux-arm64 musl-source llvm-source llvm-mingw-windows-amd64 llvm-mingw-windows-arm64; do
            old_entry="$(jq -cS --arg id "$id" '.assets[] | select(.id == $id)' <<< "$old_lock")"
            new_entry="$(jq -cS --arg id "$id" '.assets[] | select(.id == $id)' "$file")"
            [ "$old_entry" = "$new_entry" ] && continue
            case "$id" in
              llvm-linux-amd64) linux_amd64=true ;;
              llvm-linux-arm64) linux_arm64=true ;;
              musl-source) select_family linux ;;
              llvm-source) linux_arm64=true; select_family darwin ;;
              llvm-mingw-windows-amd64) windows_amd64=true ;;
              llvm-mingw-windows-arm64) windows_arm64=true ;;
            esac
          done
          ;;
      esac
    done
  fi
fi

if [ "$requested_family" = auto ] && [ "${FORCE_ALL:-false}" != true ] && [ -f toolchains/toolchains.lock.json ]; then
  for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
    selected="${!target}"
    [ "$selected" = true ] || continue
    target_os="${target%_*}"
    target_arch="${target##*_}"
    minimum=()
    [ "$target_os" = darwin ] && minimum=(13.0)
    desired_id="$(scripts/toolchain-fingerprint.sh "$target_os" "$target_arch" "${minimum[@]}" | jq -er .id)"
    locked_id="$(jq -er \
      --arg os "$target_os" \
      --arg arch "$target_arch" \
      '[.toolchains[] | select(.os == $os and .arch == $arch)] | if length == 1 then .[0].id else empty end' \
      toolchains/toolchains.lock.json 2>/dev/null || true)"
    if [ "$desired_id" = "$locked_id" ]; then
      printf -v "$target" false
      echo "Skipping $target: desired immutable toolchain is already selected." >&2
    fi
  done
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
