#!/usr/bin/env bash
set -euo pipefail

failed=0
module='github.com/red060324/XiaoLanHe/internal'

while IFS= read -r file; do
  case "$file" in
    internal/usecase/*|*/usecase/*)
      if rg -n '"github.com/(cloudwego/(eino|hertz)|jackc/pgx)' "$file"; then
        echo "forbidden framework/driver import in Clean Architecture core: $file" >&2
        failed=1
      fi
      if rg -n '"'"$module"'/([^"/]*/)?(adapter|entry|presenter|config)(/|\")' "$file"; then
        echo "usecase imports an outer layer: $file" >&2
        failed=1
      fi
      ;;
    internal/entity/*|*/entity/*)
      if rg -n '"github.com/(cloudwego/(eino|hertz)|jackc/pgx)' "$file"; then
        echo "forbidden framework/driver import in entity: $file" >&2
        failed=1
      fi
      if rg -n '"'"$module"'/([^"/]*/)?(adapter|entry|presenter|usecase|config)(/|\")' "$file"; then
        echo "entity imports an outer layer: $file" >&2
        failed=1
      fi
      ;;
    internal/presenter/*|*/presenter/*)
      if rg -n '"'"$module"'/([^"/]*/)?(adapter|entry|config)(/|\")' "$file"; then
        echo "presenter imports an outer layer: $file" >&2
        failed=1
      fi
      ;;
    internal/adapter/*|*/adapter/*)
      if rg -n '"'"$module"'/([^"/]*/)?(entry|presenter|config)(/|\")' "$file"; then
        echo "adapter imports an HTTP/composition layer: $file" >&2
        failed=1
      fi
      ;;
  esac
done < <(find internal -type f -name '*.go' ! -name '*_test.go' | sort)

exit "$failed"
