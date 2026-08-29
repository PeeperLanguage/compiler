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
  case "$file" in
    README.md|docs/*|*.md) ;;
    *) docs_only=false ;;
  esac
  case "$file" in
    .github/workflows/*) workflow=true ;;
  esac
  case "$file" in
    runtime/*|internal/backend/*|internal/codegen/*) runtime=true ;;
  esac
  case "$file" in
    distribution/*|scripts/package-release.sh|cmd/distpack/*|cmd/release-index/*|cmd/sign-release/*|internal/distribution/*|internal/installer/*) distribution=true ;;
  esac
  case "$file" in
    distribution/toolchains*|scripts/build-toolchain.sh|internal/toolchain/*) toolchain=true ;;
  esac
  case "$file" in
    *.go|*.c|*.h|*.inc|x_test/*) compiler_source=true ;;
  esac
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
