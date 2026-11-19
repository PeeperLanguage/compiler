#!/usr/bin/env bash
# Prints each open peeper-bot pull request titled TITLE: "same N" when its
# FILE equals the working tree's, "other N" when it does not. A toolchain
# workflow reads it before opening a pull request, so two runs that pin the
# same thing open one.
#
# Usage: scripts/open-bot-prs.sh FILE TITLE (needs GH_TOKEN, GITHUB_REPOSITORY)
set -euo pipefail
file=$1
title=$2
gh pr list --repo "$GITHUB_REPOSITORY" --state open --limit 100 \
    --json number,title,headRefName,headRefOid \
    --jq ".[] | select(.title == \"$title\" and (.headRefName | startswith(\"automation/\")))
        | \"\(.number) \(.headRefOid)\"" |
    while read -r number sha; do
        if gh api -H "Accept: application/vnd.github.raw" \
            "repos/$GITHUB_REPOSITORY/contents/$file?ref=$sha" | cmp -s - "$file"; then
            echo "same $number"
        else
            echo "other $number"
        fi
    done
