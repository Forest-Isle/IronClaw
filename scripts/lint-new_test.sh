#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
original_path=$PATH
cat >"$tmp/golangci-lint" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$LINT_ARGS_FILE"
EOF
chmod +x "$tmp/golangci-lint"

export PATH="$tmp:$PATH" LINT_ARGS_FILE="$tmp/args"
"$root/scripts/lint-new.sh" HEAD^
test "$(cat "$tmp/args")" = "run --new-from-rev HEAD^ ./..."

rm -f "$tmp/args"
if "$root/scripts/lint-new.sh" definitely-not-a-commit; then
  echo "invalid base unexpectedly succeeded" >&2
  exit 1
fi
test ! -e "$tmp/args"

# When the real linter is available, prove the gate distinguishes a clean base
# from a newly introduced compile/lint failure in an isolated repository.
export PATH=$original_path
if command -v golangci-lint >/dev/null 2>&1; then
  repo="$tmp/repo"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.name "Daimon Test"
  git -C "$repo" config user.email "test@daimon.local"
  printf 'module example.com/linttest\n\ngo 1.24\n' >"$repo/go.mod"
  printf 'package linttest\n\nfunc Value() int { return 1 }\n' >"$repo/lint.go"
  git -C "$repo" add .
  git -C "$repo" commit -qm base
  base=$(git -C "$repo" rev-parse HEAD)
  (
    cd "$repo"
    "$root/scripts/lint-new.sh" "$base"
  )
  printf 'package linttest\n\nfunc Broken() { undefinedSymbol() }\n' >"$repo/broken.go"
  if (
    cd "$repo"
    "$root/scripts/lint-new.sh" "$base"
  ); then
    echo "new lint failure unexpectedly succeeded" >&2
    exit 1
  fi
else
  echo "lint-new test: real golangci-lint regression skipped (tool unavailable)" >&2
fi
