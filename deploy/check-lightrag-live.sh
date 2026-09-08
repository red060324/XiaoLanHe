#!/usr/bin/env bash
set -euo pipefail

if (( $# != 6 )); then
  echo "usage: $0 <base-url> <32-512 character api-key without line breaks> <fence-dir> <generation> <contract-sha256> <shared-gid>" >&2
  exit 2
fi
base_url=$1
api_key=$2
fence_dir=$3
fence_generation=$4
fence_contract_sha256=$5
shared_gid=$6
if (( ${#api_key} < 32 || ${#api_key} > 512 )) || [[ "$api_key" == *$'\n'* || "$api_key" == *$'\r'* ]]; then
  echo "invalid API key" >&2
  exit 2
fi
[[ -n "$base_url" && -n "$fence_dir" && -n "$fence_generation" && -n "$fence_contract_sha256" ]] || {
  echo "live deployment contract arguments must not be empty" >&2
  exit 2
}

# Prove the local deployment fence before making any network request, so this
# command cannot degrade into an API-only success signal.
bash deploy/check-lightrag-fence.sh "$fence_dir" "$fence_generation" "$fence_contract_sha256" "$shared_gid"

common_headers=(-H "X-API-Key: $api_key" -H "LIGHTRAG-WORKSPACE: xiaolanhe_v1")

# /health remains a public, minimal liveness probe. It must not reveal the
# authenticated configuration, paths, providers, stores or process topology.
public_health=$(curl --fail --silent --show-error "$base_url/health")
jq -e '
  .status == "healthy" and
  .core_version == "1.5.7" and
  .api_version == "0344" and
  (.pipeline_active | type == "boolean") and
  (has("configuration") | not) and
  (has("working_directory") | not) and
  (has("server_mode") | not) and
  (has("workers") | not)
' <<<"$public_health" >/dev/null

for protected_url in "$base_url/auth/verify" "$base_url/documents/pipeline_status"; do
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$protected_url")
  [[ "$status" == "403" ]] || { echo "expected unauthenticated 403 from $protected_url, got $status" >&2; exit 1; }
done
wrong_status=$(curl --silent --output /dev/null --write-out '%{http_code}' -H "X-API-Key: ${api_key}-wrong" "$base_url/auth/verify")
[[ "$wrong_status" == "403" ]] || { echo "expected wrong-key 403 from /auth/verify, got $wrong_status" >&2; exit 1; }

curl --fail --silent --show-error "${common_headers[@]}" "$base_url/auth/verify" \
  | jq -e '.status == "ok"' >/dev/null

health=$(curl --fail --silent --show-error "${common_headers[@]}" "$base_url/health")
jq -e '
  .status == "healthy" and
  .core_version == "1.5.7" and
  .api_version == "0344" and
  .working_directory == "/app/data/rag_storage" and
  .configuration.workspace == "xiaolanhe_v1" and
  .configuration.kv_storage == "JsonKVStorage" and
    .configuration.vector_storage == "MilvusVectorDBStorage" and
  .configuration.graph_storage == "NetworkXStorage" and
  .configuration.doc_status_storage == "JsonDocStatusStorage" and
  .server_mode == "gunicorn" and
  .workers == 2
' <<<"$health" >/dev/null

pipeline=$(curl --fail --silent --show-error "${common_headers[@]}" "$base_url/documents/pipeline_status")
jq -e '.recovery_required == false and (.busy | type == "boolean")' <<<"$pipeline" >/dev/null
