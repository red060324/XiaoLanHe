#!/usr/bin/env bash
set -euo pipefail

# Destructive only to a uniquely named, disposable Compose project and its volume.
# Real provider calls may incur cost, so the acknowledgement is deliberately explicit.
if [[ "${XLH_LIGHTRAG_LIFECYCLE_ACK:-}" != "isolated-destructive-test" ]]; then
  echo "set XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test to run the isolated lifecycle test" >&2
  exit 2
fi
for name in XLH_LIGHTRAG_API_KEY XLH_LIGHTRAG_LLM_API_KEY XLH_LIGHTRAG_EMBEDDING_API_KEY; do
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required" >&2
    exit 2
  fi
done
if (( ${#XLH_LIGHTRAG_API_KEY} < 32 || ${#XLH_LIGHTRAG_API_KEY} > 512 )) || [[ "$XLH_LIGHTRAG_API_KEY" == *$'\n'* || "$XLH_LIGHTRAG_API_KEY" == *$'\r'* ]]; then
  echo "XLH_LIGHTRAG_API_KEY must contain 32-512 characters without line breaks" >&2
  exit 2
fi
for command_name in curl docker jq tar; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 2; }
done
docker compose version >/dev/null

compose_file=deploy/docker-compose.lightrag.yml
base_url=http://127.0.0.1:9621
run_suffix=${XLH_LIGHTRAG_LIFECYCLE_RUN_ID:-$$}
project=xlhlightraglifecycle${run_suffix}
if [[ ! "$run_suffix" =~ ^[a-z0-9][a-z0-9_-]{0,31}$ ]]; then
  echo "XLH_LIGHTRAG_LIFECYCLE_RUN_ID must be 1-32 lowercase letters, digits, underscores or hyphens" >&2
  exit 2
fi
existing_containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
existing_volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$project")
existing_networks=$(docker network ls -q --filter "label=com.docker.compose.project=$project")
if [[ -n "$existing_containers$existing_volumes$existing_networks" ]]; then
  echo "isolated Compose project already exists; choose a fresh XLH_LIGHTRAG_LIFECYCLE_RUN_ID" >&2
  exit 2
fi

readonly image_ref='ghcr.io/hkuds/lightrag:v1.5.7@sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e'
readonly source_key='xlh-7ed76b602fa608069361918764653e17f8d4a41d25d86c1b8b4fe406599c6f03.txt'
readonly query_text='Which hero protects Azure Harbor and what relic powers the shield?'
headers=(-H "X-API-Key: $XLH_LIGHTRAG_API_KEY" -H 'LIGHTRAG-WORKSPACE: xiaolanhe_v1')
compose=(docker compose -p "$project" -f "$compose_file")
backup_dir=$(mktemp -d "${TMPDIR:-/tmp}/xlh-lightrag-lifecycle.XXXXXX")

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf -- "$backup_dir"
}
trap cleanup EXIT INT TERM

wait_for_track() {
  local track_id=$1 response status
  for _ in $(seq 1 180); do
    response=$(curl --fail --silent --show-error "${headers[@]}" "$base_url/documents/track_status/$track_id")
    status=$(jq -r '.documents[0].status // empty' <<<"$response")
    case "$status" in
      PROCESSED) jq -er '.documents[0].id' <<<"$response"; return 0 ;;
      FAILED) echo "LightRAG ingestion failed" >&2; jq -c '{track_id,status_summary}' <<<"$response" >&2; return 1 ;;
    esac
    sleep 2
  done
  echo "LightRAG ingestion did not reach a terminal state" >&2
  return 1
}

list_documents() {
  curl --fail --silent --show-error "${headers[@]}" \
    -H 'Content-Type: application/json' \
    --data '{"page":1,"page_size":200,"sort_field":"updated_at","sort_direction":"desc"}' \
    "$base_url/documents/paginated"
}

assert_document_present() {
  local response=$1 expected_id=$2
  jq -e --arg source "$source_key" --arg id "$expected_id" \
    'any(.documents[]; .id == $id and .file_path == $source and .status == "PROCESSED")' \
    <<<"$response" >/dev/null
}

assert_queries() {
  local mode response
  for mode in local global hybrid mix; do
    response=$(jq -nc --arg query "$query_text" --arg mode "$mode" \
      '{query:$query,mode:$mode,top_k:20,chunk_top_k:12,max_total_tokens:12000,include_references:true}' \
      | curl --fail --silent --show-error "${headers[@]}" -H 'Content-Type: application/json' --data-binary @- "$base_url/query/data")
    jq -e --arg source "$source_key" '
      .status == "success" and
      (.data.entities | type == "array") and
      (.data.relationships | type == "array") and
      (.data.chunks | type == "array") and
      any(.data.references[]?; .file_path == $source)
    ' <<<"$response" >/dev/null
  done
}

echo "Starting isolated LightRAG project: $project"
bash deploy/check-lightrag-compose.sh "$compose_file"
"${compose[@]}" up --detach --wait lightrag
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY"

create_response=$(jq -nc --arg source "$source_key" '{
  text:("XiaoLanHe-Knowledge-v1\nTitle: Azure Harbor Shield Guide\nSource-Type: lifecycle-fixture\nGame-Code: azure-harbor\nRegion-Code: CN\nPatch-Version: 1.0\n\nIn Azure Harbor, the hero Lin protects the harbor. The Moonstone relic powers Lin safe shield."),
  file_source:$source
}' | curl --fail --silent --show-error "${headers[@]}" -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/text")
track_id=$(jq -er '.status == "success" and .track_id' <<<"$create_response")
document_id=$(wait_for_track "$track_id")
before_restart=$(list_documents)
assert_document_present "$before_restart" "$document_id"
assert_queries
duplicate_status=$(jq -nc --arg source "$source_key" '{text:"duplicate",file_source:$source}' \
  | curl --silent --show-error --output "$backup_dir/duplicate.json" --write-out '%{http_code}' \
      "${headers[@]}" -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/text")
[[ "$duplicate_status" == "409" ]] || { echo "expected duplicate source 409, got $duplicate_status" >&2; exit 1; }

"${compose[@]}" restart --timeout 30 lightrag
"${compose[@]}" up --detach --wait lightrag
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY"
assert_document_present "$(list_documents)" "$document_id"
assert_queries

"${compose[@]}" stop --timeout 30 lightrag
volumes=$(docker volume ls --quiet \
  --filter "label=com.docker.compose.project=$project" \
  --filter 'label=com.docker.compose.volume=xlh-lightrag-data')
volume_count=$(printf '%s\n' "$volumes" | awk 'NF { count++ } END { print count+0 }')
if [[ "$volume_count" != "1" ]]; then
  echo "expected exactly one isolated LightRAG volume, found $volume_count" >&2
  exit 1
fi
volume=$volumes
docker run --rm --entrypoint sh -v "$volume:/source:ro" -v "$backup_dir:/backup" "$image_ref" \
  -c 'cd /source && tar -cf /backup/lightrag.tar .'
tar -tf "$backup_dir/lightrag.tar" >/dev/null
[[ -s "$backup_dir/lightrag.tar" ]] || { echo "LightRAG backup archive is empty" >&2; exit 1; }

"${compose[@]}" down
docker volume rm "$volume" >/dev/null
docker volume create \
  --label "com.docker.compose.project=$project" \
  --label com.docker.compose.volume=xlh-lightrag-data \
  "$volume" >/dev/null
docker run --rm --entrypoint sh -v "$volume:/target" -v "$backup_dir:/backup:ro" "$image_ref" \
  -c 'cd /target && tar -xf /backup/lightrag.tar'

"${compose[@]}" up --detach --wait lightrag
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY"
assert_document_present "$(list_documents)" "$document_id"
assert_queries

delete_response=$(jq -nc --arg id "$document_id" '{doc_ids:[$id],delete_file:false,delete_llm_cache:false}' \
  | curl --fail --silent --show-error "${headers[@]}" -X DELETE -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/delete_document")
jq -e --arg id "$document_id" '.status == "deletion_started" and .doc_id == $id' <<<"$delete_response" >/dev/null
for _ in $(seq 1 120); do
  if ! jq -e --arg id "$document_id" 'any(.documents[]; .id == $id)' <<<"$(list_documents)" >/dev/null; then
    echo "LightRAG lifecycle PASS: ingest, four retrieval modes, restart, backup/restore, auth and exact delete"
    exit 0
  fi
  sleep 2
done
echo "LightRAG exact deletion did not complete" >&2
exit 1
