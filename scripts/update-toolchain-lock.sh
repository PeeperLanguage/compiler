#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: update-toolchain-lock.sh <lock-file> <records-directory>" >&2
  exit 2
fi

lock_file="$1"
records_directory="$2"
targets=(linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64)

for target in "${targets[@]}"; do
  selected_name="SELECTED_${target}"
  result_name="RESULT_${target}"
  selected="${!selected_name:-}"
  result="${!result_name:-}"
  if [ "$selected" = true ] && [ "$result" != success ]; then
    echo "selected toolchain $target ended as $result" >&2
    exit 1
  fi
done

shopt -s nullglob
records=("$records_directory"/*.json)
if [ "${#records[@]}" -eq 0 ]; then
  echo "no selected toolchain records" >&2
  exit 1
fi

records_json="$(jq -sc 'sort_by(.os, .arch)' "${records[@]}")"
temporary="$(mktemp "${lock_file}.tmp.XXXXXX")"
trap 'rm -f "$temporary"' EXIT
jq --argjson records "$records_json" '
  .toolchains = (
    ([.toolchains[] | select(.os as $os | .arch as $arch | $records | any(.os == $os and .arch == $arch) | not)] + $records)
    | sort_by(.os, .arch)
  )
' "$lock_file" > "$temporary"
mv "$temporary" "$lock_file"
trap - EXIT

for target in "${targets[@]}"; do
  target_os="${target%_*}"
  target_arch="${target##*_}"
  go run ./cmd/toolchain-lock -lock "$lock_file" -os "$target_os" -arch "$target_arch" >/dev/null
done
