#!/usr/bin/env bash
set -u

status=0
pattern='TODO[[:space:]]+implement|TODO[[:space:]]+precise assertion|<PLACEHOLDER>|<INSERT_HERE>|<YOUR_|panic\("TODO'
for file in "$@"; do
  [ -f "$file" ] || continue
  case "$file" in .hooks/check-no-placeholders.sh) continue ;; esac
  if grep -nEi "$pattern" "$file" >/dev/null 2>&1; then
    echo "$file: contains placeholder-style code"
    status=1
  fi
done
exit "$status"
