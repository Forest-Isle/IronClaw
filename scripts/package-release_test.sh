#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

goos=$(go env GOOS)
goarch=$(go env GOARCH)
version=test-version
archive_one="$tmp/one/daimon_${goos}_${goarch}.tar.gz"
archive_two="$tmp/two/daimon_${goos}_${goarch}.tar.gz"
archive_default="$tmp/default/daimon_${goos}_${goarch}.tar.gz"

SOURCE_DATE_EPOCH=1700000000 GOOS="$goos" GOARCH="$goarch" VERSION="$version" OUTPUT_DIR="$tmp/one" \
  "$root/scripts/package-release.sh"
SOURCE_DATE_EPOCH=1700000000 GOOS="$goos" GOARCH="$goarch" VERSION="$version" OUTPUT_DIR="$tmp/two" \
  "$root/scripts/package-release.sh"
GOOS="$goos" GOARCH="$goarch" VERSION="$version" OUTPUT_DIR="$tmp/default" \
  "$root/scripts/package-release.sh"

test -f "$archive_one"
python3 - "$archive_one" "$archive_two" "$archive_default" "$(git show -s --format=%ct HEAD)" <<'PY'
import gzip
import hashlib
import sys
import tarfile

archive = sys.argv[1]
second_archive = sys.argv[2]
default_archive = sys.argv[3]
commit_timestamp = int(sys.argv[4])
with open(archive, "rb") as first, open(second_archive, "rb") as second:
    assert hashlib.sha256(first.read()).digest() == hashlib.sha256(second.read()).digest()
with open(archive, "rb") as raw:
    assert raw.read(4)[3] & 0x08 == 0
    raw.seek(0)
    with gzip.GzipFile(fileobj=raw) as compressed:
        compressed.read(1)
        assert compressed.mtime == 1700000000
with tarfile.open(archive, "r:gz") as package:
    members = package.getmembers()
    assert [member.name for member in members] == ["LICENSE", "README.md", "daimon"]
    for member in members:
        assert member.mtime == 1700000000
        assert (member.uid, member.gid, member.uname, member.gname) == (0, 0, "", "")
with tarfile.open(default_archive, "r:gz") as package:
    assert all(member.mtime == commit_timestamp for member in package.getmembers())
PY
tar -C "$tmp" -xzf "$archive_one"
test -x "$tmp/daimon"
test -f "$tmp/LICENSE"
test -f "$tmp/README.md"
"$tmp/daimon" version | grep -F "daimon $version"
