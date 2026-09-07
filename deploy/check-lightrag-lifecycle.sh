#!/usr/bin/env bash
set -euo pipefail

# Destructive only to a uniquely named, disposable Compose project and its volumes.
# Real provider calls may incur cost, so the acknowledgement is deliberately explicit.
if [[ "${XLH_LIGHTRAG_LIFECYCLE_ACK:-}" != "isolated-destructive-test" ]]; then
  echo "set XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test to run the isolated lifecycle test" >&2
  exit 2
fi
for name in \
  XLH_LIGHTRAG_API_KEY \
  XLH_LIGHTRAG_LLM_API_KEY \
  XLH_LIGHTRAG_EMBEDDING_API_KEY \
  XLH_MILVUS_MINIO_USER \
  XLH_MILVUS_MINIO_PASSWORD \
  XLH_MILVUS_ROOT_PASSWORD \
  XLH_MILVUS_TOKEN; do
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required" >&2
    exit 2
  fi
done
if (( ${#XLH_LIGHTRAG_API_KEY} < 32 || ${#XLH_LIGHTRAG_API_KEY} > 512 )) || [[ "$XLH_LIGHTRAG_API_KEY" == *$'\n'* || "$XLH_LIGHTRAG_API_KEY" == *$'\r'* ]]; then
  echo "XLH_LIGHTRAG_API_KEY must contain 32-512 characters without line breaks" >&2
  exit 2
fi
for command_name in curl docker jq python3 tar; do
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
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION="lifecycle-${run_suffix}"
source_generation=$XLH_LIGHTRAG_DEPLOYMENT_GENERATION
temp_parent=$(cd "${TMPDIR:-/tmp}" && pwd -P)
temp_root=$(mktemp -d "$temp_parent/xlh-lightrag-lifecycle.XXXXXX")
backup_dir="$temp_root/backup"
fence_dir="$temp_root/fence"
writer_evidence_dir="$temp_root/writer-evidence"
mkdir -m 700 "$backup_dir" "$fence_dir" "$writer_evidence_dir"
export XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR="$fence_dir"
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR="$writer_evidence_dir"
export XLH_LIGHTRAG_BACKUP_HOST_DIR="$backup_dir"
# The fresh, uniquely named project check below is the lifecycle runner's
# external observation that no process can own its not-yet-created volumes.
export XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers
readonly image_ref='ghcr.io/hkuds/lightrag:v1.5.7@sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e'
readonly source_key='xlh-7ed76b602fa608069361918764653e17f8d4a41d25d86c1b8b4fe406599c6f03.txt'
readonly query_text='Which hero protects Azure Harbor and what relic powers the shield?'
headers=(-H "X-API-Key: $XLH_LIGHTRAG_API_KEY" -H 'LIGHTRAG-WORKSPACE: xiaolanhe_v1')
compose=(docker compose -p "$project" -f "$compose_file")
readonly -a consistency_components=(working_directory milvus etcd minio)
readonly -a consistency_volumes=(xlh-lightrag-data xlh-milvus-data xlh-milvus-etcd-data xlh-milvus-minio-data)
lifecycle_started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
lifecycle_phase=preflight
lifecycle_target=contract
lifecycle_completed=false
compose_cleanup_required=false
lifecycle_failure_report="$temp_root/lifecycle-failure.json"

write_lifecycle_failure_report() {
  local exit_code=$1 compose_cleanup=$2
  python3 - "$lifecycle_failure_report" "$fence_dir" "$lifecycle_started_at" \
    "$lifecycle_phase" "$lifecycle_target" "$exit_code" "$run_suffix" \
    "$project" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$compose_cleanup" <<'PY'
import datetime
import json
import os
import pathlib
import sys
import tempfile

(
    output_name,
    fence_name,
    started_at,
    phase,
    target,
    exit_code,
    run_id,
    project,
    generation,
    compose_cleanup,
) = sys.argv[1:]
output = pathlib.Path(output_name)
fence = pathlib.Path(fence_name)
fence_summary = None
native_failure = None
try:
    marker = json.loads((fence / "current.json").read_text(encoding="utf-8"))
    if isinstance(marker, dict):
        fence_summary = {
            key: marker.get(key)
            for key in (
                "state",
                "reason_code",
                "attempt_id",
                "generation",
                "operation",
                "report_path",
                "report_sha256",
            )
        }
        attempt_id = marker.get("attempt_id")
        if marker.get("state") == "failed" and isinstance(attempt_id, str):
            report_path = fence / "reports" / f"{attempt_id}.json"
            report = json.loads(report_path.read_text(encoding="utf-8"))
            if isinstance(report, dict) and report.get("state") == "failed":
                native_failure = {
                    key: report.get(key)
                    for key in (
                        "state",
                        "reason_code",
                        "attempt_id",
                        "generation",
                        "operation",
                        "errors",
                    )
                }
except (FileNotFoundError, json.JSONDecodeError, OSError, TypeError, ValueError):
    pass

payload = {
    "schema_version": 1,
    "evidence_type": "xlh.lightrag_lifecycle_failure.v1",
    "state": "failed",
    "reason_code": "operation_failed",
    "run_id": run_id,
    "project": project,
    "generation": generation,
    "started_at": started_at,
    "finished_at": datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    "exit_code": int(exit_code),
    "errors": [
        {
            "stage": phase,
            "target": target,
            "code": "lifecycle_command_failed",
            "retryable": False,
        }
    ],
    "cleanup": {"compose_down": compose_cleanup},
    "fence": fence_summary,
    "native_failure": native_failure,
}
encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")
print(encoded.decode("utf-8"))
file_descriptor, temporary_name = tempfile.mkstemp(prefix=".lifecycle-failure.", dir=output.parent)
try:
    os.fchmod(file_descriptor, 0o600)
    with os.fdopen(file_descriptor, "wb") as destination:
        destination.write(encoded + b"\n")
        destination.flush()
        os.fsync(destination.fileno())
    os.replace(temporary_name, output)
    directory = os.open(output.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
finally:
    try:
        os.unlink(temporary_name)
    except FileNotFoundError:
        pass
PY
}

cleanup() {
  local original_status=$? final_status compose_cleanup_status cleanup_status failure_json="" failure_reported=false
  final_status=$original_status
  compose_cleanup_status=not_run
  trap - EXIT INT TERM
  set +e

  if [[ "$lifecycle_completed" != "true" && "$final_status" == "0" ]]; then
    final_status=1
    lifecycle_phase=cleanup
    lifecycle_target=lifecycle-completion
  fi
  if (( final_status != 0 )); then
    failure_json=$(write_lifecycle_failure_report "$final_status" pending) || true
    echo "LightRAG lifecycle failed report:" >&2
    if [[ -n "$failure_json" ]]; then
      printf '%s\n' "$failure_json" >&2
    elif [[ -s "$lifecycle_failure_report" ]]; then
      jq -c . "$lifecycle_failure_report" >&2 || true
    else
      jq -nc --arg stage "$lifecycle_phase" --arg target "$lifecycle_target" \
        --argjson exit_code "$final_status" \
        '{schema_version:1,evidence_type:"xlh.lightrag_lifecycle_failure.v1",state:"failed",reason_code:"operation_failed",exit_code:$exit_code,errors:[{stage:$stage,target:$target,code:"failure_report_write_failed",retryable:false}]}' >&2 || true
    fi
    echo "LightRAG lifecycle failure evidence retained at: $temp_root" >&2
    failure_reported=true
  fi

  if [[ "$compose_cleanup_required" == "true" ]]; then
    "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1
    cleanup_status=$?
    if (( cleanup_status == 0 )); then
      compose_cleanup_status=succeeded
    else
      compose_cleanup_status=failed
      if (( final_status == 0 )); then
        final_status=$cleanup_status
        lifecycle_phase=cleanup
        lifecycle_target=compose-project
      fi
    fi
  fi

  if (( final_status == 0 )); then
    case "$temp_root" in
      "$temp_parent"/xlh-lightrag-lifecycle.*)
        rm -rf -- "$temp_root"
        cleanup_status=$?
        if (( cleanup_status != 0 )); then
          final_status=$cleanup_status
          lifecycle_phase=cleanup
          lifecycle_target=temporary-evidence
        fi
        ;;
      *)
        echo "refusing to remove unexpected lifecycle temporary path: $temp_root" >&2
        final_status=1
        lifecycle_phase=cleanup
        lifecycle_target=temporary-evidence
        ;;
    esac
  fi
  if (( final_status != 0 )); then
    # Refresh the retained copy with the cleanup outcome. The diagnostic above
    # was already persisted and printed before cleanup, so cleanup cannot hide it.
    failure_json=$(write_lifecycle_failure_report "$final_status" "$compose_cleanup_status") || true
    if [[ "$failure_reported" != "true" ]]; then
      echo "LightRAG lifecycle failed report:" >&2
      if [[ -n "$failure_json" ]]; then
        printf '%s\n' "$failure_json" >&2
      elif [[ -s "$lifecycle_failure_report" ]]; then
        jq -c . "$lifecycle_failure_report" >&2 || true
      else
        jq -nc --arg stage "$lifecycle_phase" --arg target "$lifecycle_target" \
          --argjson exit_code "$final_status" \
          '{schema_version:1,evidence_type:"xlh.lightrag_lifecycle_failure.v1",state:"failed",reason_code:"operation_failed",exit_code:$exit_code,errors:[{stage:$stage,target:$target,code:"failure_report_write_failed",retryable:false}]}' >&2 || true
      fi
      echo "LightRAG lifecycle failure evidence retained at: $temp_root" >&2
    fi
  fi

  exit "$final_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

contract_sha256=$(bash deploy/lightrag-contract-hash.sh)
lifecycle_target=compose-project
existing_containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
existing_volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$project")
existing_networks=$(docker network ls -q --filter "label=com.docker.compose.project=$project")
if [[ -n "$existing_containers$existing_volumes$existing_networks" ]]; then
  echo "isolated Compose project already exists; choose a fresh XLH_LIGHTRAG_LIFECYCLE_RUN_ID" >&2
  exit 2
fi
compose_cleanup_required=true

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
    lifecycle_phase=retrieval
    lifecycle_target=$mode
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

canonical_sha256() {
  python3 - "$1" <<'PY'
import hashlib
import json
import pathlib
import sys

value = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
print("sha256:" + hashlib.sha256(canonical).hexdigest())
PY
}

create_writer_evidence() {
  local operation=$1 attempt_id=$2 generation=$3 output=$4
  local status health running restart_policy pipeline_idle lightrag_container
  status=$(curl --fail --silent --show-error "${headers[@]}" "$base_url/documents/pipeline_status")
  pipeline_idle=$(jq -er '.busy == false and .recovery_required == false' <<<"$status")
  [[ "$pipeline_idle" == "true" ]] || { echo "LightRAG pipeline is not idle" >&2; return 1; }
  health=$(curl --fail --silent --show-error "${headers[@]}" "$base_url/health")
  jq -e '.pipeline_active == false' <<<"$health" >/dev/null || { echo "LightRAG health still reports an active pipeline" >&2; return 1; }
  "${compose[@]}" stop --timeout 30 lightrag
  lightrag_container=$("${compose[@]}" ps --all --quiet lightrag)
  [[ -n "$lightrag_container" ]] || { echo "managed LightRAG container is missing" >&2; return 1; }
  read -r running restart_policy < <(docker inspect --format '{{.State.Running}} {{.HostConfig.RestartPolicy.Name}}' "$lightrag_container")
  [[ "$running" == "false" ]] || { echo "managed LightRAG container did not stop" >&2; return 1; }
  if [[ "$restart_policy" != "no" ]]; then
    docker update --restart=no "$lightrag_container" >/dev/null
    restart_policy=$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' "$lightrag_container")
  fi
  [[ "$restart_policy" == "no" ]] || { echo "managed LightRAG automatic restart remains enabled" >&2; return 1; }
  python3 - "$output" "$attempt_id" "$generation" "$operation" "$contract_sha256" <<'PY'
import datetime
import hashlib
import json
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
issued = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
payload = {
    "schema_version": 2,
    "evidence_type": "xlh.lightrag_writer_fence.v2",
    "attempt_id": sys.argv[2],
    "generation": sys.argv[3],
    "operation": sys.argv[4],
    "contract_sha256": sys.argv[5],
    "issued_at": issued.isoformat().replace("+00:00", "Z"),
    "expires_at": (issued + datetime.timedelta(minutes=30)).isoformat().replace("+00:00", "Z"),
    "pipeline_idle_observed": True,
    "server_replicas": 0,
    "automatic_restart_disabled": True,
    "observer": "lightrag-lifecycle.backup",
    "uncontrolled_writers_attestation": "no_uncontrolled_writers",
}
canonical = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "wb") as output_file:
    output_file.write(canonical + b"\n")
    output_file.flush()
    os.fsync(output_file.fileno())
PY
}

project_volume() {
  local volume_name=$1 ids count
  ids=$(docker volume ls --quiet \
    --filter "label=com.docker.compose.project=$project" \
    --filter "label=com.docker.compose.volume=$volume_name")
  count=$(printf '%s\n' "$ids" | awk 'NF { count++ } END { print count+0 }')
  [[ "$count" == "1" ]] || { echo "expected exactly one isolated $volume_name volume, found $count" >&2; return 1; }
  printf '%s\n' "$ids"
}

archive_volume() {
  local component=$1 volume_name=$2 volume_id archive_path
  volume_id=$(project_volume "$volume_name")
  archive_path="$backup_dir/$component.tar"
  docker run --rm --entrypoint sh -v "$volume_id:/source:ro" -v "$backup_dir:/backup" "$image_ref" \
    -c "cd /source && tar -cf /backup/$component.tar ."
  [[ -s "$archive_path" ]] || { echo "$component backup archive is empty" >&2; return 1; }
}

write_backup_report() {
  local output=$1 writer_sha=$2
  python3 - "$backup_dir" "$output" "$source_generation" "$contract_sha256" "$writer_sha" <<'PY'
import hashlib
import json
import os
import pathlib
import tarfile
import sys

root, output = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
source_generation, contract_sha256, writer_sha256 = sys.argv[3:]
configuration_sha256 = contract_sha256
def file_digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1024 * 1024):
            value.update(chunk)
    return "sha256:" + value.hexdigest()

components = {}
for name in ("working_directory", "milvus", "etcd", "minio"):
    archive = root / f"{name}.tar"
    archive_digest = file_digest(archive)
    members = []
    with tarfile.open(archive, "r:*") as source:
        for member in source.getmembers():
            path = pathlib.PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or member.issym() or member.islnk() or member.isdev():
                raise SystemExit(f"unsafe backup member: {name}:{member.name}")
            if not member.isfile():
                if member.isdir():
                    continue
                raise SystemExit(f"unsupported backup member: {name}:{member.name}")
            normalized = pathlib.PurePosixPath(*[part for part in path.parts if part not in ("", ".")]).as_posix()
            if not normalized or normalized in {".", ".."}:
                raise SystemExit(f"invalid backup member: {name}:{member.name}")
            extracted = source.extractfile(member)
            if extracted is None:
                raise SystemExit(f"unreadable backup member: {name}:{member.name}")
            digest = hashlib.sha256()
            size = 0
            while chunk := extracted.read(1024 * 1024):
                digest.update(chunk)
                size += len(chunk)
            if size != member.size:
                raise SystemExit(f"truncated backup member: {name}:{member.name}")
            members.append({"path": normalized, "size_bytes": size, "sha256": "sha256:" + digest.hexdigest()})
    members.sort(key=lambda item: item["path"].encode("utf-8"))
    if len({item["path"] for item in members}) != len(members):
        raise SystemExit(f"duplicate normalized member path: {name}")
    members_bytes = json.dumps(members, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    components[name] = {
        "source_generation": source_generation,
        "contract_sha256": contract_sha256,
        "configuration_sha256": configuration_sha256,
        "writer_evidence_sha256": writer_sha256,
        "archive_path": archive.name,
        "size_bytes": archive.stat().st_size,
        "sha256": archive_digest,
        "member_count": len(members),
        "members_sha256": "sha256:" + hashlib.sha256(members_bytes).hexdigest(),
        "members": members,
    }
manifest = {
    "schema_version": 2,
    "source_generation": source_generation,
    "contract_sha256": contract_sha256,
    "configuration_sha256": configuration_sha256,
    "writer_evidence_sha256": writer_sha256,
    "components": components,
}
manifest_bytes = json.dumps(manifest, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
envelope = {
    "schema_version": 2,
    "evidence_type": "xlh.lightrag_backup.v2",
    "manifest": manifest,
    "manifest_sha256": "sha256:" + hashlib.sha256(manifest_bytes).hexdigest(),
}
canonical = json.dumps(envelope, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "wb") as report:
    report.write(canonical + b"\n")
    report.flush()
    os.fsync(report.fileno())
PY
}

validate_backup_report() {
  python3 - "$1" "$backup_dir" "$source_generation" "$contract_sha256" "$2" <<'PY'
import hashlib
import json
import pathlib
import tarfile
import sys

report_path, root = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
expected_generation, expected_contract, expected_writer = sys.argv[3:]
def file_digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1024 * 1024):
            value.update(chunk)
    return "sha256:" + value.hexdigest()

envelope = json.loads(report_path.read_text(encoding="utf-8"))
if set(envelope) != {"schema_version", "evidence_type", "manifest", "manifest_sha256"}:
    raise SystemExit("backup envelope keys mismatch")
if envelope["schema_version"] != 2 or envelope["evidence_type"] != "xlh.lightrag_backup.v2":
    raise SystemExit("backup envelope identity mismatch")
manifest = envelope["manifest"]
manifest_keys = {"schema_version", "source_generation", "contract_sha256", "configuration_sha256", "writer_evidence_sha256", "components"}
if set(manifest) != manifest_keys or manifest["schema_version"] != 2:
    raise SystemExit("backup manifest keys mismatch")
manifest_bytes = json.dumps(manifest, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
if "sha256:" + hashlib.sha256(manifest_bytes).hexdigest() != envelope["manifest_sha256"]:
    raise SystemExit("backup manifest digest mismatch")
if manifest["source_generation"] != expected_generation or manifest["contract_sha256"] != expected_contract or manifest["writer_evidence_sha256"] != expected_writer:
    raise SystemExit("backup manifest binding mismatch")
if manifest["configuration_sha256"] != expected_contract:
    raise SystemExit("backup configuration digest mismatch")
if set(manifest["components"]) != {"working_directory", "milvus", "etcd", "minio"}:
    raise SystemExit("backup component set mismatch")
component_keys = {"source_generation", "contract_sha256", "configuration_sha256", "writer_evidence_sha256", "archive_path", "size_bytes", "sha256", "member_count", "members_sha256", "members"}
member_keys = {"path", "size_bytes", "sha256"}
for name, component in manifest["components"].items():
    if set(component) != component_keys:
        raise SystemExit(f"{name} component keys mismatch")
    for binding in ("source_generation", "contract_sha256", "configuration_sha256", "writer_evidence_sha256"):
        if component[binding] != manifest[binding]:
            raise SystemExit(f"{name} mixed-generation/configuration binding")
    relative = pathlib.PurePosixPath(component["archive_path"])
    if relative.is_absolute() or ".." in relative.parts or relative.name != f"{name}.tar":
        raise SystemExit(f"{name} archive path is unsafe")
    archive = root / relative.as_posix()
    if not archive.is_file() or archive.is_symlink() or archive.stat().st_size != component["size_bytes"]:
        raise SystemExit(f"{name} archive size mismatch")
    if file_digest(archive) != component["sha256"]:
        raise SystemExit(f"{name} archive digest mismatch")
    expected_members = component["members"]
    if not isinstance(expected_members, list) or any(set(item) != member_keys for item in expected_members):
        raise SystemExit(f"{name} member schema mismatch")
    if expected_members != sorted(expected_members, key=lambda item: item["path"].encode("utf-8")) or len({item["path"] for item in expected_members}) != len(expected_members):
        raise SystemExit(f"{name} member order/path duplication mismatch")
    member_bytes = json.dumps(expected_members, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    if component["member_count"] != len(expected_members) or "sha256:" + hashlib.sha256(member_bytes).hexdigest() != component["members_sha256"]:
        raise SystemExit(f"{name} member manifest mismatch")
    actual = []
    with tarfile.open(archive, "r:*") as source:
        for member in source.getmembers():
            path = pathlib.PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or member.issym() or member.islnk() or member.isdev():
                raise SystemExit(f"unsafe backup member: {name}:{member.name}")
            if not member.isfile():
                if member.isdir():
                    continue
                raise SystemExit(f"unsupported backup member: {name}:{member.name}")
            normalized = pathlib.PurePosixPath(*[part for part in path.parts if part not in ("", ".")]).as_posix()
            extracted = source.extractfile(member)
            if not normalized or normalized in {".", ".."} or extracted is None:
                raise SystemExit(f"invalid backup member: {name}:{member.name}")
            digest = hashlib.sha256()
            size = 0
            while chunk := extracted.read(1024 * 1024):
                digest.update(chunk)
                size += len(chunk)
            actual.append({"path": normalized, "size_bytes": size, "sha256": "sha256:" + digest.hexdigest()})
    actual.sort(key=lambda item: item["path"].encode("utf-8"))
    if actual != expected_members:
        raise SystemExit(f"{name} archive member mismatch")
PY
}

restore_volume() {
  local component=$1 volume_name=$2 volume_id
  volume_id=$(project_volume "$volume_name")
  docker run --rm --entrypoint sh -v "$volume_id:/target" -v "$backup_dir:/backup:ro" "$image_ref" \
    -c "cd /target && tar -xf /backup/$component.tar"
}

assert_volume_empty() {
  local volume_name=$1 volume_id
  volume_id=$(project_volume "$volume_name")
  docker run --rm --entrypoint sh -v "$volume_id:/target" "$image_ref" \
    -c 'test -z "$(find /target -mindepth 1 -print -quit)"'
}

write_restore_fixture_command() {
  local command_path=$1
  python3 - "$command_path" "$source_key" "$query_text" <<'PY'
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
source_key, query_text = sys.argv[2:]
script = f'''#!/usr/bin/env python3
import asyncio
import hashlib
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

SOURCE_KEY = {source_key!r}
QUERY_TEXT = {query_text!r}
BASE_URL = "http://127.0.0.1:9621"
REPORT = pathlib.Path("/rebuild-fence/restore-fixtures.json")

def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")

def request(path, payload=None, authenticated=True):
    headers = {{"LIGHTRAG-WORKSPACE": "xiaolanhe_v1"}}
    if authenticated:
        headers["X-API-Key"] = os.environ["LIGHTRAG_API_KEY"]
    data = None
    if payload is not None:
        data = canonical(payload)
        headers["Content-Type"] = "application/json"
    with urllib.request.urlopen(urllib.request.Request(BASE_URL + path, data=data, headers=headers), timeout=30) as response:
        return json.load(response)

REPORT.unlink(missing_ok=True)
fixture_environment = dict(os.environ)
fixture_environment.update({{"HOST": "127.0.0.1", "PORT": "9621", "WORKERS": "1"}})
with open("/tmp/xlh-restore-fixture-server.log", "wb") as log:
    server = subprocess.Popen(["lightrag-gunicorn"], env=fixture_environment, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
try:
    for _ in range(120):
        if server.poll() is not None:
            raise SystemExit("temporary LightRAG fixture server exited early")
        try:
            if request("/health", authenticated=False).get("status") == "healthy":
                break
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            pass
        time.sleep(1)
    else:
        raise SystemExit("temporary LightRAG fixture server did not become healthy")
    if request("/auth/verify").get("status") != "ok":
        raise SystemExit("fixture authentication failed")
    pipeline = request("/documents/pipeline_status")
    if pipeline.get("busy") is not False or pipeline.get("recovery_required") is not False:
        raise SystemExit("restored pipeline is not idle")
    documents = request("/documents/paginated", {{"page": 1, "page_size": 200, "sort_field": "updated_at", "sort_direction": "desc"}})
    matches = [item for item in documents.get("documents", []) if item.get("file_path") == SOURCE_KEY and item.get("status") == "PROCESSED"]
    if len(matches) != 1 or not isinstance(matches[0].get("chunks_count"), int) or isinstance(matches[0].get("chunks_count"), bool) or matches[0]["chunks_count"] < 1:
        raise SystemExit("restored fixture document is missing")
    modes = ["local", "global", "hybrid", "mix"]
    required_by_mode = {{
        "local": ("managed_entities",),
        "global": ("managed_relationships",),
        "hybrid": ("managed_entities", "managed_relationships"),
        "mix": ("managed_chunks",),
    }}
    mode_results = {{}}
    aggregate = {{"managed_chunks": 0, "managed_entities": 0, "managed_relationships": 0, "managed_citations": 0}}
    chunk_content = []
    for mode in modes:
        response = request("/query/data", {{
            "query": QUERY_TEXT, "mode": mode, "top_k": 20, "chunk_top_k": 12,
            "max_total_tokens": 12000, "include_references": True,
            "hl_keywords": ["Azure Harbor protection", "Moonstone shield"],
            "ll_keywords": ["Azure Harbor", "Lin", "Moonstone"],
        }})
        data = response.get("data")
        metadata = response.get("metadata")
        if response.get("status") != "success" or not isinstance(data, dict) or not isinstance(metadata, dict) or metadata.get("query_mode") != mode:
            raise SystemExit(f"restored {{mode}} retrieval fixture failed")
        for key in ("chunks", "entities", "relationships", "references"):
            if not isinstance(data.get(key), list):
                raise SystemExit(f"restored {{mode}} {{key}} payload is malformed")
        managed_references = {{
            item.get("reference_id")
            for item in data["references"]
            if isinstance(item, dict) and isinstance(item.get("reference_id"), str)
            and item.get("reference_id") and item.get("file_path") == SOURCE_KEY
        }}
        if not managed_references:
            raise SystemExit(f"restored {{mode}} retrieval has no managed citation")
        counts = {{}}
        for key in ("chunks", "entities", "relationships"):
            expected = []
            for item in data[key]:
                if not isinstance(item, dict) or item.get("file_path") != SOURCE_KEY:
                    continue
                if key == "chunks" and item.get("reference_id") in managed_references and isinstance(item.get("content"), str) and item["content"].strip():
                    expected.append(item)
                    chunk_content.append(item["content"].casefold())
                elif key == "entities" and isinstance(item.get("entity_name"), str) and item["entity_name"].strip() and isinstance(item.get("description"), str) and item["description"].strip():
                    expected.append(item)
                elif key == "relationships" and all(isinstance(item.get(field), str) and item[field].strip() for field in ("src_id", "tgt_id", "description")):
                    expected.append(item)
            result_key = "managed_" + key
            counts[result_key] = len(expected)
            aggregate[result_key] += len(expected)
        aggregate["managed_citations"] += len(managed_references)
        if any(counts[key] < 1 for key in required_by_mode[mode]):
            raise SystemExit(f"restored {{mode}} retrieval missed required source-bound evidence")
        mode_results[mode] = {{**counts, "managed_citations": len(managed_references), "passed": True}}
    for key in ("managed_chunks", "managed_entities", "managed_relationships"):
        if aggregate[key] < 1:
            raise SystemExit(f"restored fixture has no {{key.removeprefix('managed_')}} evidence")
    semantic_signals = ("azure harbor", "lin", "moonstone")
    semantic_signal_hits = sum(any(signal in content for content in chunk_content) for signal in semantic_signals)
    semantic_signal_threshold = len(semantic_signals)
    if semantic_signal_hits < semantic_signal_threshold:
        raise SystemExit("restored fixture failed its semantic-signal quality threshold")
    results = {{
        "document_present": True,
        "mode_results": mode_results,
        "aggregate": aggregate,
        "quality": {{
            "mode_passes": len(mode_results), "mode_threshold": len(modes),
            "semantic_signal_hits": semantic_signal_hits,
            "semantic_signal_threshold": semantic_signal_threshold, "passed": True,
        }},
    }}
finally:
    if server.poll() is None:
        os.killpg(server.pid, signal.SIGTERM)
        try:
            server.wait(timeout=30)
        except subprocess.TimeoutExpired:
            os.killpg(server.pid, signal.SIGKILL)
            server.wait(timeout=10)

sys.path.insert(0, "/opt/xlh")
import rebuild_fence

async def source_sha256_after_fixture():
    tool, module = await rebuild_fence.setup_tool()
    try:
        _, source = await rebuild_fence.expected_ids(tool, module)
        return source["sha256"]
    finally:
        await rebuild_fence.finalize_tool(tool)

if asyncio.run(source_sha256_after_fixture()) != os.environ["XLH_REBUILD_SOURCE_SHA256"]:
    raise SystemExit("authoritative source changed while running restore fixtures")
results["source_unchanged_after_fixture"] = True
passed_checks = 1 + len(mode_results) + len(("managed_chunks", "managed_entities", "managed_relationships")) + 1 + 1
payload = {{
    "schema_version": 2, "evidence_type": "xlh.lightrag_fixture.v2",
    "attempt_id": os.environ["XLH_REBUILD_ATTEMPT_ID"],
    "generation": os.environ["XLH_REBUILD_GENERATION"],
    "operation": os.environ["XLH_REBUILD_OPERATION"],
    "contract_sha256": os.environ["XLH_REBUILD_CONTRACT_SHA256"],
    "source_sha256": os.environ["XLH_REBUILD_SOURCE_SHA256"],
    "suite_version": os.environ["XLH_LIGHTRAG_FIXTURE_SUITE_VERSION"],
    "passed": passed_checks, "failed": 0,
    "results_sha256": "sha256:" + hashlib.sha256(canonical(results)).hexdigest(),
}}
temporary = REPORT.with_name(".restore-fixtures.tmp")
with temporary.open("wb") as output:
    output.write(canonical(payload) + b"\\n")
    output.flush()
    os.fsync(output.fileno())
os.replace(temporary, REPORT)
directory = os.open(REPORT.parent, os.O_RDONLY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
'''
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o700)
with os.fdopen(fd, "w", encoding="utf-8") as output:
    output.write(script)
PY
}

echo "Starting isolated LightRAG project: $project"
lifecycle_phase=bootstrap
lifecycle_target=compose-contract
bash deploy/check-lightrag-compose.sh "$compose_file"
bootstrap_attempt_id=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
export XLH_LIGHTRAG_ATTEMPT_ID=$bootstrap_attempt_id
lifecycle_target=empty-fence
bash deploy/lightrag-bootstrap-empty.sh "$compose_file" "$project"
bootstrap_writer_path="$writer_evidence_dir/$bootstrap_attempt_id.json"
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$(canonical_sha256 "$bootstrap_writer_path")
lifecycle_target=milvus-contract
bash deploy/check-lightrag-milvus.sh "$compose_file" "$project"
lifecycle_target=lightrag-service
"${compose[@]}" up --detach --wait --no-deps lightrag
lifecycle_target=lightrag-readiness
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY" "$fence_dir" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$contract_sha256"

lifecycle_phase=ingest
lifecycle_target=document-create
create_response=$(jq -nc --arg source "$source_key" '{
  text:("XiaoLanHe-Knowledge-v1\nTitle: Azure Harbor Shield Guide\nSource-Type: lifecycle-fixture\nGame-Code: azure-harbor\nRegion-Code: CN\nPatch-Version: 1.0\n\nIn Azure Harbor, the hero Lin protects the harbor. The Moonstone relic powers Lin safe shield."),
  file_source:$source
}' | curl --fail --silent --show-error "${headers[@]}" -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/text")
track_id=$(jq -er '.status == "success" and .track_id' <<<"$create_response")
lifecycle_target=document-processing
document_id=$(wait_for_track "$track_id")
lifecycle_target=document-list
before_restart=$(list_documents)
assert_document_present "$before_restart" "$document_id"
assert_queries
lifecycle_phase=ingest
lifecycle_target=duplicate-source-guard
duplicate_status=$(jq -nc --arg source "$source_key" '{text:"duplicate",file_source:$source}' \
  | curl --silent --show-error --output "$backup_dir/duplicate.json" --write-out '%{http_code}' \
      "${headers[@]}" -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/text")
[[ "$duplicate_status" == "409" ]] || { echo "expected duplicate source 409, got $duplicate_status" >&2; exit 1; }

lifecycle_phase=restart
lifecycle_target=lightrag-service
"${compose[@]}" restart --timeout 30 lightrag
"${compose[@]}" up --detach --wait lightrag
lifecycle_target=lightrag-readiness
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY" "$fence_dir" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$contract_sha256"
lifecycle_target=document-persistence
assert_document_present "$(list_documents)" "$document_id"
assert_queries

lifecycle_phase=backup
lifecycle_target=writer-fence
backup_attempt_id=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
backup_writer_path="$writer_evidence_dir/$backup_attempt_id.json"
create_writer_evidence backup "$backup_attempt_id" "$source_generation" "$backup_writer_path"
backup_writer_sha256=$(canonical_sha256 "$backup_writer_path")
lifecycle_target=milvus-consistency-unit
"${compose[@]}" stop --timeout 30 milvus
"${compose[@]}" stop --timeout 30 milvus-etcd milvus-minio
for index in "${!consistency_components[@]}"; do
  lifecycle_target=${consistency_components[$index]}
  archive_volume "${consistency_components[$index]}" "${consistency_volumes[$index]}"
done
lifecycle_target=backup-report
backup_report="$backup_dir/backup-report.json"
write_backup_report "$backup_report" "$backup_writer_sha256"
validate_backup_report "$backup_report" "$backup_writer_sha256"
backup_report_sha256=$(canonical_sha256 "$backup_report")

lifecycle_phase=restore
lifecycle_target=compose-project
"${compose[@]}" down
for volume_name in "${consistency_volumes[@]}"; do
  lifecycle_target=$volume_name
  volume_id=$(project_volume "$volume_name")
  docker volume rm "$volume_id" >/dev/null
  docker volume create --label "com.docker.compose.project=$project" --label "com.docker.compose.volume=$volume_name" "$volume_id" >/dev/null
  assert_volume_empty "$volume_name"
done
restore_generation="restore-${run_suffix}"
[[ "$restore_generation" != "$source_generation" ]] || { echo "restore generation must differ from source generation" >&2; exit 1; }
restore_attempt_id=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
restore_writer_path="$writer_evidence_dir/$restore_attempt_id.json"
# The fresh-volume project is fully stopped, so the external lifecycle step can
# attest zero replicas and disabled restart without consulting the controller.
restored_lightrag_containers=$("${compose[@]}" ps --all --quiet lightrag)
[[ -z "$restored_lightrag_containers" ]] || { echo "LightRAG service must be absent before restore writer attestation" >&2; exit 1; }
lifecycle_target=writer-fence
restore_writer_sha256=$(python3 - "$restore_writer_path" "$restore_attempt_id" "$restore_generation" "$contract_sha256" <<'PY'
import datetime, hashlib, json, os, pathlib, sys
path = pathlib.Path(sys.argv[1])
issued = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
payload = {"schema_version":2,"evidence_type":"xlh.lightrag_writer_fence.v2","attempt_id":sys.argv[2],"generation":sys.argv[3],"operation":"restore_verify","contract_sha256":sys.argv[4],"issued_at":issued.isoformat().replace("+00:00","Z"),"expires_at":(issued+datetime.timedelta(minutes=30)).isoformat().replace("+00:00","Z"),"pipeline_idle_observed":True,"server_replicas":0,"automatic_restart_disabled":True,"observer":"lightrag-lifecycle.restore","uncontrolled_writers_attestation":"no_uncontrolled_writers"}
canonical = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",",":")).encode()
fd = os.open(path, os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
with os.fdopen(fd, "wb") as output:
    output.write(canonical+b"\n"); output.flush(); os.fsync(output.fileno())
print("sha256:"+hashlib.sha256(canonical).hexdigest())
PY
)
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION=$restore_generation
export XLH_LIGHTRAG_ATTEMPT_ID=$restore_attempt_id
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$restore_writer_sha256
lifecycle_target=restore-preflight
"${compose[@]}" run --rm --no-deps --entrypoint python lightrag-bootstrap \
  /opt/xlh/rebuild_fence.py prepare-restore --fence-dir /rebuild-fence \
  --generation "$restore_generation" --attempt-id "$restore_attempt_id" \
  --writer-evidence "/writer-evidence/$restore_attempt_id.json" \
  --writer-evidence-sha256 "$restore_writer_sha256" \
  --backup-report /backup-evidence/backup-report.json \
  --backup-report-sha256 "$backup_report_sha256" \
  --backup-writer-evidence "/writer-evidence/$backup_attempt_id.json" \
  --backup-writer-evidence-sha256 "$backup_writer_sha256" \
  --expected-source-generation "$source_generation"
# Revalidate the complete manifest and all archive/member digests immediately
# before extracting anything into the new, empty consistency-unit volumes.
validate_backup_report "$backup_report" "$backup_writer_sha256"
for index in "${!consistency_components[@]}"; do
  lifecycle_target=${consistency_components[$index]}
  restore_volume "${consistency_components[$index]}" "${consistency_volumes[$index]}"
done
lifecycle_target=milvus-consistency-unit
"${compose[@]}" up --detach --wait milvus-etcd milvus-minio milvus
fixture_report="$fence_dir/restore-fixtures.json"
fixture_command="$backup_dir/restore-fixtures.py"
write_restore_fixture_command "$fixture_command"
lifecycle_target=restore-verification
"${compose[@]}" run --rm --no-deps --entrypoint python lightrag-bootstrap \
  /opt/xlh/rebuild_fence.py restore-verify --fence-dir /rebuild-fence \
  --generation "$restore_generation" --attempt-id "$restore_attempt_id" \
  --writer-evidence "/writer-evidence/$restore_attempt_id.json" \
  --writer-evidence-sha256 "$restore_writer_sha256" \
  --backup-report /backup-evidence/backup-report.json \
  --backup-report-sha256 "$backup_report_sha256" \
  --backup-writer-evidence "/writer-evidence/$backup_attempt_id.json" \
  --backup-writer-evidence-sha256 "$backup_writer_sha256" \
  --expected-source-generation "$source_generation" \
  --fixture-report /rebuild-fence/restore-fixtures.json \
  --fixture-command /backup-evidence/restore-fixtures.py
lifecycle_target=verified-fence
bash deploy/check-lightrag-fence.sh "$fence_dir" "$restore_generation" "$contract_sha256"
lifecycle_target=milvus-contract
bash deploy/check-lightrag-milvus.sh "$compose_file" "$project"
lifecycle_target=lightrag-service
"${compose[@]}" up --detach --wait --no-deps lightrag
lifecycle_target=lightrag-readiness
bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY" "$fence_dir" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$contract_sha256"
lifecycle_target=document-persistence
assert_document_present "$(list_documents)" "$document_id"
assert_queries

lifecycle_phase=delete
lifecycle_target=document-delete
delete_response=$(jq -nc --arg id "$document_id" '{doc_ids:[$id],delete_file:false,delete_llm_cache:false}' \
  | curl --fail --silent --show-error "${headers[@]}" -X DELETE -H 'Content-Type: application/json' --data-binary @- "$base_url/documents/delete_document")
jq -e --arg id "$document_id" '.status == "deletion_started" and .doc_id == $id' <<<"$delete_response" >/dev/null
for _ in $(seq 1 120); do
  if ! jq -e --arg id "$document_id" 'any(.documents[]; .id == $id)' <<<"$(list_documents)" >/dev/null; then
    lifecycle_phase=complete
    lifecycle_target=lifecycle
    lifecycle_completed=true
    echo "LightRAG lifecycle PASS: ingest, four retrieval modes, restart, backup/restore, auth and exact delete"
    exit 0
  fi
  sleep 2
done
echo "LightRAG exact deletion did not complete" >&2
exit 1
