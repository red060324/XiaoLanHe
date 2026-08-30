#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -gt 0 ] && ! git rev-parse --verify "$1" >/dev/null 2>&1; then
  changed="$(printf '%s\n' "$@")"
else
  base_ref="${1:-origin/master}"
  if git rev-parse --verify "$base_ref" >/dev/null 2>&1; then
    changed="$({ git diff --name-only "$base_ref"...HEAD; git diff --name-only; git ls-files --others --exclude-standard; } | sort -u)"
  elif git rev-parse --verify HEAD^ >/dev/null 2>&1; then
    changed="$({ git diff --name-only HEAD^...HEAD; git diff --name-only; git ls-files --others --exclude-standard; } | sort -u)"
  else
    exit 0
  fi
fi

if ! printf '%s\n' "$changed" | rg -q '^(cmd/|internal/|frontend/.*/src/|migrations/|prompts/|go\.(mod|sum)$|Dockerfile$|render\.yaml$)'; then
  exit 0
fi

if printf '%s\n' "$changed" | rg -q '^(specs/|ARCHITECTURE\.md$|docs/)'; then
  exit 0
fi

echo "production-facing changes require an updated spec or durable architecture/docs evidence" >&2
exit 1
