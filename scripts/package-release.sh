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

build_ref=HEAD
if git -C "$root" rev-parse --verify --quiet "${VERSION}^{commit}" >/dev/null; then
  build_ref=$VERSION
fi
commit=$(git -C "$root" rev-parse --short "$build_ref")
source_date_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$root" show -s --format=%ct "$build_ref")}
if [[ ! "$source_date_epoch" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a non-negative integer" >&2
  exit 2
fi
build_date=$(python3 - "$source_date_epoch" <<'PY'
import datetime
import sys

print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
)
ldflags="-s -w -X main.version=$VERSION -X main.commit=$commit -X main.date=$build_date"
(
  cd "$root"
  CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go build -tags fts5 -trimpath \
    -ldflags "$ldflags" -o "$stage/package/daimon" ./cmd/daimon
)
cp "$root/LICENSE" "$root/README.md" "$stage/package/"
python3 - "$stage/package" "$output/daimon_${GOOS}_${GOARCH}.tar.gz" "$source_date_epoch" <<'PY'
import gzip
import pathlib
import sys
import tarfile

source = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2])
timestamp = int(sys.argv[3])

with destination.open("wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=timestamp) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
            for name in sorted(("daimon", "LICENSE", "README.md")):
                info = archive.gettarinfo(str(source / name), arcname=name)
                info.mtime = timestamp
                info.uid = 0
                info.gid = 0
                info.uname = ""
                info.gname = ""
                with (source / name).open("rb") as item:
                    archive.addfile(info, item)
PY
