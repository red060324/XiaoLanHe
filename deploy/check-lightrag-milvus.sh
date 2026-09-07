#!/usr/bin/env bash
set -euo pipefail

compose_file=${1:-deploy/docker-compose.lightrag.yml}
project=${2:-}
compose=(docker compose)
if [[ -n "$project" ]]; then
  [[ "$project" =~ ^[a-z0-9][a-z0-9_-]{0,63}$ ]] || { echo "invalid Compose project" >&2; exit 2; }
  compose+=(--project-name "$project")
fi
compose+=(-f "$compose_file")

# Dependency liveness is distinct from collection contract validation.
"${compose[@]}" exec -T milvus curl --fail --silent http://localhost:9091/healthz >/dev/null

"${compose[@]}" run --rm --no-deps -T --entrypoint python lightrag-bootstrap - <<'PY'
import os
from typing import Any

from lightrag.kg.milvus_impl import DataType, MilvusClient

EXPECTED = {
    "xiaolanhe_v1_entities_text_embedding_v4_1024d": {
        "id": (DataType.VARCHAR, 64, False, True),
        "vector": (DataType.FLOAT_VECTOR, 1024, False, False),
        "created_at": (DataType.INT64, None, False, False),
        "entity_name": (DataType.VARCHAR, 512, True, False),
        "content": (DataType.VARCHAR, 65535, True, False),
        "source_id": (DataType.VARCHAR, 65535, True, False),
        "file_path": (DataType.VARCHAR, 32768, True, False),
    },
    "xiaolanhe_v1_relationships_text_embedding_v4_1024d": {
        "id": (DataType.VARCHAR, 64, False, True),
        "vector": (DataType.FLOAT_VECTOR, 1024, False, False),
        "created_at": (DataType.INT64, None, False, False),
        "src_id": (DataType.VARCHAR, 512, True, False),
        "tgt_id": (DataType.VARCHAR, 512, True, False),
        "content": (DataType.VARCHAR, 65535, True, False),
        "source_id": (DataType.VARCHAR, 65535, True, False),
        "file_path": (DataType.VARCHAR, 32768, True, False),
    },
    "xiaolanhe_v1_chunks_text_embedding_v4_1024d": {
        "id": (DataType.VARCHAR, 64, False, True),
        "vector": (DataType.FLOAT_VECTOR, 1024, False, False),
        "created_at": (DataType.INT64, None, False, False),
        "full_doc_id": (DataType.VARCHAR, 64, True, False),
        "content": (DataType.VARCHAR, 65535, True, False),
        "file_path": (DataType.VARCHAR, 32768, True, False),
    },
}


def field_type(field: dict[str, Any]) -> Any:
    value = field.get("data_type", field.get("type", field.get("dtype")))
    try:
        return DataType(value)
    except (TypeError, ValueError):
        normalized = str(value).upper().split(".")[-1].replace("_", "")
        aliases = {
            "VARCHAR": DataType.VARCHAR,
            "FLOATVECTOR": DataType.FLOAT_VECTOR,
            "INT64": DataType.INT64,
        }
        return aliases.get(normalized, value)


def field_limit(field: dict[str, Any], data_type: Any) -> int | None:
    params = field.get("params") or {}
    raw = params.get("dim") if data_type == DataType.FLOAT_VECTOR else params.get("max_length")
    if raw is None:
        raw = field.get("dim") if data_type == DataType.FLOAT_VECTOR else field.get("max_length")
    return int(raw) if raw is not None else None


def index_records(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, list):
        return [item for item in value if isinstance(item, dict)]
    return [value] if isinstance(value, dict) else []


if os.environ.get("MILVUS_DB_NAME") != "lightrag":
    raise SystemExit("MILVUS_DB_NAME must be exactly lightrag")
client = MilvusClient(
    uri=os.environ["MILVUS_URI"],
    db_name="lightrag",
    token=os.environ.get("MILVUS_TOKEN") or None,
)
try:
    if client.get_server_version() != "2.6.11":
        raise SystemExit("unexpected Milvus version")
    collections = set(client.list_collections())
    missing = set(EXPECTED) - collections
    if missing:
        raise SystemExit(f"missing exact LightRAG collections: {sorted(missing)}")
    for name, expected_fields in EXPECTED.items():
        description = client.describe_collection(collection_name=name)
        schema = description.get("schema") if isinstance(description.get("schema"), dict) else {}
        dynamic = description.get("enable_dynamic_field", schema.get("enable_dynamic_field"))
        if dynamic is not True:
            raise SystemExit(f"{name} must enable dynamic fields")
        raw_fields = description.get("fields") or schema.get("fields") or []
        if not isinstance(raw_fields, list):
            raise SystemExit(f"{name} returned a malformed field list")
        fields = {str(field.get("name")): field for field in raw_fields if isinstance(field, dict)}
        if set(fields) != set(expected_fields):
            raise SystemExit(f"{name} field set mismatch: {sorted(fields)}")
        for field_name, (expected_type, expected_limit, nullable, primary) in expected_fields.items():
            field = fields[field_name]
            actual_type = field_type(field)
            if actual_type != expected_type:
                raise SystemExit(f"{name}.{field_name} type mismatch: {actual_type}")
            if field_limit(field, actual_type) != expected_limit:
                raise SystemExit(f"{name}.{field_name} length/dimension mismatch")
            actual_nullable = field.get("nullable", False)
            if not isinstance(actual_nullable, bool) or actual_nullable is not nullable:
                raise SystemExit(f"{name}.{field_name} nullability mismatch")
            actual_primary = field.get("is_primary", False)
            if not isinstance(actual_primary, bool) or actual_primary is not primary:
                raise SystemExit(f"{name}.{field_name} primary-key mismatch")
        indexes: list[dict[str, Any]] = []
        for index_name in client.list_indexes(collection_name=name):
            indexes.extend(index_records(client.describe_index(collection_name=name, index_name=index_name)))
        vector_indexes = [index for index in indexes if index.get("field_name") == "vector"]
        if len(vector_indexes) != 1:
            raise SystemExit(f"{name} must have exactly one vector index")
        params = vector_indexes[0].get("params") or {}
        index_type = str(vector_indexes[0].get("index_type", params.get("index_type", ""))).upper()
        metric_type = str(vector_indexes[0].get("metric_type", params.get("metric_type", ""))).upper()
        if index_type != "AUTOINDEX" or metric_type != "COSINE":
            raise SystemExit(f"{name} vector index mismatch: {index_type}/{metric_type}")
        client.load_collection(collection_name=name)
        client.query(collection_name=name, filter="", output_fields=["id"], limit=1)
finally:
    client.close()
PY
