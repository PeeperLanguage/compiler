#!/usr/bin/env bash

toolchain_sources_lock="${TOOLCHAIN_SOURCES_LOCK:-pkg/distribution/toolchain-sources.lock.json}"

toolchain_asset_field() {
  jq -er --arg id "$1" --arg field "$2" '.assets[] | select(.id == $id) | .[$field]' "$toolchain_sources_lock"
}

toolchain_sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

toolchain_sha256_stream() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  else
    sha256sum | awk '{print $1}'
  fi
}

toolchain_download_asset() {
  local id="$1" destination="$2"
  local url size sha256 actual_sha256
  url="$(toolchain_asset_field "$id" url)"
  size="$(toolchain_asset_field "$id" size)"
  sha256="$(toolchain_asset_field "$id" sha256)"
  curl --fail --location --retry 3 --output "$destination" "$url"
  test "$(wc -c < "$destination")" -eq "$size"
  actual_sha256="$(toolchain_sha256 "$destination")"
  test "$actual_sha256" = "$sha256"
}
