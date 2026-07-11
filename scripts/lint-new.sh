#!/usr/bin/env bash
set -euo pipefail

base=${1:-${LINT_BASE_SHA:-${GITHUB_BASE_SHA:-${GITHUB_EVENT_BEFORE:-}}}}
if [[ -z "$base" ]]; then
  base=$(git merge-base HEAD main)
fi
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  echo "lint-new: invalid base commit: $base" >&2
  exit 2
fi
exec golangci-lint run --new-from-rev "$base" ./...
