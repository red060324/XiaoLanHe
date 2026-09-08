#!/usr/bin/env bash
set -euo pipefail

compose_file=${1:-deploy/docker-compose.lightrag.yml}
project=${2:-}
for command_name in docker python3; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 2; }
done
for name in XLH_LIGHTRAG_API_KEY XLH_LIGHTRAG_LLM_API_KEY XLH_LIGHTRAG_EMBEDDING_API_KEY XLH_MILVUS_MINIO_USER XLH_MILVUS_MINIO_PASSWORD XLH_MILVUS_ROOT_PASSWORD XLH_MILVUS_TOKEN XLH_LIGHTRAG_DEPLOYMENT_GENERATION XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256 XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR XLH_LIGHTRAG_SHARED_GID; do
  [[ -n "${!name:-}" ]] || { echo "$name is required" >&2; exit 2; }
done
[[ "$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" == /* ]] || { echo "XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR must be absolute" >&2; exit 2; }
if [[ -n "$project" && ! "$project" =~ ^[a-z0-9][a-z0-9_-]{0,63}$ ]]; then
  echo "invalid Compose project" >&2
  exit 2
fi
fence_dir=$(cd "$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" && pwd -P)
[[ "$fence_dir" == "$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" ]] || { echo "fence path must already be canonical" >&2; exit 2; }
bash deploy/check-lightrag-fence.sh "$fence_dir" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256" "$XLH_LIGHTRAG_SHARED_GID"

# Compose interpolates inactive-profile services too. Supply an inert absolute
# writer path so steady-state startup does not require bootstrap-only state.
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR=${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:-/xlh-bootstrap-profile-disabled}

compose=(docker compose)
if [[ -n "$project" ]]; then
  compose+=(--project-name "$project")
fi
compose+=(-f "$compose_file")
# Explicit service selection excludes bootstrap-profile jobs. The container
# entry point independently rechecks the same verified fence before exec.
"${compose[@]}" up --detach --wait milvus-etcd milvus-minio milvus lightrag
