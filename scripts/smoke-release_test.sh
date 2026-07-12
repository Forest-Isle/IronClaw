#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

pass=0
run_case() {
  name=$1
  shift
  case_dir="$tmp/$name"
  mkdir -p "$case_dir/bin" "$case_dir/assets" "$case_dir/work"
  "$@" "$case_dir"
  pass=$((pass + 1))
}

write_uname() {
  case_dir=$1 os=$2 arch=$3
  cat >"$case_dir/bin/uname" <<EOF
#!/usr/bin/env bash
case "\${1:-}" in
  -s) printf '%s\n' '$os' ;;
  -m) printf '%s\n' '$arch' ;;
  *) exit 2 ;;
esac
EOF
  chmod +x "$case_dir/bin/uname"
}

write_archive() {
  case_dir=$1 archive=$2 version=$3 mode=${4:-safe}
  stage="$case_dir/stage"
  rm -rf "$stage"
  mkdir -p "$stage"
  cat >"$stage/daimon" <<EOF
#!/usr/bin/env bash
set -euo pipefail
if [[ \$# -ne 1 || \$1 != version ]]; then
  exit 64
fi
printf '%s\n' 'daimon $version (commit: fixture, built: 2026-07-12T00:00:00Z)'
EOF
  chmod +x "$stage/daimon"
  printf 'license\n' >"$stage/LICENSE"
  printf 'readme\n' >"$stage/README.md"
  case "$mode" in
    safe) COPYFILE_DISABLE=1 tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md ;;
    nonexec)
      chmod -x "$stage/daimon"
      COPYFILE_DISABLE=1 tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md
      ;;
    unexpected)
      printf 'unexpected\n' >"$stage/extra"
      COPYFILE_DISABLE=1 tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md extra
      ;;
    traversal)
      python3 - "$case_dir/assets/$archive" "$stage" <<'PY'
import io
import os
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    for name in ("daimon", "LICENSE", "README.md"):
        archive.add(os.path.join(sys.argv[2], name), arcname=name)
    info = tarfile.TarInfo("../daimon")
    info.mode = 0o755
    data = b"unsafe\n"
    info.size = len(data)
    archive.addfile(info, io.BytesIO(data))
PY
      ;;
    symlink)
      python3 - "$case_dir/assets/$archive" "$stage" <<'PY'
import os
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    for name in ("LICENSE", "README.md"):
        archive.add(os.path.join(sys.argv[2], name), arcname=name)
    info = tarfile.TarInfo("daimon")
    info.type = tarfile.SYMTYPE
    info.linkname = "/bin/true"
    archive.addfile(info)
PY
      ;;
    absolute)
      python3 - "$case_dir/assets/$archive" "$stage" <<'PY'
import io
import os
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    for name in ("daimon", "LICENSE", "README.md"):
        archive.add(os.path.join(sys.argv[2], name), arcname=name)
    info = tarfile.TarInfo("/daimon")
    data = b"unsafe\n"
    info.size = len(data)
    archive.addfile(info, io.BytesIO(data))
PY
      ;;
    hardlink)
      python3 - "$case_dir/assets/$archive" "$stage" <<'PY'
import os
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    for name in ("LICENSE", "README.md"):
        archive.add(os.path.join(sys.argv[2], name), arcname=name)
    info = tarfile.TarInfo("daimon")
    info.type = tarfile.LNKTYPE
    info.linkname = "README.md"
    archive.addfile(info)
PY
      ;;
    device)
      python3 - "$case_dir/assets/$archive" "$stage" <<'PY'
import os
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    for name in ("LICENSE", "README.md"):
        archive.add(os.path.join(sys.argv[2], name), arcname=name)
    info = tarfile.TarInfo("daimon")
    info.type = tarfile.CHRTYPE
    info.devmajor = 1
    info.devminor = 3
    archive.addfile(info)
PY
      ;;
  esac
}

write_checksums() {
  case_dir=$1 archive=$2 mode=${3:-valid}
  hash=$(shasum -a 256 "$case_dir/assets/$archive" | awk '{print $1}')
  case "$mode" in
    valid) printf '%s  %s\n' "$hash" "$archive" >"$case_dir/assets/checksums.txt" ;;
    mismatch) printf '%064d  %s\n' 0 "$archive" >"$case_dir/assets/checksums.txt" ;;
    missing) printf '%s  %s\n' "$hash" other.tar.gz >"$case_dir/assets/checksums.txt" ;;
    duplicate) printf '%s  %s\n%s  %s\n' "$hash" "$archive" "$hash" "$archive" >"$case_dir/assets/checksums.txt" ;;
    malformed) printf 'not-a-checksum  %s\n' "$archive" >"$case_dir/assets/checksums.txt" ;;
  esac
}

write_gh() {
  case_dir=$1
  cat >"$case_dir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$GH_CALLS"
test "$1 $2" = "release download"
tag=$3
shift 3
repo=
dir=
patterns=()
while (($#)); do
  case "$1" in
    --repo) repo=$2; shift 2 ;;
    --dir) dir=$2; shift 2 ;;
    --pattern) patterns+=("$2"); shift 2 ;;
    *) exit 2 ;;
  esac
done
test "$tag" = "$EXPECTED_TAG"
test "$repo" = "$EXPECTED_REPO"
if [[ -n ${GH_SIGNAL:-} ]]; then
  kill "-$GH_SIGNAL" "$PPID"
  exit 0
fi
for pattern in "${patterns[@]}"; do
  cp "$FIXTURE_ASSETS/$pattern" "$dir/$pattern"
done
EOF
  chmod +x "$case_dir/bin/gh"
}

invoke() {
  case_dir=$1
  shift
  env \
    DAIMON_SMOKE_TEST_UNAME="$case_dir/bin/uname" \
    DAIMON_SMOKE_TEST_PATH="$case_dir/bin:/usr/bin:/bin" \
    DAIMON_SMOKE_TEST_TMPDIR="$case_dir/work" \
    GH_CALLS="$case_dir/gh.calls" \
    FIXTURE_ASSETS="$case_dir/assets" \
    EXPECTED_TAG=v0.1.0 \
    EXPECTED_REPO=Forest-Isle/daimon \
    GH_SIGNAL=${GH_SIGNAL:-} \
    "$root/scripts/smoke-release.sh" v0.1.0 Forest-Isle/daimon "$@"
}

case_success() {
  case_dir=$1
  write_uname "$case_dir" Darwin arm64
  write_gh "$case_dir"
  write_archive "$case_dir" daimon_darwin_arm64.tar.gz v0.1.0
  write_checksums "$case_dir" daimon_darwin_arm64.tar.gz
  output=$(invoke "$case_dir")
  grep -F 'tag: v0.1.0' <<<"$output"
  grep -F 'archive: daimon_darwin_arm64.tar.gz' <<<"$output"
  grep -F 'checksum: verified' <<<"$output"
  grep -F 'daimon v0.1.0 (commit: fixture, built: 2026-07-12T00:00:00Z)' <<<"$output"
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  env \
    DAIMON_SMOKE_TEST_UNAME="$case_dir/bin/uname" \
    DAIMON_SMOKE_TEST_PATH="$case_dir/bin:/usr/bin:/bin" \
    DAIMON_SMOKE_TEST_TMPDIR="$case_dir/work" \
    GH_CALLS="$case_dir/gh.calls" \
    FIXTURE_ASSETS="$case_dir/assets" \
    EXPECTED_TAG=v0.1.0 \
    EXPECTED_REPO=Forest-Isle/daimon \
    "$root/scripts/smoke-release.sh" v0.1.0 >/dev/null
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
}

case_mapping() {
  case_dir=$1
  for mapping in 'Darwin x86_64 darwin amd64' 'Darwin arm64 darwin arm64' 'Linux amd64 linux amd64' 'Linux aarch64 linux arm64'; do
    set -- $mapping
    os=$1 arch=$2 release_os=$3 release_arch=$4
    rm -rf "$case_dir/assets" "$case_dir/work"
    mkdir -p "$case_dir/assets" "$case_dir/work"
    write_uname "$case_dir" "$os" "$arch"
    write_gh "$case_dir"
    archive="daimon_${release_os}_${release_arch}.tar.gz"
    write_archive "$case_dir" "$archive" v0.1.0
    write_checksums "$case_dir" "$archive"
    invoke "$case_dir" >/dev/null
    tail -1 "$case_dir/gh.calls" | grep -F -- "--pattern $archive"
    test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  done
}

case_checksum_failure() {
  case_dir=$1
  write_uname "$case_dir" Linux amd64
  write_gh "$case_dir"
  archive=daimon_linux_amd64.tar.gz
  write_archive "$case_dir" "$archive" v0.1.0
  for mode in mismatch missing duplicate malformed; do
    write_checksums "$case_dir" "$archive" "$mode"
    if invoke "$case_dir" >"$case_dir/out" 2>&1; then
      echo "checksum $mode unexpectedly succeeded" >&2
      exit 1
    fi
    grep -F 'checksum validation failed' "$case_dir/out"
    test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  done
}

case_unsafe_archive() {
  case_dir=$1
  write_uname "$case_dir" Linux amd64
  write_gh "$case_dir"
  archive=daimon_linux_amd64.tar.gz
  for mode in unexpected traversal symlink absolute hardlink device; do
    write_archive "$case_dir" "$archive" v0.1.0 "$mode"
    write_checksums "$case_dir" "$archive"
    if invoke "$case_dir" >"$case_dir/out" 2>&1; then
      echo "unsafe archive $mode unexpectedly succeeded" >&2
      exit 1
    fi
    case "$mode" in
      unexpected) grep -F "unexpected archive members:" "$case_dir/out" ;;
      traversal) grep -F "unsafe archive member: ../daimon" "$case_dir/out" ;;
      absolute) grep -F "unsafe archive member: /daimon" "$case_dir/out" ;;
      symlink|hardlink|device) grep -F "unsafe archive member: daimon" "$case_dir/out" ;;
    esac
    grep -F 'archive inspection failed' "$case_dir/out"
    test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  done
}

case_signals() {
  case_dir=$1
  write_uname "$case_dir" Darwin arm64
  write_gh "$case_dir"
  for spec in 'HUP 129' 'INT 130' 'TERM 143'; do
    set -- $spec
    signal=$1 expected=$2
    status=0
    GH_SIGNAL=$signal invoke "$case_dir" >"$case_dir/out" 2>&1 || status=$?
    test "$status" -eq "$expected"
    test "$(grep -Fc 'smoke-release: release download failed' "$case_dir/out")" -eq 1
    test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  done
}

case_unsupported_host() {
  case_dir=$1
  write_uname "$case_dir" FreeBSD riscv64
  write_gh "$case_dir"
  if invoke "$case_dir" >"$case_dir/out" 2>&1; then
    echo 'unsupported host unexpectedly succeeded' >&2
    exit 1
  fi
  grep -F 'unsupported host: FreeBSD/riscv64' "$case_dir/out"
  test ! -e "$case_dir/gh.calls"
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
}

case_version_mismatch() {
  case_dir=$1
  write_uname "$case_dir" Darwin arm64
  write_gh "$case_dir"
  archive=daimon_darwin_arm64.tar.gz
  write_archive "$case_dir" "$archive" v0.0.9
  write_checksums "$case_dir" "$archive"
  if invoke "$case_dir" >"$case_dir/out" 2>&1; then
    echo 'version mismatch unexpectedly succeeded' >&2
    exit 1
  fi
  grep -F 'version validation failed: expected v0.1.0, got v0.0.9' "$case_dir/out"
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
}

case_download_failure() {
  case_dir=$1
  write_uname "$case_dir" Darwin arm64
  write_gh "$case_dir"
  archive=daimon_darwin_arm64.tar.gz
  write_archive "$case_dir" "$archive" v0.1.0
  write_checksums "$case_dir" "$archive"
  for missing in "$archive" checksums.txt; do
    mv "$case_dir/assets/$missing" "$case_dir/$missing"
    if invoke "$case_dir" >"$case_dir/out" 2>&1; then
      echo "missing $missing unexpectedly succeeded" >&2
      exit 1
    fi
    grep -F 'release download failed' "$case_dir/out"
    mv "$case_dir/$missing" "$case_dir/assets/$missing"
    test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
  done
}

case_no_checksum_tool() {
  case_dir=$1
  write_uname "$case_dir" Darwin arm64
  write_gh "$case_dir"
  archive=daimon_darwin_arm64.tar.gz
  write_archive "$case_dir" "$archive" v0.1.0
  write_checksums "$case_dir" "$archive"
  tools="$case_dir/no-checksum-bin"
  mkdir -p "$tools"
  for command_path in /bin/bash /bin/cp /bin/rm /usr/bin/mktemp; do
    ln -s "$command_path" "$tools/$(basename "$command_path")"
  done
  ln -s "$case_dir/bin/gh" "$tools/gh"
  ln -s "$case_dir/bin/uname" "$tools/uname"
  if env \
    DAIMON_SMOKE_TEST_UNAME="$tools/uname" \
    DAIMON_SMOKE_TEST_PATH="$tools" \
    DAIMON_SMOKE_TEST_TMPDIR="$case_dir/work" \
    GH_CALLS="$case_dir/gh.calls" \
    FIXTURE_ASSETS="$case_dir/assets" \
    EXPECTED_TAG=v0.1.0 \
    EXPECTED_REPO=Forest-Isle/daimon \
    "$root/scripts/smoke-release.sh" v0.1.0 Forest-Isle/daimon \
    >"$case_dir/out" 2>&1; then
    echo 'missing checksum tools unexpectedly succeeded' >&2
    exit 1
  fi
  grep -F 'neither sha256sum nor shasum is available' "$case_dir/out"
  grep -F 'checksum validation failed' "$case_dir/out"
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
}

case_non_executable() {
  case_dir=$1
  write_uname "$case_dir" Linux amd64
  write_gh "$case_dir"
  archive=daimon_linux_amd64.tar.gz
  write_archive "$case_dir" "$archive" v0.1.0 nonexec
  write_checksums "$case_dir" "$archive"
  if invoke "$case_dir" >"$case_dir/out" 2>&1; then
    echo 'non-executable binary unexpectedly succeeded' >&2
    exit 1
  fi
  grep -F 'archive extraction failed' "$case_dir/out"
  test -z "$(find "$case_dir/work" -mindepth 1 -print -quit)"
}

run_case success case_success
run_case mapping case_mapping
run_case checksum_failure case_checksum_failure
run_case unsafe_archive case_unsafe_archive
run_case signals case_signals
run_case unsupported_host case_unsupported_host
run_case version_mismatch case_version_mismatch
run_case download_failure case_download_failure
run_case no_checksum_tool case_no_checksum_tool
run_case non_executable case_non_executable
printf 'smoke-release tests: %d cases passed\n' "$pass"
