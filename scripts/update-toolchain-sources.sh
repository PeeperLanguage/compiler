#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

if [ "$#" -ne 2 ]; then
  echo "usage: update-toolchain-sources.sh <current-lock> <output-lock>" >&2
  exit 2
fi

current_lock="$1"
output_lock="$2"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/toolchains/common.sh"
temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT
candidate="$temporary_root/toolchain-sources.lock.json"
cp "$current_lock" "$candidate"
changed_summary="$temporary_root/changed.md"
: > "$changed_summary"

read_release() {
  local repository="$1" override="$2" destination="$3"
  if [ -n "$override" ]; then
    jq -e . "$override" > "$destination"
  else
    gh api "repos/$repository/releases/latest" > "$destination"
  fi
  jq -e '.draft == false and .prerelease == false' "$destination" >/dev/null
}

release_asset() {
  local release="$1" name="$2"
  jq -ce --arg name "$name" '
    [.assets[] | select(.name == $name)]
    | if length == 1 then .[0] else error("release requires exactly one asset named " + $name) end
  ' "$release"
}

replace_asset() {
  local id="$1" version="$2" url="$3" size="$4" sha256="$5" signature_url="$6" license_url="$7"
  local previous updated
  previous="$(jq -er --arg id "$id" '.assets[] | select(.id == $id) | .version' "$candidate")"
  local updated="$temporary_root/updated.json"
  jq \
    --arg id "$id" \
    --arg version "$version" \
    --arg url "$url" \
    --argjson size "$size" \
    --arg sha256 "$sha256" \
    --arg signature_url "$signature_url" \
    --arg license_url "$license_url" '
      if ([.assets[] | select(.id == $id)] | length) != 1 then
        error("source lock requires exactly one asset " + $id)
      else
        .assets |= map(
          if .id == $id then
            .version = $version
            | .url = $url
            | .size = $size
            | .sha256 = $sha256
            | .license_url = $license_url
            | if $signature_url == "" then del(.signature_url) else .signature_url = $signature_url end
          else . end
        )
      end
    ' "$candidate" > "$updated"
  mv "$updated" "$candidate"
  if [ "$previous" != "$version" ]; then
    printf -- '- %s: %s → %s\n' "$id" "$previous" "$version" >> "$changed_summary"
  fi
}

require_not_older() {
  local label="$1" current="$2" discovered="$3"
  local newest
  newest="$(printf '%s\n%s\n' "$current" "$discovered" | sort -V | tail -n 1)"
  if [ "$newest" != "$discovered" ]; then
    echo "$label latest release $discovered is older than locked version $current" >&2
    exit 1
  fi
}

asset_metadata() {
  local asset="$1" expected_prefix="$2"
  local url size digest
  url="$(jq -er .browser_download_url <<< "$asset")"
  size="$(jq -er '.size | select(type == "number" and . > 0)' <<< "$asset")"
  digest="$(jq -er '.digest | select(type == "string")' <<< "$asset")"
  digest="${digest#sha256:}"
  [[ "$url" == "$expected_prefix"* ]]
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]]
  printf '%s\n%s\n%s\n' "$url" "$size" "$digest"
}

llvm_release="$temporary_root/llvm-release.json"
read_release llvm/llvm-project "${LLVM_RELEASE_JSON:-}" "$llvm_release"
llvm_tag="$(jq -er .tag_name "$llvm_release")"
[[ "$llvm_tag" =~ ^llvmorg-([0-9]+\.[0-9]+\.[0-9]+)$ ]]
llvm_version="${BASH_REMATCH[1]}"
require_not_older LLVM "$(jq -er '.assets[] | select(.id == "llvm-source") | .version' "$current_lock")" "$llvm_version"
llvm_prefix="https://github.com/llvm/llvm-project/releases/download/$llvm_tag/"
llvm_license="https://github.com/llvm/llvm-project/blob/$llvm_tag/LICENSE.TXT"

for specification in \
  "llvm-source|llvm-project-$llvm_version.src.tar.xz" \
  "llvm-linux-amd64|LLVM-$llvm_version-Linux-X64.tar.xz" \
  "llvm-linux-arm64|LLVM-$llvm_version-Linux-ARM64.tar.xz"; do
  id="${specification%%|*}"
  name="${specification#*|}"
  asset="$(release_asset "$llvm_release" "$name")"
  signature="$(release_asset "$llvm_release" "$name.sig")"
  mapfile -t metadata < <(asset_metadata "$asset" "$llvm_prefix")
  signature_url="$(jq -er .browser_download_url <<< "$signature")"
  [[ "$signature_url" == "$llvm_prefix"* ]]
  replace_asset "$id" "$llvm_version" "${metadata[0]}" "${metadata[1]}" "${metadata[2]}" "$signature_url" "$llvm_license"
done

mingw_release="$temporary_root/llvm-mingw-release.json"
read_release mstorsjo/llvm-mingw "${LLVM_MINGW_RELEASE_JSON:-}" "$mingw_release"
mingw_version="$(jq -er .tag_name "$mingw_release")"
[[ "$mingw_version" =~ ^[0-9]{8}$ ]]
require_not_older llvm-mingw "$(jq -er '.assets[] | select(.id == "llvm-mingw-windows-amd64") | .version' "$current_lock")" "$mingw_version"
mingw_prefix="https://github.com/mstorsjo/llvm-mingw/releases/download/$mingw_version/"
mingw_license="https://github.com/mstorsjo/llvm-mingw/blob/$mingw_version/LICENSE.TXT"

for specification in \
  "llvm-mingw-windows-amd64|llvm-mingw-$mingw_version-ucrt-x86_64.zip" \
  "llvm-mingw-windows-arm64|llvm-mingw-$mingw_version-ucrt-aarch64.zip"; do
  id="${specification%%|*}"
  name="${specification#*|}"
  asset="$(release_asset "$mingw_release" "$name")"
  mapfile -t metadata < <(asset_metadata "$asset" "$mingw_prefix")
  replace_asset "$id" "$mingw_version" "${metadata[0]}" "${metadata[1]}" "${metadata[2]}" "" "$mingw_license"
done

musl_version="${MUSL_VERSION:-}"
if [ -z "$musl_version" ]; then
  musl_version="$(
    git ls-remote --tags --refs https://git.musl-libc.org/git/musl 'refs/tags/v*' \
      | sed -n 's#.*refs/tags/v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$#\1#p' \
      | sort -V \
      | tail -n 1
  )"
fi
[[ "$musl_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
current_musl_version="$(jq -er '.assets[] | select(.id == "musl-source") | .version' "$current_lock")"
require_not_older musl "$current_musl_version" "$musl_version"
if [ "$musl_version" != "$current_musl_version" ]; then
  musl_url="https://musl.libc.org/releases/musl-$musl_version.tar.gz"
  musl_archive="${MUSL_ARCHIVE:-$temporary_root/musl.tar.gz}"
  if [ -z "${MUSL_ARCHIVE:-}" ]; then
    curl --fail --location --retry 3 --output "$musl_archive" "$musl_url"
  fi
  musl_size="$(wc -c < "$musl_archive")"
  musl_sha256="$(toolchain_sha256 "$musl_archive")"
  replace_asset \
    musl-source \
    "$musl_version" \
    "$musl_url" \
    "$musl_size" \
    "$musl_sha256" \
    "" \
    "https://git.musl-libc.org/cgit/musl/tree/COPYRIGHT?h=v$musl_version"
fi

# Canonical serialization: logical key order, optional signature_url last among
# asset identity fields. Must stay byte-stable so no-change runs produce an
# identical file instead of a formatting-only diff.
jq '
  {
    schema_version: .schema_version,
    kind: .kind,
    assets: [.assets[] |
      ({id, version, url, size, sha256} +
       (if .signature_url then {signature_url} else {} end) +
       {license, license_url})
    ]
  }
' "$candidate" > "$output_lock"

# PR summary fragment: survives the temp-dir cleanup trap, consumed by the
# update workflow when composing the pull request body.
cp "$changed_summary" "$output_lock.summary"
