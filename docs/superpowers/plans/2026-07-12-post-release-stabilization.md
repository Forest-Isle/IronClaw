# Post-release Stabilization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fail-closed, host-native release smoke test, move every repository-owned JavaScript action to a maintained Node.js 24-compatible release, and protect `main` with an active ruleset whose nine required contexts have first been observed on the stabilization pull request.

**Architecture:** Keep release verification in one Bash entry point with a fully hermetic fake-`gh` test harness and a temporary extraction directory. Upgrade workflow references without changing triggers, permissions, job names, matrices, inputs, artifact names, or failure policy; prove runtime compatibility from upstream action metadata and the pull-request run. Treat the GitHub ruleset as the final external mutation: export prior state, build its payload only from observed check runs, read it back, exercise its behavior, and restore the prior payload on any failure.

**Tech Stack:** Bash 3.2-compatible shell, Python 3 standard library (`tarfile`) for portable archive inspection, Make, GitHub Actions YAML, `actionlint`, `gh` CLI, `jq`, GitHub REST and GraphQL APIs.

## Global Constraints

- Work only in `/Users/wuqisen/dev/Daimon/.worktrees/post-release-stabilization` on the worktree's feature branch.
- Do not change Daimon runtime behavior, release archive contents, release tags/assets, or unrelated dependencies.
- `scripts/smoke-release.sh` defaults only the repository to `Forest-Isle/daimon`; its tag argument is mandatory, and automation passes both tag and repository explicitly.
- The smoke test must fail before execution for a missing asset, malformed/duplicate/mismatched checksum, unsafe archive, unsupported host, non-executable binary, or version mismatch.
- The smoke test must never start Gateway, read the operator's Daimon home, write repository files, retry another platform/release, or retain downloaded artifacts.
- Test-only host and command injection is limited to `DAIMON_SMOKE_TEST_UNAME`, `DAIMON_SMOKE_TEST_PATH`, and `DAIMON_SMOKE_TEST_TMPDIR`, documented as unsupported operator interfaces.
- Preserve every workflow trigger, permission, job display name, matrix, input, artifact name, and `continue-on-error`/failure policy.
- Keep `Lint`, `Build`, coverage, and scheduled security jobs non-required; keep `Incremental Lint`, `Layer Boundaries`, `Test`, `Eval Gate`, `Vet`, and all four `Package <os>/<arch>` jobs blocking.
- Do not infer Node.js compatibility from `actionlint`; inspect the selected upstream `action.yml`/nested action metadata and require `runs.using: node24` for every JavaScript action.
- Never guess a status-check context. The ruleset payload may use a context only after the exact name is observed successfully on the current pull-request head.
- The `main` ruleset is the final external change. Before mutation export every current repository ruleset in full; on payload, read-back, plan-support, or behavior failure restore the prior named-ruleset payload (or delete only the newly created ruleset when none existed).
- Never delete or modify an unrelated repository or organization ruleset.
- Use TDD for the smoke script: observe the hermetic suite RED before adding the production script, then GREEN.
- Each repository change is committed before the pull request is opened; ruleset API payloads, downloaded release assets, and temporary probe files are not committed.

---

### Task 1: Hermetic Release Smoke Contract

**Files:**
- Create: `scripts/smoke-release_test.sh`
- Create: `scripts/smoke-release.sh`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Consumes: `scripts/smoke-release.sh <tag> [owner/repository]`, default repository `Forest-Isle/daimon`; `gh release download`; current `uname -s`/`uname -m`; `sha256sum` or `shasum -a 256`; Python 3 `tarfile`; released binary output `daimon <version> (commit: <commit>, built: <date>)`.
- Produces: `make smoke-release-test`; selected asset `daimon_<darwin|linux>_<amd64|arm64>.tar.gz`; stage-prefixed failures; success output containing tag, archive, `checksum: verified`, and the exact version line.
- Test-only injection: `DAIMON_SMOKE_TEST_UNAME` is an executable replacing `uname`, `DAIMON_SMOKE_TEST_PATH` replaces `PATH`, and `DAIMON_SMOKE_TEST_TMPDIR` is the parent passed to `mktemp -d`; production leaves all three unset.

- [ ] **Step 1: Write the failing hermetic suite**

Create `scripts/smoke-release_test.sh`:

```bash
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
printf '%s\n' 'daimon $version (commit: fixture, built: 2026-07-12T00:00:00Z)'
EOF
  chmod +x "$stage/daimon"
  printf 'license\n' >"$stage/LICENSE"
  printf 'readme\n' >"$stage/README.md"
  case "$mode" in
    safe) tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md ;;
    nonexec)
      chmod -x "$stage/daimon"
      tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md
      ;;
    unexpected)
      printf 'unexpected\n' >"$stage/extra"
      tar -C "$stage" -czf "$case_dir/assets/$archive" daimon LICENSE README.md extra
      ;;
    traversal)
      python3 - "$case_dir/assets/$archive" <<'PY'
import io
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    info = tarfile.TarInfo("../daimon")
    info.mode = 0o755
    data = b"unsafe\n"
    info.size = len(data)
    archive.addfile(info, io.BytesIO(data))
PY
      ;;
    symlink)
      python3 - "$case_dir/assets/$archive" <<'PY'
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    info = tarfile.TarInfo("daimon")
    info.type = tarfile.SYMTYPE
    info.linkname = "/bin/true"
    archive.addfile(info)
PY
      ;;
    absolute)
      python3 - "$case_dir/assets/$archive" <<'PY'
import io
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    info = tarfile.TarInfo("/daimon")
    data = b"unsafe\n"
    info.size = len(data)
    archive.addfile(info, io.BytesIO(data))
PY
      ;;
    hardlink)
      python3 - "$case_dir/assets/$archive" <<'PY'
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
    info = tarfile.TarInfo("daimon")
    info.type = tarfile.LNKTYPE
    info.linkname = "README.md"
    archive.addfile(info)
PY
      ;;
    device)
      python3 - "$case_dir/assets/$archive" <<'PY'
import tarfile
import sys

with tarfile.open(sys.argv[1], "w:gz") as archive:
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
    grep -F 'archive inspection failed' "$case_dir/out"
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
run_case unsupported_host case_unsupported_host
run_case version_mismatch case_version_mismatch
run_case download_failure case_download_failure
run_case no_checksum_tool case_no_checksum_tool
run_case non_executable case_non_executable
printf 'smoke-release tests: %d cases passed\n' "$pass"
```

- [ ] **Step 2: Run the suite and observe RED**

Run: `bash scripts/smoke-release_test.sh`

Expected: FAIL at the first invocation with `scripts/smoke-release.sh: No such file or directory`; the fake `gh` never contacts GitHub.

- [ ] **Step 3: Implement the minimal fail-closed smoke script**

Create `scripts/smoke-release.sh`:

```bash
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
cleanup() {
  status=$?
  if [[ -n ${work:-} ]]; then
    rm -rf "$work"
  fi
  if ((status != 0)); then
    printf 'smoke-release: %s failed\n' "$stage" >&2
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

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
    if len(names) != 3 or set(names) != expected:
        raise SystemExit(f"unexpected archive members: {names}")
    for member in members:
        path = pathlib.PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts or not member.isfile():
            raise SystemExit(f"unsafe archive member: {member.name}")
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
```

- [ ] **Step 4: Expose the suite through Make and document operator/test commands**

Add `smoke-release-test` to `.PHONY` and add this target after `package-test` in `Makefile`:

```make
## smoke-release-test: Test release smoke validation without network access
smoke-release-test:
	bash scripts/smoke-release_test.sh
```

Replace the manual download block in `README.md`'s `Release archives` section with:

````markdown
Verify the published archive for the current host (requires authenticated `gh`):

```bash
scripts/smoke-release.sh v0.1.0 Forest-Isle/daimon
```

The command downloads into a temporary directory, verifies the selected checksum,
rejects unsafe or unexpected tar members, and requires `daimon version` to report
the requested tag. It never starts Daimon or reads `~/.daimon`. A failure names the
stage (`host detection`, `release download`, `checksum validation`, `archive
inspection`, `archive extraction`, or `version validation`) and leaves no archive
behind.

Run the credential-free, network-free test suite separately:

```bash
make smoke-release-test
```

`DAIMON_SMOKE_TEST_UNAME`, `DAIMON_SMOKE_TEST_PATH`, and
`DAIMON_SMOKE_TEST_TMPDIR` exist only for the hermetic suite and are not supported
operator configuration.
````

- [ ] **Step 5: Run GREEN and repository hygiene checks**

Run:

```bash
chmod +x scripts/smoke-release.sh scripts/smoke-release_test.sh
make smoke-release-test
git diff --check
git status --short
```

Expected: `smoke-release tests: 9 cases passed`; success/failure cleanup assertions pass; only `scripts/smoke-release.sh`, `scripts/smoke-release_test.sh`, `Makefile`, and `README.md` are changed.

- [ ] **Step 6: Commit the independently reviewable smoke slice**

```bash
git add scripts/smoke-release.sh scripts/smoke-release_test.sh Makefile README.md
git commit -m "test: add hermetic release smoke verification"
```

---

### Task 2: Published `v0.1.0` Host Smoke and Delivery Evidence

**Files:**
- Create: `docs/superpowers/reports/2026-07-12-post-release-stabilization.md`

**Interfaces:**
- Consumes: committed `scripts/smoke-release.sh`; published `Forest-Isle/daimon` release `v0.1.0`; authenticated `gh`; the executor's real Darwin/arm64 host.
- Produces: a committed Markdown record containing the real tag, selected `daimon_darwin_arm64.tar.gz`, checksum result, and unedited `daimon version` output; no downloaded asset is retained.

- [ ] **Step 1: Prove the release and expected assets exist before executing anything**

Run:

```bash
gh release view v0.1.0 --repo Forest-Isle/daimon \
  --json tagName,isDraft,isPrerelease,url,assets \
  --jq '{tagName,isDraft,isPrerelease,url,assets:[.assets[].name]}'
```

Expected: tag `v0.1.0`; assets include exactly the four `daimon_<os>_<arch>.tar.gz` files plus `checksums.txt`; this command does not download or execute an asset.

- [ ] **Step 2: Run the real host-native smoke and capture its output**

Run:

```bash
evidence=$(mktemp)
trap 'rm -f "$evidence"' EXIT
scripts/smoke-release.sh v0.1.0 Forest-Isle/daimon | tee "$evidence"
test "$(sed -n 's/^archive: //p' "$evidence")" = daimon_darwin_arm64.tar.gz
grep -Fx 'checksum: verified' "$evidence"
grep -E '^daimon v0\.1\.0 \(commit: [^,]+, built: [^)]+\)$' "$evidence"
git status --short
```

Expected: all commands exit 0; output identifies `v0.1.0`, `daimon_darwin_arm64.tar.gz`, a verified checksum, and exact `v0.1.0` binary output; repository status contains no downloaded artifact.

- [ ] **Step 3: Create the delivery report from observed values**

Run:

```bash
mkdir -p docs/superpowers/reports
{
  printf '# Post-release Stabilization Delivery Report\n\n'
  printf '## Published release smoke\n\n'
  printf -- '- Repository: `Forest-Isle/daimon`\n'
  printf -- '- Executed at (UTC): `%s`\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  sed 's/^/- /' "$evidence"
} >docs/superpowers/reports/2026-07-12-post-release-stabilization.md
```

Expected: the report contains only completed smoke evidence and makes no claim about work that has not run.

- [ ] **Step 4: Commit the release evidence**

```bash
git add docs/superpowers/reports/2026-07-12-post-release-stabilization.md
git commit -m "docs: record v0.1.0 release smoke"
```

---

### Task 3: Node.js 24 Action Inventory and Workflow Upgrade

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/coverage.yml`
- Modify: `.github/workflows/package.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/security.yml`

**Interfaces:**
- Consumes: every `uses:` under `.github/workflows`; upstream metadata at the exact selected ref; current workflow triggers, permissions, names, matrices, inputs, artifacts, and failure semantics.
- Produces: JavaScript action references whose selected metadata declares `node24`; composite/Docker references classified without rewriting them solely for runtime; no floating `@master`; clean `actionlint` across all five workflows.
- Exact inventory/decision: `actions/checkout@v4` -> `@v7` (`node24`), `actions/setup-go@v5` -> `@v6` (`node24`), `golangci/golangci-lint-action@v7` -> `@v9` (`node24`), `actions/upload-artifact@v4` -> `@v7` (`node24`), `actions/download-artifact@v4` -> `@v8` (`node24`), `madrapps/jacoco-report@v1.6.1` -> `@v1.8.0` (`node24`), `github/codeql-action/upload-sarif@v4` remains `@v4` (`node24`), `codecov/codecov-action@v4` -> `@v7` (composite; nested `actions/github-script` metadata must be `node24`), and `securego/gosec@master` -> `@v2.27.1` (Docker action, pinned away from a floating branch).

- [ ] **Step 1: Save a complete before-inventory and assert it has exactly nine unique references**

Run:

```bash
before=$(mktemp)
rg -No 'uses:\s*[^ ]+' .github/workflows \
  | sed -E 's/.*uses:[[:space:]]*//' | sort -u | tee "$before"
test "$(wc -l <"$before" | tr -d ' ')" = 9
```

Expected: the nine references listed in the task interface, including `securego/gosec@master`; no workflow is omitted.

- [ ] **Step 2: Verify maintained releases and runtime metadata before editing YAML**

Run:

```bash
metadata=$(mktemp -d)
while read -r repo path ref kind; do
  file="$metadata/${repo//\//_}-${ref//\//_}.yml"
  gh api -H 'Accept: application/vnd.github.raw+json' \
    "repos/$repo/contents/$path?ref=$ref" >"$file"
  case "$kind" in
    node24) rg -n "using:[[:space:]]*['\"]?node24" "$file" ;;
    composite) rg -n "using:[[:space:]]*['\"]?composite" "$file" ;;
    docker) rg -n "using:[[:space:]]*['\"]?docker" "$file" ;;
  esac
done <<'EOF'
actions/checkout action.yml v7 node24
actions/setup-go action.yml v6 node24
golangci/golangci-lint-action action.yml v9 node24
actions/upload-artifact action.yml v7 node24
actions/download-artifact action.yml v8 node24
madrapps/jacoco-report action.yml v1.8.0 node24
github/codeql-action upload-sarif/action.yml v4 node24
codecov/codecov-action action.yml v7 composite
securego/gosec action.yml v2.27.1 docker
EOF
rg -n 'actions/github-script@[^ ]+' "$metadata/codecov_codecov-action-v7.yml"
```

Then fetch the exact nested Codecov `actions/github-script` SHA printed by the last command and verify its metadata:

```bash
github_script_sha=$(sed -nE 's/.*actions\/github-script@([0-9a-f]{40}).*/\1/p' "$metadata/codecov_codecov-action-v7.yml" | head -1)
test -n "$github_script_sha"
gh api -H 'Accept: application/vnd.github.raw+json' \
  "repos/actions/github-script/contents/action.yml?ref=$github_script_sha" \
  | rg "using:[[:space:]]*['\"]?node24"
```

Expected: every JavaScript metadata file and Codecov's nested JavaScript action says `node24`; Codecov itself says `composite`; gosec says `docker`. If any assertion fails, stop and select a newer maintained stable release before changing workflows.

- [ ] **Step 3: Apply only the inventory substitutions**

Make these exact replacements across `.github/workflows/*.yml`:

```text
actions/checkout@v4                  -> actions/checkout@v7
actions/setup-go@v5                  -> actions/setup-go@v6
golangci/golangci-lint-action@v7     -> golangci/golangci-lint-action@v9
actions/upload-artifact@v4           -> actions/upload-artifact@v7
actions/download-artifact@v4         -> actions/download-artifact@v8
codecov/codecov-action@v4            -> codecov/codecov-action@v7
madrapps/jacoco-report@v1.6.1        -> madrapps/jacoco-report@v1.8.0
securego/gosec@master                -> securego/gosec@v2.27.1
```

Leave `github/codeql-action/upload-sarif@v4` unchanged. Do not modify any surrounding key, input, name, permission, trigger, matrix, or failure option.

- [ ] **Step 4: Verify the exact after-inventory and workflow semantics**

Run:

```bash
after=$(mktemp)
rg -No 'uses:\s*[^ ]+' .github/workflows \
  | sed -E 's/.*uses:[[:space:]]*//' | sort -u | tee "$after"
test "$(wc -l <"$after" | tr -d ' ')" = 9
cat >"$after.expected" <<'EOF'
actions/checkout@v7
actions/download-artifact@v8
actions/setup-go@v6
actions/upload-artifact@v7
codecov/codecov-action@v7
github/codeql-action/upload-sarif@v4
golangci/golangci-lint-action@v9
madrapps/jacoco-report@v1.8.0
securego/gosec@v2.27.1
EOF
diff -u "$after.expected" "$after"
! rg -n 'uses:.*@(master|main)([[:space:]]|$)' .github/workflows
git diff --word-diff=porcelain -- .github/workflows
```

Expected: only the eight substitutions appear. Manually reject the diff if it changes `on`, `permissions`, job/name fields, `continue-on-error`, matrix values, action inputs, artifact names, release tag policy, or shell commands.

- [ ] **Step 5: Run syntax and local repository gates**

Run:

```bash
actionlint .github/workflows/*.yml
make smoke-release-test
make package-test
git diff --check
```

Expected: all commands exit 0. `actionlint` proves workflow syntax only; the metadata evidence from Step 2 remains mandatory for Node.js runtime acceptance.

- [ ] **Step 6: Commit the independently reviewable workflow slice**

```bash
git add .github/workflows/ci.yml .github/workflows/coverage.yml \
  .github/workflows/package.yml .github/workflows/release.yml \
  .github/workflows/security.yml
git commit -m "ci: upgrade actions to Node.js 24 runtimes"
```

---

### Task 4: Pull-request Runs and Observed Check Contexts

**Files:**
- Modify: `docs/superpowers/reports/2026-07-12-post-release-stabilization.md`

**Interfaces:**
- Consumes: the committed Tasks 1-3 branch; GitHub pull-request runs for the current head SHA; GitHub check-run API and workflow logs.
- Produces: an open stabilization PR; proof that all nine exact required contexts succeeded on the current head; proof that repository-owned references emitted no Node.js 20 deprecation annotation; `observed-required-contexts.json` in a temporary directory for Task 5, generated from API results rather than typed into the ruleset payload.

- [ ] **Step 1: Verify the branch is clean, push it, and open the pull request**

Run:

```bash
git status --short
branch=$(git branch --show-current)
test -n "$branch"
git push -u origin "$branch"
pr_url=$(gh pr create --repo Forest-Isle/daimon \
  --base main --head "$branch" \
  --title "Post-release stabilization" \
  --body "Adds hermetic v0.1.0 release smoke verification and upgrades repository-owned actions to Node.js 24-compatible releases. The main ruleset will be applied only after all nine required contexts are observed on this PR.")
printf '%s\n' "$pr_url"
```

Expected: clean status before push; one open PR targeting `main`; no ruleset has been changed.

- [ ] **Step 2: Watch every PR workflow to completion without hiding optional failures**

Run:

```bash
pr=$(gh pr view --repo Forest-Isle/daimon --json number --jq .number)
head_sha=$(gh pr view "$pr" --repo Forest-Isle/daimon --json headRefOid --jq .headRefOid)
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10
gh pr checks "$pr" --repo Forest-Isle/daimon
```

Expected: the command displays CI, Coverage, Package, and Security check runs for `head_sha`. Blocking jobs and all four package jobs succeed; explicitly optional/non-blocking steps retain their existing behavior.

- [ ] **Step 3: Derive and validate the nine contexts from successful check runs**

Run:

```bash
evidence_dir=$(mktemp -d)
gh api --paginate "repos/Forest-Isle/daimon/commits/$head_sha/check-runs?per_page=100" \
  | jq -s '[.[].check_runs[]] | unique_by(.id)' >"$evidence_dir/check-runs.json"
jq '[.[] | select(.conclusion == "success") | .name] | unique | sort' \
  "$evidence_dir/check-runs.json" >"$evidence_dir/successful-contexts.json"
jq -n '["Incremental Lint","Layer Boundaries","Test","Eval Gate","Vet","Package linux/amd64","Package linux/arm64","Package darwin/amd64","Package darwin/arm64"] | sort' \
  >"$evidence_dir/required-design-contexts.json"
jq --slurpfile observed "$evidence_dir/successful-contexts.json" \
  '[.[] | select(. as $name | $observed[0] | index($name))]' \
  "$evidence_dir/required-design-contexts.json" \
  >"$evidence_dir/observed-required-contexts.json"
diff -u "$evidence_dir/required-design-contexts.json" \
  "$evidence_dir/observed-required-contexts.json"
jq -e 'length == 9' "$evidence_dir/observed-required-contexts.json"
```

Expected: `diff` is empty and length is nine. If a package context differs, update the future payload from the observed successful name only after reconciling the design; do not activate a guessed substitute.

- [ ] **Step 4: Prove no repository-owned action emits a Node.js 20 warning**

Run:

```bash
jq -r '.[].details_url' "$evidence_dir/check-runs.json" \
  | sed -nE 's#https://github.com/Forest-Isle/daimon/actions/runs/([0-9]+)/job/.*#\1#p' \
  | sort -u >"$evidence_dir/run-ids"
while read -r run_id; do
  gh run view "$run_id" --repo Forest-Isle/daimon --log
done <"$evidence_dir/run-ids" >"$evidence_dir/workflow.log"
if rg -i 'Node\.js 20|node20.*deprecated|deprecated.*node20' "$evidence_dir/workflow.log"; then
  echo 'Node.js 20 deprecation warning remains' >&2
  exit 1
fi
while read -r check_id; do
  gh api --paginate "repos/Forest-Isle/daimon/check-runs/$check_id/annotations?per_page=100"
done < <(jq -r '.[].id' "$evidence_dir/check-runs.json") \
  | jq -s '[.[][]]' >"$evidence_dir/annotations.json"
if jq -r '.[] | [.title,.message,.raw_details] | map(select(. != null)) | join(" ")' \
  "$evidence_dir/annotations.json" \
  | rg -i 'Node\.js 20|node20.*deprecated|deprecated.*node20'; then
  echo 'Node.js 20 deprecation annotation remains' >&2
  exit 1
fi
```

Expected: no Node.js 20 warning in complete run logs or annotations. If found, identify its owning `uses:` reference from the job log, revise only that step, and repeat Tasks 3-4 before ruleset work.

- [ ] **Step 5: Append observed action evidence to the delivery report**

Run:

```bash
report=docs/superpowers/reports/2026-07-12-post-release-stabilization.md
python3 - "$report" "$pr" "$head_sha" "$evidence_dir/observed-required-contexts.json" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
contexts = json.loads(pathlib.Path(sys.argv[4]).read_text())
lines = [
    "## GitHub Actions verification",
    "",
    f"- Pull request: `#{sys.argv[2]}`",
    f"- Verified head: `{sys.argv[3]}`",
    "- `actionlint`: passed for all five workflow files",
    "- Node.js 20 deprecation logs/annotations: none",
    "- Successful required contexts:",
    *[f"  - `{context}`" for context in contexts],
]
text = path.read_text()
if "## GitHub Actions verification" in text:
    raise SystemExit("action evidence already exists")
path.write_text(text.rstrip() + "\n\n" + "\n".join(lines) + "\n")
PY
git diff --check
```

Expected: report records the actual PR number/head SHA and the nine API-observed names.

- [ ] **Step 6: Commit and re-observe the report-only head**

```bash
git add docs/superpowers/reports/2026-07-12-post-release-stabilization.md
git commit -m "docs: record Node.js 24 workflow verification"
git push
head_sha=$(git rev-parse HEAD)
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10
```

Regenerate and compare the contexts for the new head:

```bash
gh api --paginate "repos/Forest-Isle/daimon/commits/$head_sha/check-runs?per_page=100" \
  | jq -s '[.[].check_runs[]] | unique_by(.id)' >"$evidence_dir/check-runs.json"
jq '[.[] | select(.conclusion == "success") | .name] | unique | sort' \
  "$evidence_dir/check-runs.json" >"$evidence_dir/successful-contexts.json"
jq -n '["Incremental Lint","Layer Boundaries","Test","Eval Gate","Vet","Package linux/amd64","Package linux/arm64","Package darwin/amd64","Package darwin/arm64"] | sort' \
  >"$evidence_dir/required-design-contexts.json"
jq --slurpfile observed "$evidence_dir/successful-contexts.json" \
  '[.[] | select(. as $name | $observed[0] | index($name))]' \
  "$evidence_dir/required-design-contexts.json" \
  >"$evidence_dir/observed-required-contexts.json"
diff -u "$evidence_dir/required-design-contexts.json" \
  "$evidence_dir/observed-required-contexts.json"
jq -e 'length == 9' "$evidence_dir/observed-required-contexts.json"
```

Expected: the report commit's current head, not the earlier code head, has all nine exact successes. Keep this regenerated `observed-required-contexts.json` for Task 5.

---

### Task 5: Final `main` Ruleset Mutation, Read-back, and Behavioral Verification

**Files:**
- Modify: `docs/superpowers/reports/2026-07-12-post-release-stabilization.md`
- External state: GitHub repository rulesets for `Forest-Isle/daimon`; only the single ruleset named `Daimon main protection` may be created or updated.

**Interfaces:**
- Consumes: the same open PR from Task 4; its current `head_sha`; regenerated `observed-required-contexts.json` containing exactly nine successful names; GitHub ruleset REST API.
- Produces: one active branch ruleset targeting exactly `refs/heads/main`, no bypass actors, pull-request-only changes with zero approvals, strict current-head checks, the nine observed contexts, deletion protection, and non-fast-forward protection; full prior-state and applied/read-back JSON retained outside the repository until verification completes.
- Recovery: if `Daimon main protection` existed, `PUT` its exported `{name,target,enforcement,bypass_actors,conditions,rules}` payload back; if it did not exist, `DELETE` only the ID created by this task. Never alter other IDs.

- [ ] **Step 1: Reconfirm this is the last external change and export every prior ruleset in full**

Run:

```bash
git status --short
test "$(gh pr view "$pr" --repo Forest-Isle/daimon --json headRefOid --jq .headRefOid)" = "$head_sha"
jq -e 'length == 9' "$evidence_dir/observed-required-contexts.json"
rules_dir=$(mktemp -d)
gh api --paginate repos/Forest-Isle/daimon/rulesets \
  | jq -s '[.[][]] | unique_by(.id)' >"$rules_dir/prior-summaries.json"
jq -r '.[].id' "$rules_dir/prior-summaries.json" | while read -r id; do
  gh api "repos/Forest-Isle/daimon/rulesets/$id"
done | jq -s '.' >"$rules_dir/prior-full.json"
jq '[.[] | {id,name,target,enforcement,bypass_actors,conditions,rules}]' \
  "$rules_dir/prior-full.json" >"$rules_dir/prior-restorable.json"
jq . "$rules_dir/prior-restorable.json"
```

Expected: files contain every current repository ruleset and its full restorable policy. Copy `rules_dir` to a secure temporary location if the shell will not remain open. Stop if the export is incomplete.

- [ ] **Step 2: Build the checked payload from the observed contexts and validate it locally**

Run:

```bash
jq -n --slurpfile contexts "$evidence_dir/observed-required-contexts.json" '
{
  name: "Daimon main protection",
  target: "branch",
  enforcement: "active",
  bypass_actors: [],
  conditions: {ref_name: {include: ["refs/heads/main"], exclude: []}},
  rules: [
    {type: "deletion"},
    {type: "non_fast_forward"},
    {type: "pull_request", parameters: {
      dismiss_stale_reviews_on_push: false,
      require_code_owner_review: false,
      require_last_push_approval: false,
      required_approving_review_count: 0,
      required_review_thread_resolution: false
    }},
    {type: "required_status_checks", parameters: {
      do_not_enforce_on_create: false,
      strict_required_status_checks_policy: true,
      required_status_checks: ($contexts[0] | map({context: .}))
    }}
  ]
}' >"$rules_dir/request.json"
jq -e '
  .target == "branch" and .enforcement == "active" and
  .bypass_actors == [] and
  .conditions.ref_name == {include:["refs/heads/main"],exclude:[]} and
  ([.rules[].type] | sort) == (["deletion","non_fast_forward","pull_request","required_status_checks"] | sort) and
  ([.rules[] | select(.type == "pull_request")][0].parameters.required_approving_review_count == 0) and
  ([.rules[] | select(.type == "required_status_checks")][0].parameters.strict_required_status_checks_policy == true) and
  ([.rules[] | select(.type == "required_status_checks")][0].parameters.required_status_checks | length == 9)
' "$rules_dir/request.json"
```

Expected: validation returns `true`; all contexts originate from Task 4's API output.

- [ ] **Step 3: Create or update only the named ruleset with an armed recovery trap**

Run this in one shell, leaving the trap active through Steps 4-6:

```bash
existing_id=$(jq -r '.[] | select(.name == "Daimon main protection") | .id' \
  "$rules_dir/prior-restorable.json")
test "$(printf '%s\n' "$existing_id" | sed '/^$/d' | wc -l | tr -d ' ')" -le 1
created_id=
restore_ruleset() {
  if [[ -n $existing_id ]]; then
    jq --argjson id "$existing_id" '.[] | select(.id == $id) | del(.id)' \
      "$rules_dir/prior-restorable.json" >"$rules_dir/restore-request.json"
    gh api --method PUT "repos/Forest-Isle/daimon/rulesets/$existing_id" \
      --input "$rules_dir/restore-request.json" >/dev/null
  elif [[ -n ${created_id:-} ]]; then
    gh api --method DELETE "repos/Forest-Isle/daimon/rulesets/$created_id"
  fi
}
trap 'status=$?; if ((status != 0)); then restore_ruleset; fi; exit $status' EXIT
if [[ -n $existing_id ]]; then
  gh api --method PUT "repos/Forest-Isle/daimon/rulesets/$existing_id" \
    --input "$rules_dir/request.json" >"$rules_dir/applied.json"
  applied_id=$existing_id
else
  gh api --method POST repos/Forest-Isle/daimon/rulesets \
    --input "$rules_dir/request.json" >"$rules_dir/applied.json"
  created_id=$(jq -r .id "$rules_dir/applied.json")
  applied_id=$created_id
fi
test -n "$applied_id"
```

Expected: API returns one ID. If GitHub reports an unsupported rule/plan, the trap restores prior state (or removes only the newly created ruleset) and execution stops; do not weaken the request.

- [ ] **Step 4: Read back and compare every policy field**

Run:

```bash
gh api "repos/Forest-Isle/daimon/rulesets/$applied_id" >"$rules_dir/readback.json"
jq '{name,target,enforcement,bypass_actors,conditions,rules}' \
  "$rules_dir/readback.json" >"$rules_dir/readback-policy.json"
jq -S . "$rules_dir/request.json" >"$rules_dir/request.sorted.json"
jq -S . "$rules_dir/readback-policy.json" >"$rules_dir/readback.sorted.json"
diff -u "$rules_dir/request.sorted.json" "$rules_dir/readback.sorted.json"
```

If GitHub adds documented default fields, compare semantic fields explicitly instead of deleting requested fields:

```bash
jq -e --slurpfile request "$rules_dir/request.json" '
  .name == $request[0].name and
  .target == $request[0].target and
  .enforcement == "active" and
  .bypass_actors == [] and
  .conditions.ref_name == $request[0].conditions.ref_name and
  ([.rules[].type] | sort) == (["deletion","non_fast_forward","pull_request","required_status_checks"] | sort) and
  ([.rules[] | select(.type == "pull_request")][0].parameters ==
   ($request[0].rules[] | select(.type == "pull_request") | .parameters)) and
  ([.rules[] | select(.type == "required_status_checks")][0].parameters.strict_required_status_checks_policy == true) and
  ([.rules[] | select(.type == "required_status_checks")][0].parameters.do_not_enforce_on_create == false) and
  (([.rules[] | select(.type == "required_status_checks")][0].parameters.required_status_checks | map(.context) | sort) ==
   ($request[0].rules[] | select(.type == "required_status_checks") | .parameters.required_status_checks | map(.context) | sort))
' "$rules_dir/readback.json"
```

Expected: exact diff is empty or the semantic comparison returns `true` only for documented server-added defaults. Any requested-field difference triggers restoration.

- [ ] **Step 5: Verify `main` rejects non-fast-forward updates and deletion**

Run:

```bash
main_sha=$(git ls-remote origin refs/heads/main | awk '{print $1}')
parent_sha=$(git rev-parse "$main_sha^")
if git push origin "$parent_sha:refs/heads/main" \
  --force-with-lease="refs/heads/main:$main_sha"; then
  echo 'non-fast-forward main update unexpectedly succeeded' >&2
  exit 1
fi
test "$(git ls-remote origin refs/heads/main | awk '{print $1}')" = "$main_sha"
if git push origin :refs/heads/main; then
  echo 'main deletion unexpectedly succeeded' >&2
  exit 1
fi
test "$(git ls-remote origin refs/heads/main | awk '{print $1}')" = "$main_sha"
```

Expected: both pushes are rejected under the authenticated maintainer identity and remote `main` remains at `main_sha`.

- [ ] **Step 6: Verify required-check blocking, strict current-head behavior, and zero-review usability on the same PR**

Create a temporary failing required check on the PR branch:

```bash
cat >cmd/daimon/ruleset_probe_test.go <<'EOF'
package main

import "testing"

func TestRulesetRequiredCheckProbe(t *testing.T) {
	t.Fatal("intentional ruleset probe")
}
EOF
git add cmd/daimon/ruleset_probe_test.go
git commit -m "test: probe required-check ruleset blocking"
git push
probe_sha=$(git rev-parse HEAD)
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10 || true
gh pr view "$pr" --repo Forest-Isle/daimon \
  --json headRefOid,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup \
  >"$rules_dir/failing-pr-state.json"
jq -e --arg sha "$probe_sha" '
  .headRefOid == $sha and
  .mergeStateStatus != "CLEAN" and
  ([.statusCheckRollup[] | select(.name == "Test" and .conclusion == "FAILURE")] | length == 1)
' "$rules_dir/failing-pr-state.json"
```

Remove the probe via a new current head, then wait for all nine contexts on that head:

```bash
git rm cmd/daimon/ruleset_probe_test.go
git commit -m "test: remove required-check ruleset probe"
git push
head_sha=$(git rev-parse HEAD)
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10
gh pr view "$pr" --repo Forest-Isle/daimon \
  --json headRefOid,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup \
  >"$rules_dir/passing-pr-state.json"
jq -e --arg sha "$head_sha" '
  .headRefOid == $sha and
  .mergeable == "MERGEABLE" and
  .mergeStateStatus == "CLEAN" and
  (.reviewDecision == null or .reviewDecision == "")
' "$rules_dir/passing-pr-state.json"
gh api --paginate "repos/Forest-Isle/daimon/commits/$head_sha/check-runs?per_page=100" \
  | jq -s '[.[].check_runs[] | select(.conclusion == "success") | .name] | unique | sort' \
  >"$rules_dir/final-successful-contexts.json"
jq --slurpfile observed "$rules_dir/final-successful-contexts.json" \
  'all(.[]; . as $context | $observed[0] | index($context))' \
  "$evidence_dir/observed-required-contexts.json"
```

Expected: the failing `Test` current head is not mergeable; the next current head is mergeable only after all nine contexts succeed; `reviewDecision` proves no second approval is required. Do not merge yet.

- [ ] **Step 7: Disarm recovery only after all behavior checks and record final evidence**

Run:

```bash
trap - EXIT
report=docs/superpowers/reports/2026-07-12-post-release-stabilization.md
python3 - "$report" "$applied_id" "$head_sha" "$rules_dir/readback.json" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
ruleset = json.loads(pathlib.Path(sys.argv[4]).read_text())
text = path.read_text()
lines = [
    "## `main` ruleset verification",
    "",
    f"- Ruleset ID: `{sys.argv[2]}`",
    f"- Final verified PR head: `{sys.argv[3]}`",
    f"- Enforcement: `{ruleset['enforcement']}`",
    "- Target: `refs/heads/main`",
    "- Bypass actors: none",
    "- Pull request approvals required: `0`",
    "- Strict required checks: enabled",
    "- Non-fast-forward update: rejected",
    "- Branch deletion: rejected",
    "- Failing required check: merge blocked",
    "- All nine checks on current head: mergeable without a second reviewer",
]
if "## `main` ruleset verification" in text:
    raise SystemExit("ruleset evidence already exists")
path.write_text(text.rstrip() + "\n\n" + "\n".join(lines) + "\n")
PY
git add docs/superpowers/reports/2026-07-12-post-release-stabilization.md
git commit -m "docs: record main ruleset verification"
git push
```

Expected: recovery is disarmed only after API read-back and all behavior checks pass. Because this report commit creates one more PR head, wait for and verify all nine contexts again before merge:

```bash
head_sha=$(git rev-parse HEAD)
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10
gh pr view "$pr" --repo Forest-Isle/daimon --json headRefOid,mergeable,mergeStateStatus,reviewDecision
```

Expected: `headRefOid` equals `head_sha`, `mergeable` is `MERGEABLE`, `mergeStateStatus` is `CLEAN`, and no approving review is required. Keep `prior-full.json`, `request.json`, and `readback.json` until after merge; do not commit them.

---

### Task 6: Final Repository and External-State Audit

**Files:**
- Modify only a file already named in Tasks 1-5 if its corresponding verification exposes a defect; never broaden scope.

**Interfaces:**
- Consumes: all task commits, the open PR's final head, the applied ruleset read-back, and retained recovery export.
- Produces: a clean, fully committed branch ready for review/merge with no release asset, tag, runtime feature, unrelated dependency, or temporary ruleset probe file changed.

- [ ] **Step 1: Run the complete local verification matrix**

```bash
make smoke-release-test
make package-test
actionlint .github/workflows/*.yml
make vet
make test
git diff --check main...HEAD
```

Expected: every command exits 0. The smoke suite remains credential/network independent.

- [ ] **Step 2: Audit changed files and forbidden residue**

```bash
git status --short
git diff --name-status main...HEAD
test ! -e cmd/daimon/ruleset_probe_test.go
! git diff main...HEAD -- go.mod go.sum .github/workflows/release.yml \
  | rg '^[+-].*(v0\.1\.0|tags:|VERSION:)' 
find . -maxdepth 2 -type f \( -name 'daimon_*.tar.gz' -o -name checksums.txt \) -print
```

Expected: clean status; no probe or downloaded release files; no dependency change; release tag policy and archive contents remain unchanged. The release workflow may differ only in action-version references.

- [ ] **Step 3: Re-read external policy and final PR state**

```bash
gh api "repos/Forest-Isle/daimon/rulesets/$applied_id" \
  | jq '{name,target,enforcement,bypass_actors,conditions,rules}'
gh pr checks "$pr" --repo Forest-Isle/daimon
gh pr view "$pr" --repo Forest-Isle/daimon \
  --json headRefOid,mergeable,mergeStateStatus,reviewDecision
```

Expected: the active policy still matches Task 5; the PR's current head owns all nine successful required contexts and is mergeable without a second reviewer.

- [ ] **Step 4: Commit only audit corrections, if verification required one**

If a named-file correction was necessary, rerun the exact failed command, then:

```bash
git add scripts/smoke-release.sh scripts/smoke-release_test.sh Makefile README.md \
  .github/workflows/ci.yml .github/workflows/coverage.yml \
  .github/workflows/package.yml .github/workflows/release.yml \
  .github/workflows/security.yml \
  docs/superpowers/reports/2026-07-12-post-release-stabilization.md
git commit -m "fix: close post-release stabilization audit gaps"
git push
gh pr checks "$pr" --repo Forest-Isle/daimon --watch --interval 10
```

If no correction was required, do not create an empty commit. Any new head must again pass all nine contexts before merge.

- [ ] **Step 5: Hand off for merge without changing release state**

Report the smoke output, selected action metadata/runtime table, PR number/final SHA, nine observed contexts, ruleset ID/read-back, force-push/deletion rejection, failing-check block, and final mergeability. Do not create a tag, release, asset, dependency update, or additional ruleset.
