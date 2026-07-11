#!/usr/bin/env bash
set -euo pipefail
: "${GOOS:?GOOS is required}"
: "${GOARCH:?GOARCH is required}"
: "${VERSION:?VERSION is required}"
: "${OUTPUT_DIR:?OUTPUT_DIR is required}"

root=$(git rev-parse --show-toplevel)
output=$(mkdir -p "$OUTPUT_DIR" && cd "$OUTPUT_DIR" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/package"

commit=$(git -C "$root" rev-parse --short HEAD)
ldflags="-s -w -X main.version=$VERSION -X main.commit=$commit -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
(
  cd "$root"
  CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go build -tags fts5 -trimpath \
    -ldflags "$ldflags" -o "$stage/package/daimon" ./cmd/daimon
)
cp "$root/LICENSE" "$root/README.md" "$stage/package/"
tar -C "$stage/package" -czf "$output/daimon_${GOOS}_${GOARCH}.tar.gz" daimon LICENSE README.md
