#!/usr/bin/env bash
set -euo pipefail

if [ "${EVENT_NAME:-}" = pull_request ]; then
  base="$EVENT_BASE_SHA"
  head="$EVENT_HEAD_SHA"
elif [ "${EVENT_NAME:-}" = push ] && [ -n "${EVENT_BEFORE:-}" ] && [[ "$EVENT_BEFORE" != 0000000000000000000000000000000000000000 ]]; then
  base="$EVENT_BEFORE"
  head=HEAD
else
  base=""
  head=HEAD
fi

if [ -n "$base" ]; then
  mapfile -t files < <(git diff --name-only "$base" "$head")
else
  mapfile -t files < <(git diff-tree --no-commit-id --name-only -r HEAD)
fi

docs_only=true
compiler_source=false
runtime=false
distribution=false
toolchain=false
workflow=false

for file in "${files[@]}"; do
  [ -n "$file" ] || continue
  non_compiler=false
  case "$file" in
    README.md|docs/*|*.md) non_compiler=true ;;
    *) docs_only=false ;;
  esac
  case "$file" in
    .github/workflows/*|scripts/detect-changes.sh) workflow=true; non_compiler=true ;;
  esac
  case "$file" in
    runtime/*|internal/backend/*|internal/codegen/*) runtime=true; non_compiler=true ;;
  esac
  case "$file" in
    pkg/distribution/*|cmd/distpack/*|cmd/distunpack/*|cmd/release-index/*|cmd/sign-release/*|cmd/toolchain-lock/*|internal/installer/*) distribution=true; non_compiler=true ;;
  esac
  case "$file" in
    toolchains/*|scripts/fetch-toolchain.sh|scripts/plan-toolchains.sh|scripts/toolchain-fingerprint.sh|scripts/update-toolchain-lock.sh|scripts/update-toolchain-sources.sh|scripts/toolchain*_test.go|scripts/toolchains/*|internal/toolchain/*) toolchain=true; non_compiler=true ;;
  esac
  [ "$non_compiler" = true ] || compiler_source=true
done

if [ "${EVENT_NAME:-}" = workflow_dispatch ] || [ "${#files[@]}" -eq 0 ]; then
  docs_only=false
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "docs_only=$docs_only"
    echo "compiler_source=$compiler_source"
    echo "runtime=$runtime"
    echo "distribution=$distribution"
    echo "toolchain=$toolchain"
    echo "workflow=$workflow"
  } >> "$GITHUB_OUTPUT"
else
  printf 'docs_only=%s\ncompiler_source=%s\nruntime=%s\ndistribution=%s\ntoolchain=%s\nworkflow=%s\n' \
    "$docs_only" "$compiler_source" "$runtime" "$distribution" "$toolchain" "$workflow"
fi
