#!/usr/bin/env bash
set -u

status=0
for file in "$@"; do
  [ -f "$file" ] || continue
  if LC_ALL=C grep -n $'\r' "$file" >/dev/null 2>&1; then
    echo "$file: contains CRLF line endings"
    status=1
  fi
  if grep -n '[[:blank:]]$' "$file" >/dev/null 2>&1; then
    echo "$file: contains trailing whitespace"
    status=1
  fi
  if grep -nE '^(<<<<<<<|=======|>>>>>>>)' "$file" >/dev/null 2>&1; then
    echo "$file: contains merge conflict markers"
    status=1
  fi
  if [ -s "$file" ] && [ "$(tail -c 1 "$file" | od -An -t x1 | tr -d '[:space:]')" != "0a" ]; then
    echo "$file: missing final newline"
    status=1
  fi
done
exit "$status"
