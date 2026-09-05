#!/usr/bin/env bash
set -euo pipefail

file=${1:-deploy/docker-compose.lightrag.yml}
expected_image='ghcr.io/hkuds/lightrag:v1.5.7@sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e'

require_literal() {
  if ! grep -Fq -- "$1" "$file"; then
    echo "LightRAG compose is missing required setting: $1" >&2
    exit 1
  fi
}

require_literal "image: $expected_image"
require_literal 'command: ["lightrag-gunicorn"]'
require_literal 'WORKSPACE: xiaolanhe_v1'
require_literal 'WORKING_DIR: /app/data/rag_storage'
require_literal 'WORKERS: "2"'
require_literal 'LIGHTRAG_KV_STORAGE: JsonKVStorage'
require_literal 'LIGHTRAG_VECTOR_STORAGE: NanoVectorDBStorage'
require_literal 'LIGHTRAG_GRAPH_STORAGE: NetworkXStorage'
require_literal 'LIGHTRAG_DOC_STATUS_STORAGE: JsonDocStatusStorage'
require_literal 'LIGHTRAG_API_KEY: ${XLH_LIGHTRAG_API_KEY:?XLH_LIGHTRAG_API_KEY is required}'
require_literal 'WHITELIST_PATHS: /health'
require_literal 'ENABLE_API_DOCS: "false"'
require_literal 'MAX_PENDING_DOCUMENTS: "100"'
require_literal '127.0.0.1:9621:9621'
require_literal 'replicas: 1'
require_literal 'mem_limit: 2g'
require_literal 'cpus: 2.0'
require_literal 'xlh-lightrag-data:/app/data/rag_storage'

if grep -Eq '(^|[[:space:]])(POSTGRES|REDIS|NEO4J|MILVUS|QDRANT|MONGO|ELASTIC|OPENSEARCH)_[A-Z0-9_]*:' "$file"; then
  echo "LightRAG compose must use only the approved native storage backends" >&2
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
