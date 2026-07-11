#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
dockerfile="$root/Dockerfile"
compose="$root/docker-compose.yml"

awk '
  /^USER daimon$/ { user_seen = 1 }
  !user_seen && /mkdir -p .*\/home\/daimon\/\.daimon/ { created = 1 }
  !user_seen && /chown -R daimon:daimon .*\/home\/daimon/ { owned = 1 }
  END { exit !(created && owned && user_seen) }
' "$dockerfile"

grep -F -- '- daimon-data:/home/daimon/.daimon' "$compose" >/dev/null
grep -F -- '- HOME=/home/daimon' "$compose" >/dev/null
