#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: scripts/smoke-release.sh <tag> [owner/repository]}
repo=${2:-Forest-Isle/daimon}
uname_bin=${DAIMON_SMOKE_TEST_UNAME:-uname}
if [[ -n ${DAIMON_SMOKE_TEST_PATH:-} ]]; then
  PATH=$DAIMON_SMOKE_TEST_PATH
fi
tmp_parent=${DAIMON_SMOKE_TEST_TMPDIR:-${TMPDIR:-/tmp}}
stage='host detection'
work=
failure_reported=0
cleanup() {
  status=$?
  if [[ -n ${work:-} ]]; then
    rm -rf "$work"
  fi
  if ((status != 0 && failure_reported == 0)); then
    printf 'smoke-release: %s failed\n' "$stage" >&2
  fi
  exit "$status"
}
interrupt() {
  status=$1
  failure_reported=1
  printf 'smoke-release: %s failed\n' "$stage" >&2
  exit "$status"
}
trap cleanup EXIT
trap 'interrupt 129' HUP
trap 'interrupt 130' INT
trap 'interrupt 143' TERM

os=$("$uname_bin" -s)
arch=$("$uname_bin" -m)
case "$os" in
  Darwin) release_os=darwin ;;
  Linux) release_os=linux ;;
  *) printf 'smoke-release: unsupported host: %s/%s\n' "$os" "$arch" >&2; exit 2 ;;
esac
case "$arch" in
  x86_64|amd64) release_arch=amd64 ;;
  arm64|aarch64) release_arch=arm64 ;;
  *) printf 'smoke-release: unsupported host: %s/%s\n' "$os" "$arch" >&2; exit 2 ;;
esac

archive="daimon_${release_os}_${release_arch}.tar.gz"
work=$(mktemp -d "$tmp_parent/daimon-smoke.XXXXXX")

stage='release download'
gh release download "$tag" --repo "$repo" --dir "$work" \
  --pattern "$archive" --pattern checksums.txt
test -f "$work/$archive"
test -f "$work/checksums.txt"

stage='checksum validation'
checksum_line=
matches=0
while IFS= read -r line || [[ -n $line ]]; do
  if [[ ! $line =~ ^([0-9A-Fa-f]{64})[[:space:]][\ \*](.+)$ ]]; then
    printf 'smoke-release: malformed checksum entry: %s\n' "$line" >&2
    exit 1
  fi
  if [[ ${BASH_REMATCH[2]} == "$archive" ]]; then
    checksum_line="${BASH_REMATCH[1]}  $archive"
    matches=$((matches + 1))
  fi
done <"$work/checksums.txt"
if ((matches != 1)); then
  printf 'smoke-release: expected one checksum for %s, found %d\n' "$archive" "$matches" >&2
  exit 1
fi
printf '%s\n' "$checksum_line" >"$work/selected-checksum.txt"
(
  cd "$work"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c selected-checksum.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c selected-checksum.txt
  else
    printf 'smoke-release: neither sha256sum nor shasum is available\n' >&2
    exit 1
  fi
)

stage='archive inspection'
python3 - "$work/$archive" <<'PY'
import pathlib
import sys
import tarfile

expected = {"daimon", "LICENSE", "README.md"}
with tarfile.open(sys.argv[1], "r:gz") as archive:
    members = archive.getmembers()
    names = [member.name for member in members]
    for member in members:
        path = pathlib.PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts or not member.isfile():
            raise SystemExit(f"unsafe archive member: {member.name}")
    if len(names) != 3 or set(names) != expected:
        raise SystemExit(f"unexpected archive members: {names}")
PY

stage='archive extraction'
mkdir "$work/extract"
tar -xzf "$work/$archive" -C "$work/extract"
test -f "$work/extract/daimon"
test -x "$work/extract/daimon"

stage='version validation'
version_output=$("$work/extract/daimon" version)
if [[ ! $version_output =~ ^daimon[[:space:]]+([^[:space:]]+)[[:space:]]+\(commit:[[:space:]]+[^,]+,[[:space:]]+built:[[:space:]]+[^\)]+\)$ ]]; then
  printf 'smoke-release: malformed version output: %s\n' "$version_output" >&2
  exit 1
fi
actual=${BASH_REMATCH[1]}
if [[ $actual != "$tag" ]]; then
  printf 'smoke-release: version validation failed: expected %s, got %s\n' "$tag" "$actual" >&2
  exit 1
fi

printf 'tag: %s\n' "$tag"
printf 'archive: %s\n' "$archive"
printf 'checksum: verified\n'
printf '%s\n' "$version_output"
