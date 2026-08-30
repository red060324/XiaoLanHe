#!/usr/bin/env bash
set -u

status=0
for file in "$@"; do
  [ -f "$file" ] || continue
  dir=$(dirname "$file")
  links=$(sed -nE 's/.*\[[^]]+\]\(([^)]+)\).*/\1/p' "$file")
  for raw_link in $links; do
    link=${raw_link%%#*}
    link=${link%%\?*}
    case "$link" in
      ""|http:*|https:*|mailto:*|\#*|/*) continue ;;
    esac
    if [ ! -e "$dir/$link" ]; then
      echo "$file: broken relative link: $raw_link"
      status=1
    fi
  done
done
exit "$status"
