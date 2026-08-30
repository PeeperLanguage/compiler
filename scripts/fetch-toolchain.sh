#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: fetch-toolchain.sh <os> <arch> <destination>" >&2
  exit 2
fi

target_os="$1"
target_arch="$2"
destination="$3"
lock_file="${TOOLCHAINS_LOCK:-toolchains/toolchains.lock.json}"
temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT

record="$(go run ./cmd/toolchain-lock -lock "$lock_file" -os "$target_os" -arch "$target_arch")"
id="$(jq -er .id <<< "$record")"
version="$(jq -er .version <<< "$record")"
url="$(jq -er .url <<< "$record")"
size="$(jq -er .size <<< "$record")"
sha256="$(jq -er .sha256 <<< "$record")"
format="$(jq -er .format <<< "$record")"

archive="$temporary_root/toolchain.$format"
curl --fail --location --retry 3 --output "$archive" "$url"
test "$(wc -c < "$archive")" -eq "$size"
if command -v shasum >/dev/null 2>&1; then
  actual_sha256="$(shasum -a 256 "$archive" | awk '{print $1}')"
else
  actual_sha256="$(sha256sum "$archive" | awk '{print $1}')"
fi
test "$actual_sha256" = "$sha256"

go run ./cmd/distunpack \
  -archive "$archive" \
  -format "$format" \
  -destination "$destination" \
  -kind toolchain \
  -id "$id" \
  -version "$version" \
  -os "$target_os" \
  -arch "$target_arch"
