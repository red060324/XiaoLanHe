#!/usr/bin/env bash
set -euo pipefail

bootstrap_phase=preflight
compose=()

run_bounded_diagnostic() {
  python3 -c 'import subprocess, sys
try:
    result = subprocess.run(sys.argv[2:], timeout=float(sys.argv[1]))
except (OSError, subprocess.TimeoutExpired):
    raise SystemExit(124)
raise SystemExit(result.returncode)' 5 "$@"
}

report_bootstrap_exit() {
  exit_code=$?
  trap - EXIT
  if (( exit_code == 0 )); then
    return
  fi
  set +e +u
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    printf '::error title=LightRAG bootstrap failed::phase=%s exit_code=%s\n' "$bootstrap_phase" "$exit_code" >&2
  else
    printf 'LightRAG bootstrap failed: phase=%s exit_code=%s\n' "$bootstrap_phase" "$exit_code" >&2
  fi

  if [[ "$bootstrap_phase" == "dependency_start" && ${#compose[@]} -gt 0 ]]; then
    for service in milvus-etcd milvus-minio milvus; do
      container_id=$(run_bounded_diagnostic "${compose[@]}" ps --all --quiet "$service" 2>/dev/null | head -n 1)
      state=absent
      health=none
      container_exit_code=unknown
      if [[ -n "$container_id" ]]; then
        details=$(run_bounded_diagnostic docker inspect \
          --format '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{.State.ExitCode}}' \
          "$container_id" 2>/dev/null || true)
        if [[ "$details" =~ ^(created|running|paused|restarting|removing|exited|dead)\|(starting|healthy|unhealthy|none)\|([0-9]+)$ ]]; then
          state=${BASH_REMATCH[1]}
          health=${BASH_REMATCH[2]}
          container_exit_code=${BASH_REMATCH[3]}
        else
          state=unknown
          health=unknown
        fi
      fi
      if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
        printf '::error title=LightRAG dependency state::service=%s state=%s health=%s exit_code=%s\n' \
          "$service" "$state" "$health" "$container_exit_code" >&2
      else
        printf 'LightRAG dependency state: service=%s state=%s health=%s exit_code=%s\n' \
          "$service" "$state" "$health" "$container_exit_code" >&2
      fi
    done
  fi
  exit "$exit_code"
}
trap report_bootstrap_exit EXIT

for name in XLH_LIGHTRAG_DEPLOYMENT_GENERATION XLH_LIGHTRAG_ATTEMPT_ID XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR; do
  [[ -n "${!name:-}" ]] || { echo "$name is required" >&2; exit 2; }
done
generation=$XLH_LIGHTRAG_DEPLOYMENT_GENERATION
attempt_id=$XLH_LIGHTRAG_ATTEMPT_ID
uncontrolled_writers_attestation=$XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION
evidence_dir=$XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR
fence_dir=$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR
compose_file=${1:-deploy/docker-compose.lightrag.yml}
project=${2:-}
if [[ "$project" == "-" ]]; then
  project=
fi
for command_name in docker python3; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 2; }
done
for name in XLH_LIGHTRAG_API_KEY XLH_LIGHTRAG_LLM_API_KEY XLH_LIGHTRAG_EMBEDDING_API_KEY XLH_MILVUS_MINIO_USER XLH_MILVUS_MINIO_PASSWORD XLH_MILVUS_ROOT_PASSWORD XLH_MILVUS_TOKEN; do
  [[ -n "${!name:-}" ]] || { echo "$name is required" >&2; exit 2; }
done
docker compose version >/dev/null
[[ "$generation" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || { echo "invalid deployment generation" >&2; exit 2; }
[[ "$attempt_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || { echo "invalid attempt ID" >&2; exit 2; }
[[ "$uncontrolled_writers_attestation" == "no_uncontrolled_writers" ]] || {
  echo "XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION must be exactly no_uncontrolled_writers" >&2
  exit 2
}
if [[ -n "$project" && ! "$project" =~ ^[a-z0-9][a-z0-9_-]{0,63}$ ]]; then
  echo "invalid Compose project" >&2
  exit 2
fi

[[ "$evidence_dir" == /* ]] || { echo "XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR must be absolute" >&2; exit 2; }
[[ "$fence_dir" == /* ]] || { echo "XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR must be absolute" >&2; exit 2; }
mkdir -p -m 700 "$evidence_dir" "$fence_dir"
evidence_dir=$(cd "$evidence_dir" && pwd -P)
fence_dir=$(cd "$fence_dir" && pwd -P)
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR=$evidence_dir
export XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR=$fence_dir

compose=(docker compose)
if [[ -n "$project" ]]; then
  compose+=(--project-name "$project")
fi
compose+=(-f "$compose_file")
if (( $# > 2 )); then
  for override in "${@:3}"; do
    [[ -n "$override" ]] || { echo "Compose override path must not be empty" >&2; exit 2; }
    compose+=(-f "$override")
  done
fi
compose+=(--profile bootstrap)

# Compose interpolates every service even for `ps`, including the inactive steady
# service. Export the real contract plus a temporary evidence digest before the first
# Compose call; no service runs while proving that no managed container exists.
contract_sha256=$(bash deploy/lightrag-contract-hash.sh)
export XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=$contract_sha256
export XLH_LIGHTRAG_ATTEMPT_ID=$attempt_id
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=sha256:0000000000000000000000000000000000000000000000000000000000000000

# Writer evidence is produced outside the controller. Docker proves that the managed
# service is absent; the caller separately attests that no uncontrolled writer can
# reach the same working directory or Milvus namespace.
bootstrap_phase=managed_service_absence
lightrag_containers=$("${compose[@]}" ps --all --quiet lightrag)
container_count=$(printf '%s\n' "$lightrag_containers" | awk 'NF { count++ } END { print count+0 }')
if (( container_count != 0 )); then
  echo "empty bootstrap requires the managed LightRAG service to be absent" >&2
  exit 1
fi

bootstrap_phase=writer_evidence
evidence_path="$evidence_dir/$attempt_id.json"
writer_evidence_sha256=$(python3 - "$evidence_path" "$attempt_id" "$generation" "$contract_sha256" "$uncontrolled_writers_attestation" <<'PY'
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
    "operation": "bootstrap_empty",
    "contract_sha256": sys.argv[4],
    "issued_at": issued.isoformat().replace("+00:00", "Z"),
    "expires_at": (issued + datetime.timedelta(minutes=30)).isoformat().replace("+00:00", "Z"),
    "pipeline_idle_observed": True,
    "server_replicas": 0,
    "automatic_restart_disabled": True,
    "observer": "lightrag-bootstrap-empty",
    "uncontrolled_writers_attestation": sys.argv[5],
}
canonical = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
try:
    with os.fdopen(fd, "wb") as output:
        output.write(canonical + b"\n")
        output.flush()
        os.fsync(output.fileno())
finally:
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
print("sha256:" + hashlib.sha256(canonical).hexdigest())
PY
)
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$writer_evidence_sha256

# Starts dependencies and a fresh empty bootstrap attempt, but never starts LightRAG.
# The bootstrap refuses nonempty sources/targets and therefore cannot erase real data.
bootstrap_phase=dependency_start
"${compose[@]}" up --detach --wait milvus-etcd milvus-minio milvus
bootstrap_phase=milvus_rbac
"${compose[@]}" run --rm --no-deps milvus-init
bootstrap_phase=fence_controller
"${compose[@]}" run --rm --no-deps lightrag-bootstrap

bootstrap_phase=fence_verify
bash deploy/check-lightrag-fence.sh "$fence_dir" "$generation" "$contract_sha256"
bootstrap_phase=complete
printf 'XLH_LIGHTRAG_DEPLOYMENT_GENERATION=%s\n' "$generation"
printf 'XLH_LIGHTRAG_ATTEMPT_ID=%s\n' "$attempt_id"
printf 'XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=%s\n' "$writer_evidence_sha256"
printf 'XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=%s\n' "$contract_sha256"
