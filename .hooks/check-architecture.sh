#!/usr/bin/env bash
set -euo pipefail

failed=0
module='github.com/red060324/XiaoLanHe/internal'

is_legacy_migration_file() {
  case "$1" in
    cmd/import-knowledge/*|internal/knowledge/importer/postgres/*|cmd/migrate-postgres-to-mysql/*|internal/migration/postgrestomysql/*) return 0 ;;
    *) return 1 ;;
  esac
}

while IFS= read -r file; do
  case "$file" in
    internal/adapter/postgres/*|*/repository/postgres/*)
      echo "legacy PostgreSQL package exists outside the isolated migration boundary: $file" >&2
      failed=1
      ;;
  esac

  case "$file" in
    internal/usecase/*|*/usecase/*)
      if rg -n '"(database/sql|github.com/(cloudwego/(eino|hertz)|go-sql-driver/mysql|jackc/pgx))' "$file"; then
        echo "forbidden framework/driver import in Clean Architecture core: $file" >&2
        failed=1
      fi
      if rg -n '"'"$module"'/([^"/]*/)?(adapter|entry|presenter|config)(/|\")' "$file"; then
        echo "usecase imports an outer layer: $file" >&2
        failed=1
      fi
      ;;
    internal/entity/*|*/entity/*)
      if rg -n '"(database/sql|github.com/(cloudwego/(eino|hertz)|go-sql-driver/mysql|jackc/pgx))' "$file"; then
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

  # PostgreSQL exists only as an isolated, operator-invoked migration source.
  # Historical SQL under migrations/ is deliberately outside this Go runtime scan.
  if rg -q '"github\.com/jackc/pgx|"github\.com/lib/pq|"gorm\.io/driver/postgres|"github\.com/pgvector|postgres(ql)?://|NanoVectorDB(Storage)?' "$file"; then
    if ! is_legacy_migration_file "$file"; then
      rg -n '"github\.com/jackc/pgx|"github\.com/lib/pq|"gorm\.io/driver/postgres|"github\.com/pgvector|postgres(ql)?://|NanoVectorDB(Storage)?' "$file"
      echo "forbidden PostgreSQL/pgvector/NanoVectorDB dependency in normal runtime: $file" >&2
      failed=1
    fi
  fi

  if rg -n '"github\.com/red060324/XiaoLanHe/internal/([^"]*/)?(adapter/postgres|repository/postgres)(/|")' "$file"; then
    echo "forbidden legacy PostgreSQL repository import: $file" >&2
    failed=1
  fi

  if rg -q '"github\.com/red060324/XiaoLanHe/internal/knowledge/importer/postgres(/|")' "$file"; then
    case "$file" in
      cmd/import-knowledge/*) ;;
      *)
        rg -n '"github\.com/red060324/XiaoLanHe/internal/knowledge/importer/postgres(/|")' "$file"
        echo "legacy knowledge source imported outside cmd/import-knowledge: $file" >&2
        failed=1
        ;;
    esac
  fi

  if rg -q '"github\.com/red060324/XiaoLanHe/internal/migration/postgrestomysql(/|")' "$file"; then
    case "$file" in
      cmd/migrate-postgres-to-mysql/*) ;;
      *)
        rg -n '"github\.com/red060324/XiaoLanHe/internal/migration/postgrestomysql(/|")' "$file"
        echo "relational cutover source imported outside cmd/migrate-postgres-to-mysql: $file" >&2
        failed=1
        ;;
    esac
  fi

  if rg -n '"github\.com/milvus-io/|"github\.com/[^"]*milvus' "$file"; then
    echo "Go runtime must reach Milvus only through the LightRAG API: $file" >&2
    failed=1
  fi
done < <(find internal -type f -name '*.go' ! -name '*_test.go' | sort)

while IFS= read -r file; do
  if rg -q '"github\.com/jackc/pgx|"github\.com/lib/pq|"gorm\.io/driver/postgres|"github\.com/pgvector|postgres(ql)?://|NanoVectorDB(Storage)?' "$file"; then
    if ! is_legacy_migration_file "$file"; then
      rg -n '"github\.com/jackc/pgx|"github\.com/lib/pq|"gorm\.io/driver/postgres|"github\.com/pgvector|postgres(ql)?://|NanoVectorDB(Storage)?' "$file"
      echo "forbidden PostgreSQL/pgvector/NanoVectorDB dependency in normal command: $file" >&2
      failed=1
    fi
  fi
  if rg -n '"github\.com/red060324/XiaoLanHe/internal/([^"]*/)?(adapter/postgres|repository/postgres)(/|")' "$file"; then
    echo "normal command imports a legacy PostgreSQL repository: $file" >&2
    failed=1
  fi
  if rg -q '"github\.com/red060324/XiaoLanHe/internal/knowledge/importer/postgres(/|")' "$file"; then
    case "$file" in
      cmd/import-knowledge/*) ;;
      *)
        rg -n '"github\.com/red060324/XiaoLanHe/internal/knowledge/importer/postgres(/|")' "$file"
        echo "legacy knowledge source imported outside cmd/import-knowledge: $file" >&2
        failed=1
        ;;
    esac
  fi
  if rg -q '"github\.com/red060324/XiaoLanHe/internal/migration/postgrestomysql(/|")' "$file"; then
    case "$file" in
      cmd/migrate-postgres-to-mysql/*) ;;
      *)
        rg -n '"github\.com/red060324/XiaoLanHe/internal/migration/postgrestomysql(/|")' "$file"
        echo "relational cutover source imported outside cmd/migrate-postgres-to-mysql: $file" >&2
        failed=1
        ;;
    esac
  fi
  if rg -n '"github\.com/milvus-io/|"github\.com/[^"]*milvus' "$file"; then
    echo "Go command must reach Milvus only through the LightRAG API: $file" >&2
    failed=1
  fi
done < <(find cmd -type f -name '*.go' ! -name '*_test.go' | sort)

exit "$failed"
