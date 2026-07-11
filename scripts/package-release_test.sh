#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

goos=$(go env GOOS)
goarch=$(go env GOARCH)
version=test-version
archive="$tmp/daimon_${goos}_${goarch}.tar.gz"

GOOS="$goos" GOARCH="$goarch" VERSION="$version" OUTPUT_DIR="$tmp" \
  "$root/scripts/package-release.sh"

test -f "$archive"
tar -C "$tmp" -xzf "$archive"
test -x "$tmp/daimon"
test -f "$tmp/LICENSE"
test -f "$tmp/README.md"
"$tmp/daimon" version | grep -F "daimon $version"
