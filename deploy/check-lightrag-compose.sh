#!/usr/bin/env bash
set -euo pipefail

file=${1:-deploy/docker-compose.lightrag.yml}
expected_image='ghcr.io/hkuds/lightrag:v1.5.7@sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e'
expected_milvus_image='milvusdb/milvus:v2.6.11'
expected_etcd_image='quay.io/coreos/etcd:v3.5.25'
expected_minio_image='minio/minio:RELEASE.2025-09-07T16-13-09Z'
for command_name in go; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 2; }
done

# Parse service structure before textual defense-in-depth checks.
go run ./deploy/check_lightrag_compose.go "$file"

for name in XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR; do
  is_set=${!name+x}
  value=${!name-}
  if [[ -n "$is_set" && ( -z "$value" || "$value" != /* ) ]]; then
    echo "$name must be an absolute path when set" >&2
    exit 1
  fi
done

require_literal() {
  if ! grep -Fq -- "$1" "$file"; then
    echo "LightRAG compose is missing required setting: $1" >&2
    exit 1
  fi
}

require_literal_count() {
  local literal=$1
  local expected_count=$2
  local actual_count
  actual_count=$(awk -v literal="$literal" '$0 == literal { count++ } END { print count + 0 }' "$file")
  if (( actual_count != expected_count )); then
    echo "LightRAG compose must contain exactly $expected_count copies of: $literal (found $actual_count)" >&2
    exit 1
  fi
}

require_literal "image: $expected_image"
require_literal 'command: ["python", "/opt/xlh/guarded_start.py", "steady"]'
require_literal 'WORKSPACE: xiaolanhe_v1'
require_literal 'WORKING_DIR: /app/data/rag_storage'
require_literal 'WORKERS: "2"'
require_literal 'LIGHTRAG_KV_STORAGE: JsonKVStorage'
require_literal 'LIGHTRAG_VECTOR_STORAGE: MilvusVectorDBStorage'
require_literal 'LIGHTRAG_GRAPH_STORAGE: NetworkXStorage'
require_literal 'LIGHTRAG_DOC_STATUS_STORAGE: JsonDocStatusStorage'
require_literal 'MILVUS_URI: http://milvus:19530'
require_literal 'MILVUS_DB_NAME: lightrag'
require_literal 'MILVUS_INDEX_TYPE: AUTOINDEX'
require_literal 'MILVUS_METRIC_TYPE: COSINE'
require_literal 'EMBEDDING_MODEL: ${XLH_LIGHTRAG_EMBEDDING_MODEL:-text-embedding-v4}'
require_literal 'EMBEDDING_DIM: ${XLH_LIGHTRAG_EMBEDDING_DIM:-1024}'
require_literal 'EMBEDDING_SEND_DIM: "false"'
require_literal 'EMBEDDING_ASYMMETRIC: "false"'
require_literal 'EMBEDDING_DOCUMENT_PREFIX: ""'
require_literal 'EMBEDDING_QUERY_PREFIX: ""'
require_literal 'LIGHTRAG_API_KEY: ${XLH_LIGHTRAG_API_KEY:?XLH_LIGHTRAG_API_KEY is required}'
require_literal 'WHITELIST_PATHS: /health'
require_literal 'ENABLE_API_DOCS: "false"'
require_literal 'MAX_PENDING_DOCUMENTS: "100"'
require_literal '127.0.0.1:9621:9621'
require_literal 'replicas: 1'
require_literal 'mem_limit: 2g'
require_literal 'cpus: 2.0'
require_literal 'xlh-lightrag-data:/app/data/rag_storage'
require_literal "image: $expected_milvus_image"
require_literal "image: $expected_etcd_image"
require_literal "image: $expected_minio_image"
require_literal 'xlh-milvus-data:/var/lib/milvus'
require_literal 'xlh-milvus-etcd-data:/etcd'
require_literal 'xlh-milvus-minio-data:/minio_data'
require_literal '["CMD", "curl", "--fail", "http://localhost:9091/healthz"]'
require_literal 'condition: service_healthy'
require_literal 'condition: service_completed_successfully'
require_literal './lightrag/init_milvus.py:/opt/xlh/init_milvus.py:ro'
require_literal './lightrag/rebuild_fence.py:/opt/xlh/rebuild_fence.py:ro'
require_literal './lightrag/guarded_start.py:/opt/xlh/guarded_start.py:ro'
require_literal '${XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR:?XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR is required}:/rebuild-fence'
require_literal '${XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR:?XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR is required}:/rebuild-fence:ro'
require_literal '${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:?XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR is required}:/writer-evidence:ro'
require_literal_count '      XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR: ${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:?XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR must be an absolute path}' 1
require_literal_count '      XLH_LIGHTRAG_SHARED_GID: ${XLH_LIGHTRAG_SHARED_GID:?XLH_LIGHTRAG_SHARED_GID must be a nonzero numeric group ID}' 2
require_literal 'MINIO_ROOT_USER: ${XLH_MILVUS_MINIO_USER:?XLH_MILVUS_MINIO_USER is required}'
require_literal 'MINIO_ROOT_PASSWORD: ${XLH_MILVUS_MINIO_PASSWORD:?XLH_MILVUS_MINIO_PASSWORD is required}'
require_literal 'XLH_LIGHTRAG_DEPLOYMENT_GENERATION: ${XLH_LIGHTRAG_DEPLOYMENT_GENERATION:?XLH_LIGHTRAG_DEPLOYMENT_GENERATION is required}'
require_literal 'profiles: ["bootstrap"]'
require_literal 'COMMON_SECURITY_AUTHORIZATIONENABLED: "true"'
require_literal 'COMMON_SECURITY_ENABLEPUBLICPRIVILEGE: "false"'
require_literal 'MILVUS_BOOTSTRAP_TOKEN: root:${XLH_MILVUS_ROOT_PASSWORD:?XLH_MILVUS_ROOT_PASSWORD is required}'
require_literal 'MILVUS_RUNTIME_TOKEN: ${XLH_MILVUS_TOKEN:?XLH_MILVUS_TOKEN is required}'
if (( $(grep -Fc -- 'host.docker.internal:host-gateway' "$file") < 2 )); then
  echo "LightRAG and its controller must both map host.docker.internal" >&2
  exit 1
fi

contract_hash=$(bash deploy/lightrag-contract-hash.sh)
if [[ ! "$contract_hash" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "LightRAG contract hash command returned an invalid digest" >&2
  exit 1
fi

if grep -Fq 'LIGHTRAG_VECTOR_STORAGE: NanoVectorDBStorage' "$file"; then
  echo "LightRAG compose must not retain the NanoVectorDB backend" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]+-[[:space:]]+"?(19530|9091|2379|9000|9001):' "$file"; then
  echo "Milvus, etcd and MinIO ports must remain private to the Compose network" >&2
  exit 1
fi
if grep -Eq '(^|[[:space:]])image:[[:space:]]+[^[:space:]]+:latest([[:space:]]|$)' "$file"; then
  echo "LightRAG compose must pin an immutable image" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]+MAX_REQUEST_BODY_BYTES:' "$file"; then
  echo "LightRAG compose must retain the upstream tiered request-body limits" >&2
  exit 1
fi
if grep -Fq -- '${XLH_LIGHTRAG_DEPLOYMENT_GENERATION:-' "$file"; then
  echo "LightRAG deployment generation must not have a Compose fallback" >&2
  exit 1
fi
if grep -Fq -- 'XLH_LIGHTRAG_WRITERS_STOPPED' "$file"; then
  echo "LightRAG writer quiescence must use external evidence, not an environment switch" >&2
  exit 1
fi
