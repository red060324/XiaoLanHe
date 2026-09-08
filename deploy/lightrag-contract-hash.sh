#!/usr/bin/env bash
set -euo pipefail

export XLH_LIGHTRAG_IMAGE_DIGEST=${XLH_LIGHTRAG_IMAGE_DIGEST:-sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e}
export XLH_LIGHTRAG_WORKING_DIR_ID=${XLH_LIGHTRAG_WORKING_DIR_ID:-xlh-lightrag-data}
export XLH_EMBEDDING_ENDPOINT_PROFILE=${XLH_EMBEDDING_ENDPOINT_PROFILE:-dashscope-compatible-v1}
export XLH_LIGHTRAG_FIXTURE_SUITE_VERSION=${XLH_LIGHTRAG_FIXTURE_SUITE_VERSION:-lifecycle-v1}
export MILVUS_DB_NAME=${MILVUS_DB_NAME:-lightrag}
export WORKSPACE=${XLH_LIGHTRAG_WORKSPACE:-xiaolanhe_v1}
export LIGHTRAG_KV_STORAGE=JsonKVStorage
export LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage
export LIGHTRAG_GRAPH_STORAGE=NetworkXStorage
export LIGHTRAG_DOC_STATUS_STORAGE=JsonDocStatusStorage
export EMBEDDING_BINDING=openai
export EMBEDDING_MODEL=${XLH_LIGHTRAG_EMBEDDING_MODEL:-text-embedding-v4}
export EMBEDDING_DIM=${XLH_LIGHTRAG_EMBEDDING_DIM:-1024}
export EMBEDDING_SEND_DIM=false
export EMBEDDING_ASYMMETRIC=false
# LightRAG 1.5.7 rejects explicitly empty prefix variables. This symmetric
# embedding contract requires both keys to be absent, including when inherited
# from the operator shell.
unset EMBEDDING_DOCUMENT_PREFIX EMBEDDING_QUERY_PREFIX
export MILVUS_INDEX_TYPE=AUTOINDEX
export MILVUS_METRIC_TYPE=COSINE

exec python3 deploy/lightrag/rebuild_fence.py contract-hash
