#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
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
