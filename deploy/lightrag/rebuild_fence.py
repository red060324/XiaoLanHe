#!/usr/bin/env python3
"""Strict deployment-owned fence for the pinned LightRAG Milvus projection."""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import fcntl
import hashlib
import json
import os
import pathlib
import re
import signal
import stat
import tarfile
import tempfile
import time
from datetime import datetime, timezone
from typing import Any, Iterator

SCHEMA_VERSION = 2
MAX_FENCE_BYTES = 2 * 1024 * 1024
MAX_ARCHIVE_MEMBERS = 1_000_000
FIXTURE_TERMINATION_GRACE_SECONDS = 5
TARGETS = ("entities", "relationships", "chunks")
TRANSITION_COUNT_FIELDS = (
    "source_total",
    "prepared",
    "rebuilt",
    "skipped",
    "duplicates",
    "batches",
    "failed_batches",
)
TRANSITION_OUTCOMES = frozenset({"ok", "error", "recovered", "idempotent"})
REPORT_ERROR_CODES = frozenset(
    {
        "abandoned_rebuilding",
        "context_validation",
        "controller_lock_busy",
        "fixture_cleanup_failed",
        "fixture_failed",
        "fixture_spawn_failed",
        "fixture_timeout",
        "io_failed",
        "operation_cancelled",
        "serving_lock_busy",
        "unknown",
        "validation_failed",
    }
)
OPERATIONS = {
    "rebuild": "migration_rebuild",
    "bootstrap": "bootstrap_empty",
    "prepare-restore": "restore_verify",
    "restore-verify": "restore_verify",
    "revalidate": "revalidate",
    "invalidate": "revalidate",
}
ALLOWED_OPERATIONS = frozenset(OPERATIONS.values())
SERIALIZED_STATES = {"stale", "rebuilding", "failed", "verified"}
ALLOWED_TRANSITIONS = {
    ("absent", "rebuilding"),
    ("stale", "rebuilding"),
    ("failed", "rebuilding"),
    ("verified", "stale"),
    ("rebuilding", "verified"),
    ("rebuilding", "failed"),
    ("stale", "verified"),
    # Restore publishes stale even when its separately persisted fence was not
    # restored. Verification-only failure must also replace stale.
    ("absent", "stale"),
    ("stale", "failed"),
}
REASON_CODES = {
    "stale": {
        "restore_pending_verification",
        "contract_changed",
        "generation_changed",
        "legacy_manifest_changed",
        "invalidated",
    },
    "rebuilding": {"in_progress"},
    "failed": {"operation_failed", "abandoned_rebuilding"},
    "verified": {"verified"},
}
REQUIRED_CHECKS = (
    "source_unchanged",
    "stats_valid",
    "schema_valid",
    "exact_id_sets_valid",
    "graph_consistency_valid",
    "fixtures_valid",
    "legacy_import_valid",
    "cleanup_valid",
)
MARKER_KEYS = {
    "schema_version", "state", "generation", "attempt_id", "operation",
    "contract_sha256", "report_path", "report_sha256", "updated_at", "reason_code",
}
REPORT_KEYS = {
    "schema_version", "attempt_id", "generation", "operation", "state",
    "reason_code", "started_at", "finished_at", "software", "contract",
    "writer_fence", "backup", "source", "rebuild", "targets",
    "duplicate_policy", "official_consistency", "legacy_import", "checks",
    "fixtures", "cleanup", "errors",
}
ATTEMPT_CONTEXT_KEYS = {
    "schema_version", "attempt_id", "generation", "operation",
    "contract_sha256", "started_at", "software", "contract",
}
EXPECTED_COLLECTIONS = {
    "entities": "xiaolanhe_v1_entities_text_embedding_v4_1024d",
    "relationships": "xiaolanhe_v1_relationships_text_embedding_v4_1024d",
    "chunks": "xiaolanhe_v1_chunks_text_embedding_v4_1024d",
}
EXPECTED_FIELD_SCHEMAS = {
    "entities": {
        "id": ("VARCHAR", 64, False, True),
        "vector": ("FLOAT_VECTOR", None, False, False),
        "created_at": ("INT64", None, False, False),
        "entity_name": ("VARCHAR", 512, True, False),
        "content": ("VARCHAR", 65535, True, False),
        "source_id": ("VARCHAR", 65535, True, False),
        "file_path": ("VARCHAR", 32768, True, False),
    },
    "relationships": {
        "id": ("VARCHAR", 64, False, True),
        "vector": ("FLOAT_VECTOR", None, False, False),
        "created_at": ("INT64", None, False, False),
        "src_id": ("VARCHAR", 512, True, False),
        "tgt_id": ("VARCHAR", 512, True, False),
        "content": ("VARCHAR", 65535, True, False),
        "source_id": ("VARCHAR", 65535, True, False),
        "file_path": ("VARCHAR", 32768, True, False),
    },
    "chunks": {
        "id": ("VARCHAR", 64, False, True),
        "vector": ("FLOAT_VECTOR", None, False, False),
        "created_at": ("INT64", None, False, False),
        "full_doc_id": ("VARCHAR", 64, True, False),
        "content": ("VARCHAR", 65535, True, False),
        "file_path": ("VARCHAR", 32768, True, False),
    },
}
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
RFC3339_UTC = re.compile(
    r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z$"
)


class ClassifiedFailure(Exception):
    """A private-detail-free failure suitable for durable reports and events."""

    def __init__(self, code: str, *, retryable: bool = False):
        if code not in REPORT_ERROR_CODES:
            raise ValueError("invalid classified failure code")
        super().__init__(code)
        self.code = code
        self.retryable = retryable


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def canonical_bytes(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")


def digest(value: Any) -> str:
    return "sha256:" + hashlib.sha256(canonical_bytes(value)).hexdigest()


def _elapsed_ms(started_ns: int) -> int:
    return max(0, (time.monotonic_ns() - started_ns) // 1_000_000)


def _transition_targets(report: dict[str, Any] | None = None) -> dict[str, dict[str, int]]:
    targets = {
        name: {field: 0 for field in TRANSITION_COUNT_FIELDS}
        for name in TARGETS
    }
    if report is None or report["rebuild"]["applicable"] is not True:
        return targets
    for name in TARGETS:
        targets[name] = {
            field: require_nonnegative_int(
                report["rebuild"][name][field], f"transition {name}.{field}"
            )
            for field in TRANSITION_COUNT_FIELDS
        }
    return targets


def transition_event(
    transition: dict[str, Any],
    outcome: str,
    duration_ms: int,
    report: dict[str, Any] | None = None,
) -> dict[str, Any]:
    validate_marker(transition)
    if outcome not in TRANSITION_OUTCOMES:
        raise ValueError("invalid transition outcome")
    require_nonnegative_int(duration_ms, "transition duration")
    if report is not None:
        validate_report(report, verified=report["state"] == "verified")
        if (
            report["state"] != transition["state"]
            or report["operation"] != transition["operation"]
            or report["reason_code"] != transition["reason_code"]
        ):
            raise ValueError("transition report classification mismatch")
    return {
        "schema_version": 1,
        "event": "lightrag.fence.transition",
        "operation": transition["operation"],
        "state": transition["state"],
        "reason_code": transition["reason_code"],
        "outcome": outcome,
        "duration_ms": duration_ms,
        "targets": _transition_targets(report),
    }


def emit_transition(
    transition: dict[str, Any],
    outcome: str,
    duration_ms: int,
    report: dict[str, Any] | None = None,
    *,
    args: argparse.Namespace | None = None,
) -> None:
    event = transition_event(transition, outcome, duration_ms, report)
    print(canonical_bytes(event).decode("utf-8"), flush=True)


def expected_collection_schema(name: str) -> dict[str, Any]:
    fields = []
    for field_name in sorted(EXPECTED_FIELD_SCHEMAS[name]):
        field_type, max_length, nullable, primary = EXPECTED_FIELD_SCHEMAS[name][field_name]
        fields.append({
            "name": field_name,
            "type": field_type,
            "max_length": max_length,
            "nullable": nullable,
            "primary": primary,
            "dimension": 1024 if field_name == "vector" else 0,
        })
    return {
        "database": "lightrag",
        "collection": EXPECTED_COLLECTIONS[name],
        "enable_dynamic_field": True,
        "fields": fields,
    }


def expected_schema_sha256(name: str) -> str:
    return digest(expected_collection_schema(name))


def require_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise ValueError(f"{name} is required")
    return value


def require_exact(value: Any, keys: set[str], label: str) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != keys:
        raise ValueError(f"{label} has missing or unknown keys")
    return value


def require_digest(value: Any, label: str, *, nullable: bool = False) -> None:
    if nullable and value is None:
        return
    if not isinstance(value, str) or not DIGEST.fullmatch(value):
        raise ValueError(f"invalid {label}")


def require_identifier(value: Any, label: str) -> str:
    if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
        raise ValueError(f"invalid {label}")
    return value


def require_bool(value: Any, label: str) -> bool:
    if not isinstance(value, bool):
        raise ValueError(f"invalid {label}")
    return value


def require_nonnegative_int(value: Any, label: str) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise ValueError(f"invalid {label}")
    return value


def require_optional_identifier(value: Any, label: str) -> str | None:
    if value is None:
        return None
    return require_identifier(value, label)


def require_optional_string(value: Any, label: str) -> str | None:
    if value is not None and not isinstance(value, str):
        raise ValueError(f"invalid {label}")
    return value


def require_optional_bool(value: Any, label: str) -> bool | None:
    if value is not None and not isinstance(value, bool):
        raise ValueError(f"invalid {label}")
    return value


def require_optional_digest(value: Any, label: str) -> str | None:
    require_digest(value, label, nullable=True)
    return value


def parse_time(value: Any, label: str) -> datetime:
    if not isinstance(value, str) or not RFC3339_UTC.fullmatch(value):
        raise ValueError(f"invalid {label}")
    try:
        parsed = datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError as error:
        raise ValueError(f"invalid {label}") from error
    if parsed.tzinfo != timezone.utc:
        raise ValueError(f"invalid {label}")
    return parsed


def read_regular(path: pathlib.Path, maximum: int = MAX_FENCE_BYTES) -> bytes:
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0 or info.st_size > maximum:
            raise ValueError("file is empty, oversized or not regular")
        chunks: list[bytes] = []
        remaining = maximum + 1
        while remaining:
            chunk = os.read(fd, min(65536, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        data = b"".join(chunks)
        if len(data) > maximum:
            raise ValueError("file is oversized")
        return data
    finally:
        os.close(fd)


def read_canonical_json(path: pathlib.Path, label: str) -> dict[str, Any]:
    data = read_regular(path)
    try:
        value = json.loads(data)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"invalid {label} JSON") from error
    if not isinstance(value, dict) or data != canonical_bytes(value) + b"\n":
        raise ValueError(f"{label} JSON is not canonical")
    return value


def file_digest(path: pathlib.Path) -> str:
    result = hashlib.sha256()
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError("evidence path is not a regular file")
        while True:
            block = os.read(fd, 1024 * 1024)
            if not block:
                break
            result.update(block)
    finally:
        os.close(fd)
    return "sha256:" + result.hexdigest()


def build_contract() -> dict[str, Any]:
    contract = {
        "schema_version": SCHEMA_VERSION,
        "lightrag_version": "1.5.7",
        "lightrag_commit": "28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c",
        "lightrag_image_digest": require_env("XLH_LIGHTRAG_IMAGE_DIGEST"),
        "milvus_version": "2.6.11",
        "milvus_database": os.environ.get("MILVUS_DB_NAME", ""),
        "workspace": os.environ.get("WORKSPACE", ""),
        "working_directory_identity": require_env("XLH_LIGHTRAG_WORKING_DIR_ID"),
        "storage": {
            "kv": os.environ.get("LIGHTRAG_KV_STORAGE", ""),
            "vector": os.environ.get("LIGHTRAG_VECTOR_STORAGE", ""),
            "graph": os.environ.get("LIGHTRAG_GRAPH_STORAGE", ""),
            "doc_status": os.environ.get("LIGHTRAG_DOC_STATUS_STORAGE", ""),
        },
        "embedding": {
            "binding": os.environ.get("EMBEDDING_BINDING", ""),
            "endpoint_profile": require_env("XLH_EMBEDDING_ENDPOINT_PROFILE"),
            "model": os.environ.get("EMBEDDING_MODEL", ""),
            "dimension": int(os.environ.get("EMBEDDING_DIM", "0")),
            "send_dimension": os.environ.get("EMBEDDING_SEND_DIM", "").lower(),
            "asymmetric": os.environ.get("EMBEDDING_ASYMMETRIC", "").lower(),
            "document_prefix": os.environ.get("EMBEDDING_DOCUMENT_PREFIX") or None,
            "query_prefix": os.environ.get("EMBEDDING_QUERY_PREFIX") or None,
        },
        "index_type": os.environ.get("MILVUS_INDEX_TYPE", ""),
        "metric_type": os.environ.get("MILVUS_METRIC_TYPE", ""),
        "id_canonicalization_version": "lightrag-v1.5.7",
        "fixture_suite_version": require_env("XLH_LIGHTRAG_FIXTURE_SUITE_VERSION"),
        "legacy_manifest_sha256": os.environ.get("XLH_LEGACY_MANIFEST_SHA256") or None,
    }
    if contract["milvus_database"] != "lightrag" or contract["workspace"] != "xiaolanhe_v1":
        raise ValueError("invalid Milvus database or workspace contract")
    if contract["index_type"] != "AUTOINDEX" or contract["metric_type"] != "COSINE":
        raise ValueError("invalid Milvus index contract")
    if contract["storage"] != {"kv": "JsonKVStorage", "vector": "MilvusVectorDBStorage", "graph": "NetworkXStorage", "doc_status": "JsonDocStatusStorage"}:
        raise ValueError("invalid four-store contract")
    embedding = contract["embedding"]
    if embedding["model"] != "text-embedding-v4" or embedding["dimension"] != 1024 or embedding["send_dimension"] != "false" or embedding["asymmetric"] != "false" or embedding["document_prefix"] is not None or embedding["query_prefix"] is not None:
        raise ValueError("invalid embedding contract")
    require_digest(contract["lightrag_image_digest"], "LightRAG image digest")
    require_digest(contract["legacy_manifest_sha256"], "legacy manifest digest", nullable=True)
    require_identifier(contract["working_directory_identity"], "working directory identity")
    require_identifier(embedding["binding"], "embedding provider binding")
    require_identifier(embedding["endpoint_profile"], "embedding endpoint profile")
    require_identifier(contract["fixture_suite_version"], "fixture suite version")
    return contract


def report_contract(contract: dict[str, Any], contract_hash: str | None = None) -> dict[str, Any]:
    embedding, storage = contract["embedding"], contract["storage"]
    return {
        "contract_sha256": contract_hash or digest(contract),
        "workspace": contract["workspace"],
        "working_directory_identity": contract["working_directory_identity"],
        "kv_storage": storage["kv"],
        "vector_storage": storage["vector"],
        "graph_storage": storage["graph"],
        "doc_status_storage": storage["doc_status"],
        "milvus_database": contract["milvus_database"],
        "embedding_contract": {
            "provider_binding": embedding["binding"],
            "endpoint_semantics": embedding["endpoint_profile"],
            "model": embedding["model"],
            "dimension": embedding["dimension"],
            "send_dimension": embedding["send_dimension"] == "true",
            "asymmetric": embedding["asymmetric"] == "true",
            "document_prefix": embedding["document_prefix"],
            "query_prefix": embedding["query_prefix"],
        },
        "index_type": contract["index_type"],
        "metric_type": contract["metric_type"],
        "id_canonicalization_version": contract["id_canonicalization_version"],
        "fixture_suite_version": contract["fixture_suite_version"],
        "legacy_manifest_sha256": contract["legacy_manifest_sha256"],
    }


def software_section(contract: dict[str, Any]) -> dict[str, Any]:
    return {
        "lightrag_version": contract["lightrag_version"],
        "lightrag_commit": contract["lightrag_commit"],
        "lightrag_image_digest": contract["lightrag_image_digest"],
        "milvus_version": contract["milvus_version"],
        "etcd_version": "3.5.25",
        "minio_version": "RELEASE.2025-09-07T16-13-09Z",
    }


def contract_from_report(software: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    embedding = contract["embedding_contract"]
    return {
        "schema_version": SCHEMA_VERSION,
        "lightrag_version": software["lightrag_version"],
        "lightrag_commit": software["lightrag_commit"],
        "lightrag_image_digest": software["lightrag_image_digest"],
        "milvus_version": software["milvus_version"],
        "milvus_database": contract["milvus_database"],
        "workspace": contract["workspace"],
        "working_directory_identity": contract["working_directory_identity"],
        "storage": {
            "kv": contract["kv_storage"],
            "vector": contract["vector_storage"],
            "graph": contract["graph_storage"],
            "doc_status": contract["doc_status_storage"],
        },
        "embedding": {
            "binding": embedding["provider_binding"],
            "endpoint_profile": embedding["endpoint_semantics"],
            "model": embedding["model"],
            "dimension": embedding["dimension"],
            "send_dimension": str(embedding["send_dimension"]).lower(),
            "asymmetric": str(embedding["asymmetric"]).lower(),
            "document_prefix": embedding["document_prefix"],
            "query_prefix": embedding["query_prefix"],
        },
        "index_type": contract["index_type"],
        "metric_type": contract["metric_type"],
        "id_canonicalization_version": contract["id_canonicalization_version"],
        "fixture_suite_version": contract["fixture_suite_version"],
        "legacy_manifest_sha256": contract["legacy_manifest_sha256"],
    }


def validate_marker(value: Any) -> dict[str, Any]:
    value = require_exact(value, MARKER_KEYS, "fence marker")
    if value["schema_version"] != SCHEMA_VERSION or value["state"] not in SERIALIZED_STATES or value["operation"] not in ALLOWED_OPERATIONS:
        raise ValueError("invalid fence marker version, state or operation")
    require_identifier(value["attempt_id"], "marker attempt ID")
    require_identifier(value["generation"], "marker generation")
    require_digest(value["contract_sha256"], "marker contract digest")
    parse_time(value["updated_at"], "marker timestamp")
    if value["reason_code"] not in REASON_CODES[value["state"]]:
        raise ValueError("invalid marker reason code")
    terminal = value["state"] in {"verified", "failed"}
    if terminal:
        relative = pathlib.PurePosixPath(str(value["report_path"]))
        if relative.is_absolute() or relative.parent != pathlib.PurePosixPath("reports") or relative.name != value["attempt_id"] + ".json":
            raise ValueError("invalid rebuild report path")
        require_digest(value["report_sha256"], "marker report digest")
    elif value["report_path"] is not None or value["report_sha256"] is not None:
        raise ValueError("nonterminal marker cannot reference a report")
    return value


def marker(state: str, generation: str, attempt: str, operation: str, contract_hash: str, reason: str, report_path: str | None = None, report_hash: str | None = None) -> dict[str, Any]:
    return validate_marker({
        "schema_version": SCHEMA_VERSION, "state": state, "generation": generation,
        "attempt_id": attempt, "operation": operation, "contract_sha256": contract_hash,
        "report_path": report_path, "report_sha256": report_hash,
        "updated_at": utc_now(), "reason_code": reason,
    })


def assert_transition(previous: str, following: str) -> None:
    if (previous, following) not in ALLOWED_TRANSITIONS:
        raise ValueError(f"illegal fence transition {previous}->{following}")


class FenceStore:
    def __init__(self, root: pathlib.Path, *, create: bool = True):
        self.root = root.resolve()
        self.reports = self.root / "reports"
        self.attempts = self.root / "attempts"
        self.current = self.root / "current.json"
        self.lock_path = self.root / "rebuild.lock"
        self.serving_lock_path = self.root / "serving.lock"
        if create:
            self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
            self.reports.mkdir(mode=0o700, exist_ok=True)
            self.attempts.mkdir(mode=0o700, exist_ok=True)
            descriptor = os.open(
                self.serving_lock_path,
                os.O_RDWR
                | os.O_CREAT
                | getattr(os, "O_CLOEXEC", 0)
                | getattr(os, "O_NONBLOCK", 0)
                | getattr(os, "O_NOFOLLOW", 0),
                0o600,
            )
            try:
                if not stat.S_ISREG(os.fstat(descriptor).st_mode):
                    raise ValueError("serving lock is not a regular file")
                os.fchmod(descriptor, 0o600)
            finally:
                os.close(descriptor)
        self._lock: Any = None

    def __enter__(self) -> "FenceStore":
        self._lock = self.lock_path.open("a+", encoding="utf-8")
        try:
            fcntl.flock(self._lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self._lock.close()
            self._lock = None
            raise ClassifiedFailure("controller_lock_busy", retryable=True) from None
        return self

    def __exit__(self, *_: Any) -> None:
        if self._lock:
            fcntl.flock(self._lock.fileno(), fcntl.LOCK_UN)
            self._lock.close()
            self._lock = None

    @contextlib.contextmanager
    def serving_lease(self, *, exclusive: bool) -> Iterator[int]:
        """Serialize controller transitions against a live steady writer.

        Controllers take this lock exclusively before ``rebuild.lock``.  The
        steady-state supervisor takes it shared before fence verification and
        retains it until the complete server process group has stopped.
        """

        flags = os.O_RDONLY
        if exclusive:
            flags = os.O_RDWR | os.O_CREAT
        flags |= (
            getattr(os, "O_CLOEXEC", 0)
            | getattr(os, "O_NONBLOCK", 0)
            | getattr(os, "O_NOFOLLOW", 0)
        )
        descriptor = os.open(self.serving_lock_path, flags, 0o600)
        try:
            os.set_inheritable(descriptor, False)
            if not stat.S_ISREG(os.fstat(descriptor).st_mode):
                raise ValueError("serving lock is not a regular file")
            if exclusive:
                os.fchmod(descriptor, 0o600)
            operation = fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH
            try:
                fcntl.flock(descriptor, operation | fcntl.LOCK_NB)
            except BlockingIOError:
                raise ClassifiedFailure("serving_lock_busy", retryable=True) from None
            try:
                yield descriptor
            finally:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
        finally:
            os.close(descriptor)

    def _atomic_write(self, path: pathlib.Path, value: dict[str, Any], *, exclusive: bool = False) -> None:
        data = canonical_bytes(value) + b"\n"
        fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
        try:
            os.fchmod(fd, 0o600)
            with os.fdopen(fd, "wb", closefd=True) as output:
                output.write(data)
                output.flush()
                os.fsync(output.fileno())
            if exclusive:
                os.link(temporary, path)
                os.unlink(temporary)
            else:
                os.replace(temporary, path)
            directory = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            try:
                os.unlink(temporary)
            except FileNotFoundError:
                pass

    def publish(self, value: dict[str, Any]) -> None:
        self._atomic_write(self.current, validate_marker(value))

    def report(self, value: dict[str, Any]) -> tuple[str, str]:
        validate_report(value)
        relative = f"reports/{value['attempt_id']}.json"
        self._atomic_write(self.root / relative, value, exclusive=True)
        return relative, digest(value)

    def attempt(self, value: dict[str, Any]) -> str:
        value = validate_attempt_context(value)
        relative = f"attempts/{value['attempt_id']}.json"
        self._atomic_write(self.root / relative, value, exclusive=True)
        return relative

    def read_attempt(self, attempt_id: str) -> dict[str, Any]:
        require_identifier(attempt_id, "attempt context ID")
        return validate_attempt_context(
            read_canonical_json(self.attempts / f"{attempt_id}.json", "attempt context")
        )

    def read_current(self) -> dict[str, Any]:
        return validate_marker(read_canonical_json(self.current, "current marker"))

    def current_or_absent(self) -> tuple[str, dict[str, Any] | None]:
        try:
            current = self.read_current()
            return current["state"], current
        except FileNotFoundError:
            return "absent", None

    def verify_current(self, generation: str, contract_hash: str) -> None:
        require_identifier(generation, "required generation")
        require_digest(contract_hash, "required contract digest")
        current = self.read_current()
        if current["state"] != "verified" or current["generation"] != generation or current["contract_sha256"] != contract_hash:
            raise ValueError("rebuild fence is not verified for this deployment contract")
        relative = pathlib.PurePosixPath(current["report_path"])
        report = read_canonical_json(self.root / pathlib.Path(*relative.parts), "rebuild report")
        if digest(report) != current["report_sha256"]:
            raise ValueError("rebuild report digest mismatch")
        validate_report(report, verified=True)
        contract = report["contract"]
        if report["state"] != current["state"] or report["reason_code"] != current["reason_code"] or report["generation"] != current["generation"] or report["attempt_id"] != current["attempt_id"] or report["operation"] != current["operation"] or contract["contract_sha256"] != current["contract_sha256"]:
            raise ValueError("rebuild report identity mismatch")
        if parse_time(current["updated_at"], "marker timestamp") < parse_time(report["finished_at"], "report finish timestamp"):
            raise ValueError("marker predates terminal report")


def validate_bounded_error(value: Any, label: str = "bounded error") -> None:
    value = require_exact(value, {"stage", "target", "code", "retryable"}, label)
    for key in ("stage", "target"):
        if not isinstance(value[key], str) or not 0 < len(value[key]) <= 64 or any(ord(character) < 0x20 or ord(character) == 0x7F for character in value[key]):
            raise ValueError(f"invalid {label} {key}")
    if value["code"] not in REPORT_ERROR_CODES:
        raise ValueError(f"invalid {label} code")
    require_bool(value["retryable"], f"{label} retryable")


def validate_stats_shape(name: str, value: Any) -> dict[str, Any]:
    keys = {"label", "source_total", "prepared", "rebuilt", "staged", "skipped", "duplicates", "batches", "failed_batches", "errors"}
    value = require_exact(value, keys, f"{name} rebuild result")
    if value["label"] != name:
        raise ValueError(f"invalid {name} rebuild label")
    for field in keys - {"label", "errors"}:
        require_nonnegative_int(value[field], f"{name}.{field}")
    if not isinstance(value["errors"], list):
        raise ValueError(f"invalid {name}.errors")
    for error in value["errors"]:
        validate_bounded_error(error, f"{name} rebuild error")
    return value


def validate_stats(name: str, value: Any, duplicate_maximum: int = 0) -> None:
    value = validate_stats_shape(name, value)
    if not isinstance(value["errors"], list) or value["errors"] or value["failed_batches"] != 0 or value["staged"] != 0:
        raise ValueError(f"{name} rebuild reported failure")
    if value["rebuilt"] != value["prepared"] or value["prepared"] + value["skipped"] + value["duplicates"] != value["source_total"]:
        raise ValueError(f"{name} rebuild counts are inconsistent")
    if value["skipped"] != 0 or value["duplicates"] > duplicate_maximum:
        raise ValueError(f"{name} rebuild violates skip/duplicate policy")


def empty_stats(name: str) -> dict[str, Any]:
    return {"label": name, "source_total": 0, "prepared": 0, "rebuilt": 0, "staged": 0, "skipped": 0, "duplicates": 0, "batches": 0, "failed_batches": 0, "errors": []}


def empty_target() -> dict[str, Any]:
    return {"collection": None, "database": None, "dynamic_fields": None, "schema_sha256": None, "vector_field_type": None, "vector_dimension": 0, "primary_key_field": None, "index_type": None, "metric_type": None, "expected_count": 0, "actual_count": 0, "expected_ids_sha256": None, "actual_ids_sha256": None}


def empty_report(attempt: str, generation: str, operation: str, contract: dict[str, Any], contract_hash: str, started: str, *, state: str = "failed", reason: str = "operation_failed") -> dict[str, Any]:
    backup_applicable = operation in {"migration_rebuild", "restore_verify"}
    return {
        "schema_version": SCHEMA_VERSION, "attempt_id": attempt, "generation": generation,
        "operation": operation, "state": state, "reason_code": reason,
        "started_at": started, "finished_at": utc_now(),
        "software": software_section(contract), "contract": report_contract(contract, contract_hash),
        "writer_fence": {"applicable": True, "status": "not_run", "evidence_type": "xlh.lightrag_writer_fence.v2", "evidence_sha256": None, "issued_at": None, "expires_at": None, "pipeline_idle_observed": False, "server_replicas": 0, "automatic_restart_disabled": False, "observer": None, "uncontrolled_writers_attestation": None, "backup_manifest_sha256": None},
        "backup": {"applicable": backup_applicable, "status": "not_run" if backup_applicable else "not_applicable", "evidence_type": "xlh.lightrag_backup.v2", "evidence_sha256": None, "manifest_sha256": None, "source_generation": None, "configuration_sha256": None, "writer_evidence_sha256": None, "component_count": 0},
        "source": {"applicable": True, "status": "not_run", "before_sha256": None, "after_sha256": None, "graph_nodes": 0, "graph_edges_raw": 0, "relationships_normalized": 0, "text_chunks": 0},
        "rebuild": {"applicable": operation == "migration_rebuild", "status": "not_run" if operation == "migration_rebuild" else "not_applicable", **{name: empty_stats(name) for name in TARGETS}},
        "targets": {"applicable": True, "status": "not_run", **{name: empty_target() for name in TARGETS}},
        "duplicate_policy": {"applicable": False, "status": "not_applicable", "evidence_sha256": None, "reviewer": None, "expires_at": None, "max_duplicates": {name: 0 for name in TARGETS}},
        "official_consistency": {"applicable": True, "status": "not_run", "graph_entities": 0, "graph_relations": 0, "missing_entities": 0, "missing_relations": 0, "skipped_nodes": 0, "skipped_edges": 0},
        "legacy_import": {"applicable": False, "status": "not_applicable", "required": False, "manifest_sha256": None, "continuous_success_watermark": 0, "source_rows": 0, "accepted_rows": 0, "terminal_rows": 0, "reconciled": False, "retirement_eligible": False},
        "checks": {name: False for name in REQUIRED_CHECKS},
        "fixtures": {"applicable": operation != "bootstrap_empty", "status": "not_run" if operation != "bootstrap_empty" else "not_applicable", "suite_version": None, "passed": 0, "failed": 0, "results_sha256": None},
        "cleanup": {"applicable": True, "status": "not_run", "worker_finalized": False, "verifier_finalized": False, "report_reread": False},
        "errors": [],
    }


def _section(value: Any, keys: set[str], label: str) -> dict[str, Any]:
    result = require_exact(value, keys | {"applicable", "status"}, label)
    if not isinstance(result["applicable"], bool) or result["status"] not in {"succeeded", "failed", "not_run", "not_applicable"}:
        raise ValueError(f"invalid {label} applicability/status")
    if result["applicable"] == (result["status"] == "not_applicable"):
        raise ValueError(f"inconsistent {label} applicability/status")
    return result


def validate_report(value: Any, *, verified: bool = False) -> dict[str, Any]:
    report = require_exact(value, REPORT_KEYS, "fence report")
    if report["schema_version"] != SCHEMA_VERSION or report["state"] not in {"verified", "failed"} or report["operation"] not in ALLOWED_OPERATIONS:
        raise ValueError("invalid report version, state or operation")
    require_identifier(report["attempt_id"], "report attempt ID")
    require_identifier(report["generation"], "report generation")
    started = parse_time(report["started_at"], "report start timestamp")
    finished = parse_time(report["finished_at"], "report finish timestamp")
    if finished < started or report["reason_code"] not in REASON_CODES[report["state"]]:
        raise ValueError("invalid report timestamp or reason")
    software = require_exact(report["software"], {"lightrag_version", "lightrag_commit", "lightrag_image_digest", "milvus_version", "etcd_version", "minio_version"}, "software")
    if software["lightrag_version"] != "1.5.7" or software["lightrag_commit"] != "28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c" or software["milvus_version"] != "2.6.11" or software["etcd_version"] != "3.5.25" or software["minio_version"] != "RELEASE.2025-09-07T16-13-09Z":
        raise ValueError("unexpected report software identity")
    require_digest(software["lightrag_image_digest"], "report image digest")
    contract = require_exact(report["contract"], {"contract_sha256", "workspace", "working_directory_identity", "kv_storage", "vector_storage", "graph_storage", "doc_status_storage", "milvus_database", "embedding_contract", "index_type", "metric_type", "id_canonicalization_version", "fixture_suite_version", "legacy_manifest_sha256"}, "report contract")
    require_digest(contract["contract_sha256"], "report contract digest")
    require_digest(contract["legacy_manifest_sha256"], "report legacy digest", nullable=True)
    embedding = require_exact(contract["embedding_contract"], {"provider_binding", "endpoint_semantics", "model", "dimension", "send_dimension", "asymmetric", "document_prefix", "query_prefix"}, "embedding contract")
    if contract["workspace"] != "xiaolanhe_v1" or contract["milvus_database"] != "lightrag" or contract["kv_storage"] != "JsonKVStorage" or contract["vector_storage"] != "MilvusVectorDBStorage" or contract["graph_storage"] != "NetworkXStorage" or contract["doc_status_storage"] != "JsonDocStatusStorage" or contract["index_type"] != "AUTOINDEX" or contract["metric_type"] != "COSINE" or contract["id_canonicalization_version"] != "lightrag-v1.5.7":
        raise ValueError("unexpected report deployment contract")
    if embedding["model"] != "text-embedding-v4" or embedding["dimension"] != 1024 or embedding["send_dimension"] is not False or embedding["asymmetric"] is not False or embedding["document_prefix"] is not None or embedding["query_prefix"] is not None:
        raise ValueError("unexpected report embedding contract")
    require_identifier(contract["working_directory_identity"], "report working directory identity")
    require_identifier(contract["fixture_suite_version"], "report fixture suite version")
    require_identifier(embedding["provider_binding"], "report embedding provider binding")
    require_identifier(embedding["endpoint_semantics"], "report embedding endpoint semantics")
    if digest(contract_from_report(software, contract)) != contract["contract_sha256"]:
        raise ValueError("report contract digest mismatch")
    writer = _section(report["writer_fence"], {"evidence_type", "evidence_sha256", "issued_at", "expires_at", "pipeline_idle_observed", "server_replicas", "automatic_restart_disabled", "observer", "uncontrolled_writers_attestation", "backup_manifest_sha256"}, "writer fence")
    backup = _section(report["backup"], {"evidence_type", "evidence_sha256", "manifest_sha256", "source_generation", "configuration_sha256", "writer_evidence_sha256", "component_count"}, "backup")
    source = _section(report["source"], {"before_sha256", "after_sha256", "graph_nodes", "graph_edges_raw", "relationships_normalized", "text_chunks"}, "source")
    rebuild = _section(report["rebuild"], set(TARGETS), "rebuild")
    targets = _section(report["targets"], set(TARGETS), "targets")
    duplicate = _section(report["duplicate_policy"], {"evidence_sha256", "reviewer", "expires_at", "max_duplicates"}, "duplicate policy")
    official = _section(report["official_consistency"], {"graph_entities", "graph_relations", "missing_entities", "missing_relations", "skipped_nodes", "skipped_edges"}, "official consistency")
    legacy = _section(report["legacy_import"], {"required", "manifest_sha256", "continuous_success_watermark", "source_rows", "accepted_rows", "terminal_rows", "reconciled", "retirement_eligible"}, "legacy import")
    fixtures = _section(report["fixtures"], {"suite_version", "passed", "failed", "results_sha256"}, "fixtures")
    cleanup = _section(report["cleanup"], {"worker_finalized", "verifier_finalized", "report_reread"}, "cleanup")
    require_exact(report["checks"], set(REQUIRED_CHECKS), "checks")
    require_exact(duplicate["max_duplicates"], set(TARGETS), "duplicate maxima")
    if any(not isinstance(value, bool) for value in report["checks"].values()):
        raise ValueError("check values must be booleans")
    if writer["evidence_type"] != "xlh.lightrag_writer_fence.v2":
        raise ValueError("invalid writer evidence type")
    if backup["evidence_type"] != "xlh.lightrag_backup.v2":
        raise ValueError("invalid backup evidence type")
    for field in ("pipeline_idle_observed", "automatic_restart_disabled"):
        require_bool(writer[field], f"writer fence {field}")
    require_nonnegative_int(writer["server_replicas"], "writer fence server replicas")
    require_optional_identifier(writer["observer"], "writer fence observer")
    require_optional_string(writer["uncontrolled_writers_attestation"], "writer attestation")
    for key in ("evidence_sha256", "backup_manifest_sha256"):
        require_optional_digest(writer[key], f"writer fence {key}")
    if writer["issued_at"] is not None:
        parse_time(writer["issued_at"], "writer issue timestamp")
    if writer["expires_at"] is not None:
        parse_time(writer["expires_at"], "writer expiry timestamp")
    require_optional_identifier(backup["source_generation"], "backup source generation")
    require_nonnegative_int(backup["component_count"], "backup component count")
    for key in ("evidence_sha256", "manifest_sha256", "configuration_sha256", "writer_evidence_sha256"):
        require_optional_digest(backup[key], f"backup {key}")
    for key in ("graph_nodes", "graph_edges_raw", "relationships_normalized", "text_chunks"):
        require_nonnegative_int(source[key], f"source {key}")
    for key in ("before_sha256", "after_sha256"):
        require_optional_digest(source[key], f"source {key}")
    if source["relationships_normalized"] > source["graph_edges_raw"]:
        raise ValueError("normalized relationships exceed raw graph edges")
    for key in ("graph_entities", "graph_relations", "missing_entities", "missing_relations", "skipped_nodes", "skipped_edges"):
        require_nonnegative_int(official[key], f"official consistency {key}")
    require_bool(legacy["required"], "legacy required")
    require_bool(legacy["reconciled"], "legacy reconciled")
    require_bool(legacy["retirement_eligible"], "legacy retirement eligibility")
    for key in ("continuous_success_watermark", "source_rows", "accepted_rows", "terminal_rows"):
        require_nonnegative_int(legacy[key], f"legacy {key}")
    require_optional_digest(legacy["manifest_sha256"], "legacy manifest digest")
    for key in ("entities", "relationships", "chunks"):
        require_nonnegative_int(duplicate["max_duplicates"][key], f"duplicate maximum {key}")
    require_optional_digest(duplicate["evidence_sha256"], "duplicate policy digest")
    require_optional_identifier(duplicate["reviewer"], "duplicate policy reviewer")
    if duplicate["expires_at"] is not None:
        parse_time(duplicate["expires_at"], "duplicate policy expiry")
    for key in ("passed", "failed"):
        require_nonnegative_int(fixtures[key], f"fixtures {key}")
    require_optional_identifier(fixtures["suite_version"], "fixture suite version")
    require_optional_digest(fixtures["results_sha256"], "fixture result digest")
    for key in ("worker_finalized", "verifier_finalized", "report_reread"):
        require_bool(cleanup[key], f"cleanup {key}")
    stat_keys = {"label", "source_total", "prepared", "rebuilt", "staged", "skipped", "duplicates", "batches", "failed_batches", "errors"}
    target_keys = {"collection", "database", "dynamic_fields", "schema_sha256", "vector_field_type", "vector_dimension", "primary_key_field", "index_type", "metric_type", "expected_count", "actual_count", "expected_ids_sha256", "actual_ids_sha256"}
    for name in TARGETS:
        require_exact(rebuild[name], stat_keys, f"{name} rebuild result")
        require_exact(targets[name], target_keys, f"{name} target")
        validate_stats_shape(name, rebuild[name])
        target = targets[name]
        require_optional_bool(target["dynamic_fields"], f"{name} dynamic fields")
        for key in ("vector_dimension", "expected_count", "actual_count"):
            require_nonnegative_int(target[key], f"{name} {key}")
        for key in ("collection", "database", "vector_field_type", "primary_key_field", "index_type", "metric_type"):
            require_optional_string(target[key], f"{name} {key}")
        for key in ("schema_sha256", "expected_ids_sha256", "actual_ids_sha256"):
            require_optional_digest(target[key], f"{name} {key}")
    if not isinstance(report["errors"], list):
        raise ValueError("report errors must be an array")
    for error in report["errors"]:
        validate_bounded_error(error)
    if verified or report["state"] == "verified":
        _validate_verified_report(report, writer, backup, source, rebuild, targets, duplicate, official, legacy, fixtures, cleanup)
    else:
        _validate_failed_report(report, cleanup)
    return report


def _validate_failed_report(report: dict[str, Any], cleanup: dict[str, Any]) -> None:
    if report["state"] != "failed" or not report["errors"] or any(report["checks"].values()):
        raise ValueError("failed report must contain errors and no passed checks")
    if cleanup["status"] == "succeeded" or cleanup["report_reread"] is not False:
        raise ValueError("failed report cannot claim successful terminal cleanup")
    if report["reason_code"] == "abandoned_rebuilding":
        if len(report["errors"]) != 1 or report["errors"][0] != {
            "stage": "controller",
            "target": "all",
            "code": "abandoned_rebuilding",
            "retryable": True,
        }:
            raise ValueError("abandoned report error is invalid")
        for name in (
            "writer_fence", "backup", "source", "rebuild", "targets",
            "duplicate_policy", "official_consistency", "legacy_import",
            "fixtures", "cleanup",
        ):
            if report[name]["status"] not in {"not_run", "not_applicable"}:
                raise ValueError("abandoned report cannot claim executed sections")


def attempt_context(attempt: str, generation: str, operation: str, contract: dict[str, Any], contract_hash: str, started_at: str) -> dict[str, Any]:
    return validate_attempt_context({
        "schema_version": SCHEMA_VERSION,
        "attempt_id": attempt,
        "generation": generation,
        "operation": operation,
        "contract_sha256": contract_hash,
        "started_at": started_at,
        "software": software_section(contract),
        "contract": report_contract(contract, contract_hash),
    })


def validate_attempt_context(value: Any) -> dict[str, Any]:
    value = require_exact(value, ATTEMPT_CONTEXT_KEYS, "attempt context")
    if value["schema_version"] != SCHEMA_VERSION or value["operation"] not in ALLOWED_OPERATIONS:
        raise ValueError("invalid attempt context version or operation")
    require_identifier(value["attempt_id"], "attempt context attempt ID")
    require_identifier(value["generation"], "attempt context generation")
    require_digest(value["contract_sha256"], "attempt context contract digest")
    parse_time(value["started_at"], "attempt context start timestamp")
    try:
        raw_contract = contract_from_report(value["software"], value["contract"])
        if (
            digest(raw_contract) != value["contract_sha256"]
            or value["contract"].get("contract_sha256") != value["contract_sha256"]
            or software_section(raw_contract) != value["software"]
            or report_contract(raw_contract, value["contract_sha256"]) != value["contract"]
        ):
            raise ValueError("attempt context contract binding mismatch")
        probe = empty_report(
            value["attempt_id"], value["generation"], value["operation"],
            raw_contract, value["contract_sha256"], value["started_at"],
        )
        probe["finished_at"] = value["started_at"]
        probe["software"] = value["software"]
        probe["contract"] = value["contract"]
        probe["errors"] = [{"stage": "controller", "target": "all", "code": "context_validation", "retryable": False}]
        validate_report(probe)
    except (KeyError, TypeError) as error:
        raise ValueError("invalid attempt context contract") from error
    return value


def _validate_verified_report(report: dict[str, Any], writer: dict[str, Any], backup: dict[str, Any], source: dict[str, Any], rebuild: dict[str, Any], targets: dict[str, Any], duplicate: dict[str, Any], official: dict[str, Any], legacy: dict[str, Any], fixtures: dict[str, Any], cleanup: dict[str, Any]) -> None:
    if report["state"] != "verified" or report["reason_code"] != "verified" or report["errors"] or not all(report["checks"].values()):
        raise ValueError("verified report is not clean and complete")
    if writer["status"] != "succeeded" or writer["applicable"] is not True or writer["pipeline_idle_observed"] is not True or writer["server_replicas"] != 0 or writer["automatic_restart_disabled"] is not True or writer["uncontrolled_writers_attestation"] != "no_uncontrolled_writers":
        raise ValueError("verified writer evidence is incomplete")
    require_digest(writer["evidence_sha256"], "writer evidence digest")
    issued = parse_time(writer["issued_at"], "writer issue timestamp")
    expires = parse_time(writer["expires_at"], "writer expiry timestamp")
    started = parse_time(report["started_at"], "report start timestamp")
    finished = parse_time(report["finished_at"], "report finish timestamp")
    if issued > started or expires <= finished:
        raise ValueError("verified writer evidence did not cover the attempt")
    expected_backup = report["operation"] in {"migration_rebuild", "restore_verify"}
    if backup["applicable"] != expected_backup or backup["status"] != ("succeeded" if expected_backup else "not_applicable"):
        raise ValueError("verified backup applicability is invalid")
    if expected_backup:
        for key in ("evidence_sha256", "manifest_sha256", "configuration_sha256", "writer_evidence_sha256"):
            require_digest(backup[key], f"backup {key}")
        if backup["component_count"] != 4 or writer["backup_manifest_sha256"] != backup["manifest_sha256"] or backup["configuration_sha256"] != report["contract"]["contract_sha256"]:
            raise ValueError("verified backup evidence is incomplete")
        require_identifier(backup["source_generation"], "backup source generation")
        if report["operation"] == "restore_verify" and backup["source_generation"] == report["generation"]:
            raise ValueError("restored source and target generations are equal")
    elif writer["backup_manifest_sha256"] is not None:
        raise ValueError("inapplicable backup digest is populated")
    if source["status"] != "succeeded" or source["before_sha256"] != source["after_sha256"]:
        raise ValueError("verified source evidence is incomplete")
    require_digest(source["before_sha256"], "source digest")
    expected_rebuild = report["operation"] == "migration_rebuild"
    if rebuild["applicable"] != expected_rebuild or rebuild["status"] != ("succeeded" if expected_rebuild else "not_applicable"):
        raise ValueError("verified rebuild applicability is invalid")
    if not expected_rebuild and any(rebuild[name] != empty_stats(name) for name in TARGETS):
        raise ValueError("inapplicable rebuild evidence is populated")
    if targets["status"] != "succeeded" or official["status"] != "succeeded" or any(official[key] != 0 for key in ("missing_entities", "missing_relations", "skipped_nodes", "skipped_edges")):
        raise ValueError("verified target/consistency evidence is incomplete")
    if official["graph_relations"] != source["relationships_normalized"]:
        raise ValueError("official normalized relationship count differs")
    if official["graph_entities"] > source["graph_nodes"]:
        raise ValueError("official entity count exceeds raw graph nodes")
    maxima = duplicate["max_duplicates"]
    for name in TARGETS:
        if expected_rebuild:
            validate_stats(name, rebuild[name], maxima[name])
        target = targets[name]
        if target["collection"] != EXPECTED_COLLECTIONS[name] or target["database"] != "lightrag" or target["dynamic_fields"] is not True or target["vector_field_type"] != "FLOAT_VECTOR" or target["vector_dimension"] != 1024 or target["primary_key_field"] != "id" or target["index_type"] != "AUTOINDEX" or target["metric_type"] != "COSINE" or target["expected_count"] != target["actual_count"]:
            raise ValueError(f"verified {name} target evidence is invalid")
        for key in ("schema_sha256", "expected_ids_sha256", "actual_ids_sha256"):
            require_digest(target[key], f"{name} {key}")
        if target["schema_sha256"] != expected_schema_sha256(name):
            raise ValueError(f"verified {name} schema digest differs")
        if target["expected_ids_sha256"] != target["actual_ids_sha256"] or (expected_rebuild and target["actual_count"] != rebuild[name]["prepared"]):
            raise ValueError(f"verified {name} ID/count evidence differs")
    if targets["entities"]["expected_count"] != official["graph_entities"] or targets["relationships"]["expected_count"] != source["relationships_normalized"] or targets["chunks"]["expected_count"] != source["text_chunks"]:
        raise ValueError("verified source and target counts differ")
    if expected_rebuild:
        if rebuild["entities"]["source_total"] != source["graph_nodes"] or rebuild["relationships"]["source_total"] != source["graph_edges_raw"] or rebuild["relationships"]["prepared"] != source["relationships_normalized"] or rebuild["chunks"]["source_total"] != source["text_chunks"]:
            raise ValueError("rebuild statistics do not bind authoritative source counts")
    if report["operation"] == "bootstrap_empty" and (any(source[key] != 0 for key in ("graph_nodes", "graph_edges_raw", "relationships_normalized", "text_chunks")) or any(targets[name]["actual_count"] != 0 for name in TARGETS)):
        raise ValueError("bootstrap report is not empty")
    duplicates_present = expected_rebuild and any(rebuild[name]["duplicates"] for name in TARGETS)
    if duplicate["applicable"] != duplicates_present or duplicate["status"] != ("succeeded" if duplicates_present else "not_applicable"):
        raise ValueError("verified duplicate policy applicability is invalid")
    if duplicates_present:
        require_digest(duplicate["evidence_sha256"], "duplicate policy digest")
        require_identifier(duplicate["reviewer"], "duplicate policy reviewer")
        if parse_time(duplicate["expires_at"], "duplicate policy expiry") <= finished:
            raise ValueError("duplicate policy expired before attempt completion")
    elif duplicate["evidence_sha256"] is not None or duplicate["reviewer"] is not None or duplicate["expires_at"] is not None or any(duplicate["max_duplicates"].values()):
        raise ValueError("inapplicable duplicate policy is populated")
    contract = report["contract"]
    if legacy["required"]:
        if legacy["applicable"] is not True or legacy["status"] != "succeeded" or legacy["manifest_sha256"] != contract["legacy_manifest_sha256"] or not legacy["reconciled"] or not (legacy["continuous_success_watermark"] == legacy["source_rows"] == legacy["accepted_rows"] == legacy["terminal_rows"]):
            raise ValueError("verified legacy evidence is incomplete")
    elif legacy["applicable"] or legacy["status"] != "not_applicable" or legacy["manifest_sha256"] is not None or any(legacy[key] != 0 for key in ("continuous_success_watermark", "source_rows", "accepted_rows", "terminal_rows")) or legacy["reconciled"] or legacy["retirement_eligible"]:
        raise ValueError("inapplicable legacy evidence is populated")
    expected_fixtures = report["operation"] != "bootstrap_empty"
    if fixtures["applicable"] != expected_fixtures or fixtures["status"] != ("succeeded" if expected_fixtures else "not_applicable") or fixtures["failed"] != 0:
        raise ValueError("verified fixture evidence is incomplete")
    if expected_fixtures:
        if fixtures["suite_version"] != contract["fixture_suite_version"] or fixtures["passed"] <= 0:
            raise ValueError("verified fixture suite evidence is invalid")
        require_digest(fixtures["results_sha256"], "fixture results digest")
    elif fixtures["suite_version"] is not None or fixtures["passed"] != 0 or fixtures["results_sha256"] is not None:
        raise ValueError("inapplicable fixture evidence is populated")
    if cleanup["status"] != "succeeded" or cleanup["applicable"] is not True or cleanup["worker_finalized"] is not True or cleanup["verifier_finalized"] is not True or cleanup["report_reread"] is not True:
        raise ValueError("verified cleanup evidence is incomplete")


def load_writer_evidence(path: str, expected_hash: str, attempt: str, generation: str, operation: str, contract_hash: str, *, now: datetime | None = None) -> dict[str, Any]:
    require_digest(expected_hash, "writer evidence digest")
    value = read_canonical_json(pathlib.Path(path), "writer evidence")
    require_exact(value, {"schema_version", "evidence_type", "attempt_id", "generation", "operation", "contract_sha256", "issued_at", "expires_at", "pipeline_idle_observed", "server_replicas", "automatic_restart_disabled", "observer", "uncontrolled_writers_attestation"}, "writer evidence")
    if digest(value) != expected_hash or value["schema_version"] != SCHEMA_VERSION or value["evidence_type"] != "xlh.lightrag_writer_fence.v2" or value["attempt_id"] != attempt or value["generation"] != generation or value["operation"] != operation or value["contract_sha256"] != contract_hash:
        raise ValueError("writer evidence identity or digest mismatch")
    issued = parse_time(value["issued_at"], "writer evidence issue timestamp")
    expires = parse_time(value["expires_at"], "writer evidence expiry timestamp")
    current = now or datetime.now(timezone.utc)
    if expires <= issued or current < issued or current >= expires:
        raise ValueError("writer evidence is not currently valid")
    if value["pipeline_idle_observed"] is not True or value["server_replicas"] != 0 or isinstance(value["server_replicas"], bool) or value["automatic_restart_disabled"] is not True or value["uncontrolled_writers_attestation"] != "no_uncontrolled_writers":
        raise ValueError("writer evidence does not prove quiescence")
    require_identifier(value["observer"], "writer evidence observer")
    return value


def writer_report(value: dict[str, Any], evidence_hash: str, backup_manifest_hash: str | None) -> dict[str, Any]:
    return {"applicable": True, "status": "succeeded", "evidence_type": value["evidence_type"], "evidence_sha256": evidence_hash, "issued_at": value["issued_at"], "expires_at": value["expires_at"], "pipeline_idle_observed": value["pipeline_idle_observed"], "server_replicas": value["server_replicas"], "automatic_restart_disabled": value["automatic_restart_disabled"], "observer": value["observer"], "uncontrolled_writers_attestation": value["uncontrolled_writers_attestation"], "backup_manifest_sha256": backup_manifest_hash}


def _safe_relative(value: Any, label: str) -> pathlib.PurePosixPath:
    if not isinstance(value, str) or not value or "\\" in value:
        raise ValueError(f"invalid {label}")
    result = pathlib.PurePosixPath(value)
    if result.is_absolute() or value != result.as_posix() or any(part in {"", ".", ".."} for part in result.parts):
        raise ValueError(f"unsafe or nonnormalized {label}")
    return result


def _normalized_tar_member(name: str) -> str:
    if not isinstance(name, str) or not name or name.startswith("/") or "\\" in name:
        raise ValueError("unsafe tar member path")
    while name.startswith("./"):
        name = name[2:]
    result = pathlib.PurePosixPath(name)
    if not name or any(part in {"", ".", ".."} for part in result.parts):
        raise ValueError("unsafe tar member path")
    return result.as_posix()


def verify_archive(base: pathlib.Path, component: dict[str, Any]) -> None:
    require_exact(component, {"archive_path", "size_bytes", "sha256", "member_count", "members_sha256", "members", "source_generation", "contract_sha256", "configuration_sha256", "writer_evidence_sha256"}, "backup component")
    require_nonnegative_int(component["size_bytes"], "backup archive size")
    require_nonnegative_int(component["member_count"], "backup member count")
    for key in ("sha256", "members_sha256", "contract_sha256", "configuration_sha256", "writer_evidence_sha256"):
        require_digest(component[key], f"backup component {key}")
    require_identifier(component["source_generation"], "backup component source generation")
    archive = base.joinpath(*_safe_relative(component["archive_path"], "archive path").parts)
    try:
        info = archive.lstat()
    except OSError as error:
        raise ValueError("backup archive is missing") from error
    if not stat.S_ISREG(info.st_mode) or info.st_size != component["size_bytes"] or file_digest(archive) != component["sha256"]:
        raise ValueError("backup archive size or digest mismatch")
    if not isinstance(component["members"], list) or component["member_count"] != len(component["members"]) or len(component["members"]) > MAX_ARCHIVE_MEMBERS or digest(component["members"]) != component["members_sha256"]:
        raise ValueError("backup member manifest mismatch")
    declared: dict[str, dict[str, Any]] = {}
    for member in component["members"]:
        require_exact(member, {"path", "size_bytes", "sha256"}, "backup member")
        normalized = _safe_relative(member["path"], "backup member path").as_posix()
        if normalized in declared or not isinstance(member["size_bytes"], int) or isinstance(member["size_bytes"], bool) or member["size_bytes"] < 0:
            raise ValueError("invalid or duplicate backup member")
        require_digest(member["sha256"], "backup member digest")
        declared[normalized] = member
    if list(declared) != sorted(declared):
        raise ValueError("backup members are not sorted")
    observed: set[str] = set()
    try:
        with tarfile.open(archive, mode="r:*") as bundle:
            for item in bundle:
                if item.name in {".", "./"} and item.isdir():
                    continue
                normalized = _normalized_tar_member(item.name)
                if item.issym() or item.islnk() or item.isdev() or item.isfifo() or not (item.isdir() or item.isfile()):
                    raise ValueError("unsafe tar member type")
                if item.isdir():
                    continue
                if normalized in observed or normalized not in declared:
                    raise ValueError("backup archive member set mismatch")
                stream = bundle.extractfile(item)
                if stream is None:
                    raise ValueError("backup regular member is unreadable")
                member_hash, member_size = hashlib.sha256(), 0
                while True:
                    block = stream.read(1024 * 1024)
                    if not block:
                        break
                    member_size += len(block)
                    member_hash.update(block)
                expected = declared[normalized]
                if member_size != expected["size_bytes"] or "sha256:" + member_hash.hexdigest() != expected["sha256"]:
                    raise ValueError("backup regular member size or digest mismatch")
                observed.add(normalized)
    except (tarfile.TarError, EOFError, OSError) as error:
        raise ValueError("backup archive is malformed or truncated") from error
    if observed != set(declared):
        raise ValueError("backup archive is missing a declared member")


def load_backup(path: str, expected_hash: str, contract_hash: str, expected_source_generation: str, expected_writer_hash: str) -> tuple[dict[str, Any], dict[str, Any]]:
    require_digest(expected_hash, "backup evidence digest")
    require_digest(expected_writer_hash, "backup writer evidence digest")
    evidence_path = pathlib.Path(path)
    envelope = read_canonical_json(evidence_path, "backup evidence")
    require_exact(envelope, {"schema_version", "evidence_type", "manifest", "manifest_sha256"}, "backup evidence")
    manifest = require_exact(envelope["manifest"], {"schema_version", "source_generation", "contract_sha256", "configuration_sha256", "writer_evidence_sha256", "components"}, "backup manifest")
    if digest(envelope) != expected_hash or envelope["schema_version"] != SCHEMA_VERSION or envelope["evidence_type"] != "xlh.lightrag_backup.v2" or envelope["manifest_sha256"] != digest(manifest):
        raise ValueError("backup evidence envelope or digest mismatch")
    if manifest["schema_version"] != SCHEMA_VERSION or manifest["source_generation"] != expected_source_generation or manifest["contract_sha256"] != contract_hash or manifest["configuration_sha256"] != contract_hash or manifest["writer_evidence_sha256"] != expected_writer_hash:
        raise ValueError("backup manifest binding mismatch")
    require_identifier(manifest["source_generation"], "backup source generation")
    for key in ("contract_sha256", "configuration_sha256", "writer_evidence_sha256"):
        require_digest(manifest[key], f"backup manifest {key}")
    components = require_exact(manifest["components"], {"working_directory", "milvus", "etcd", "minio"}, "backup components")
    for component in components.values():
        if component.get("source_generation") != expected_source_generation or component.get("contract_sha256") != contract_hash or component.get("configuration_sha256") != contract_hash or component.get("writer_evidence_sha256") != expected_writer_hash:
            raise ValueError("mixed-generation backup component")
        verify_archive(evidence_path.parent, component)
    return envelope, {"applicable": True, "status": "succeeded", "evidence_type": envelope["evidence_type"], "evidence_sha256": expected_hash, "manifest_sha256": envelope["manifest_sha256"], "source_generation": manifest["source_generation"], "configuration_sha256": manifest["configuration_sha256"], "writer_evidence_sha256": manifest["writer_evidence_sha256"], "component_count": 4}


def load_backup_writer_evidence(path: str, expected_hash: str, source_generation: str, contract_hash: str) -> dict[str, Any]:
    require_digest(expected_hash, "backup writer evidence digest")
    value = read_canonical_json(pathlib.Path(path), "backup writer evidence")
    require_exact(value, {"schema_version", "evidence_type", "attempt_id", "generation", "operation", "contract_sha256", "issued_at", "expires_at", "pipeline_idle_observed", "server_replicas", "automatic_restart_disabled", "observer", "uncontrolled_writers_attestation"}, "backup writer evidence")
    if (
        digest(value) != expected_hash
        or value["schema_version"] != SCHEMA_VERSION
        or value["evidence_type"] != "xlh.lightrag_writer_fence.v2"
        or value["generation"] != source_generation
        or value["operation"] != "backup"
        or value["contract_sha256"] != contract_hash
    ):
        raise ValueError("backup writer evidence identity or digest mismatch")
    require_identifier(value["attempt_id"], "backup writer evidence attempt ID")
    require_identifier(value["observer"], "backup writer evidence observer")
    issued = parse_time(value["issued_at"], "backup writer evidence issue timestamp")
    expires = parse_time(value["expires_at"], "backup writer evidence expiry timestamp")
    if expires <= issued:
        raise ValueError("backup writer evidence interval is invalid")
    if (
        value["pipeline_idle_observed"] is not True
        or value["server_replicas"] != 0
        or isinstance(value["server_replicas"], bool)
        or value["automatic_restart_disabled"] is not True
        or value["uncontrolled_writers_attestation"] != "no_uncontrolled_writers"
    ):
        raise ValueError("backup writer evidence does not prove quiescence")
    return value


def load_duplicate_policy(path: str | None, expected_hash: str | None, attempt: str, generation: str, operation: str, contract_hash: str, source_hash: str, *, now: datetime | None = None) -> tuple[dict[str, int], dict[str, Any]]:
    empty = {name: 0 for name in TARGETS}
    section = {"applicable": False, "status": "not_applicable", "evidence_sha256": None, "reviewer": None, "expires_at": None, "max_duplicates": empty}
    if path is None and expected_hash is None:
        return empty, section
    if not path or not expected_hash:
        raise ValueError("duplicate policy path and digest must be supplied together")
    value = read_canonical_json(pathlib.Path(path), "duplicate policy")
    require_exact(value, {"schema_version", "evidence_type", "attempt_id", "generation", "operation", "contract_sha256", "source_sha256", "reviewer", "issued_at", "expires_at", "max_duplicates"}, "duplicate policy")
    maxima = require_exact(value["max_duplicates"], set(TARGETS), "duplicate maxima")
    if digest(value) != expected_hash or value["schema_version"] != SCHEMA_VERSION or value["evidence_type"] != "xlh.lightrag_duplicate_policy.v2" or value["attempt_id"] != attempt or value["generation"] != generation or value["operation"] != operation or value["contract_sha256"] != contract_hash or value["source_sha256"] != source_hash:
        raise ValueError("duplicate policy identity or digest mismatch")
    issued = parse_time(value["issued_at"], "duplicate policy issue timestamp")
    expires = parse_time(value["expires_at"], "duplicate policy expiry timestamp")
    current = now or datetime.now(timezone.utc)
    if expires <= issued or current < issued or current >= expires:
        raise ValueError("duplicate policy is not currently valid")
    require_identifier(value["reviewer"], "duplicate policy reviewer")
    for name, maximum in maxima.items():
        if not isinstance(maximum, int) or isinstance(maximum, bool) or maximum < 0:
            raise ValueError(f"invalid duplicate maximum for {name}")
    return maxima, {"applicable": True, "status": "succeeded", "evidence_sha256": expected_hash, "reviewer": value["reviewer"], "expires_at": value["expires_at"], "max_duplicates": maxima}


def source_digest(nodes: list[Any], edges: list[Any], chunks: list[tuple[str, Any]], stores: dict[str, Any]) -> str:
    return digest({"nodes": sorted(nodes, key=canonical_bytes), "edges": sorted(edges, key=canonical_bytes), "text_chunks": sorted(chunks, key=lambda item: item[0]), "document_stores": stores})


def _read_json_mapping(path: pathlib.Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    if path.is_symlink() or not path.is_file():
        raise ValueError("authoritative JSON store is unsafe")
    with path.open("r", encoding="utf-8") as source:
        value = json.load(source)
    if not isinstance(value, dict):
        raise ValueError("authoritative JSON store is not an object")
    return value


def inspect_document_stores() -> dict[str, Any]:
    root = pathlib.Path(require_env("WORKING_DIR")) / require_env("WORKSPACE")
    full_docs = _read_json_mapping(root / "kv_store_full_docs.json")
    statuses = _read_json_mapping(root / "kv_store_doc_status.json")
    if set(full_docs) != set(statuses):
        raise ValueError("full-document and document-status ID sets differ")
    for record in statuses.values():
        if not isinstance(record, dict) or str(record.get("status", "")).lower() != "processed":
            raise ValueError("document status store is inconsistent or nonterminal")
    return {"full_documents": len(full_docs), "document_status": len(statuses), "full_documents_sha256": digest(full_docs), "document_status_sha256": digest(statuses)}


async def expected_ids(tool: Any, rebuild_module: Any) -> tuple[dict[str, set[str]], dict[str, Any]]:
    nodes = await tool.graph.get_all_nodes()
    edges = await tool.graph.get_all_edges()
    chunk_ids = [str(value) for value in await rebuild_module.enumerate_kv_keys(tool.text_chunks)]
    entities: set[str] = set()
    for node in nodes:
        name = node.get("entity_id") or node.get("id")
        if name is not None and str(name).strip():
            entities.add(rebuild_module.compute_mdhash_id(str(name), prefix="ent-"))
    relationships: set[str] = set()
    for edge in edges:
        source, target = edge.get("source"), edge.get("target")
        if source is not None and target is not None and str(source).strip() and str(target).strip():
            relationships.add(rebuild_module.make_relation_vdb_ids(str(source), str(target))[0])
    chunks: list[tuple[str, Any]] = []
    valid_chunks: set[str] = set()
    for offset in range(0, len(chunk_ids), 500):
        page = chunk_ids[offset:offset + 500]
        records = await tool.text_chunks.get_by_ids(page)
        if len(records) != len(page):
            raise ValueError("text chunk enumeration returned an incomplete page")
        for key, record in zip(page, records):
            chunks.append((key, record))
            if not isinstance(record, dict) or not record.get("content"):
                raise ValueError("authoritative text chunk is missing content")
            valid_chunks.add(key)
    stores = inspect_document_stores()
    return {"entities": entities, "relationships": relationships, "chunks": valid_chunks}, {"graph_nodes": len(nodes), "graph_edges_raw": len(edges), "relationships_normalized": len(relationships), "text_chunks": len(chunk_ids), "full_documents": stores["full_documents"], "document_status": stores["document_status"], "sha256": source_digest(nodes, edges, chunks, stores)}


def _normalize_type(value: Any) -> str:
    numeric = {5: "INT64", 21: "VARCHAR", 101: "FLOAT_VECTOR"}
    if isinstance(value, int) and not isinstance(value, bool):
        return numeric.get(value, str(value))
    if hasattr(value, "name"):
        value = value.name
    normalized = str(value).upper().replace("DATATYPE.", "").replace("VAR_CHAR", "VARCHAR")
    return {"FLOATVECTOR": "FLOAT_VECTOR", "INT64": "INT64", "VARCHAR": "VARCHAR"}.get(normalized, normalized)


def _field_map(description: dict[str, Any]) -> dict[str, dict[str, Any]]:
    fields = description.get("fields") or (description.get("schema") or {}).get("fields") or []
    if not isinstance(fields, list):
        raise ValueError("Milvus fields are not an array")
    result = {str(field.get("name")): field for field in fields if isinstance(field, dict) and field.get("name")}
    if len(result) != len(fields):
        raise ValueError("Milvus fields have missing or duplicate names")
    return result


def normalize_collection_schema(name: str, description: dict[str, Any]) -> dict[str, Any]:
    fields = _field_map(description)
    if set(fields) != set(EXPECTED_FIELD_SCHEMAS[name]):
        raise ValueError(f"{name} collection field set mismatch")
    normalized_fields: list[dict[str, Any]] = []
    for field_name in sorted(fields):
        raw = fields[field_name]
        expected_type, expected_length, expected_nullable, expected_primary = EXPECTED_FIELD_SCHEMAS[name][field_name]
        field_type = _normalize_type(raw.get("type", raw.get("data_type", raw.get("dtype"))))
        params = raw.get("params") or {}
        raw_length = params.get("max_length", raw.get("max_length"))
        length = int(raw_length) if raw_length is not None else None
        nullable = raw.get("nullable", False)
        primary = raw.get("is_primary", raw.get("is_primary_key", raw.get("primary_key", False)))
        if field_type != expected_type or length != expected_length or nullable is not expected_nullable or primary is not expected_primary:
            raise ValueError(f"{name}.{field_name} schema mismatch")
        dimension = int(params.get("dim", raw.get("dim", 0))) if field_name == "vector" else 0
        if field_name == "vector" and dimension != 1024:
            raise ValueError(f"{name} vector dimension mismatch")
        normalized_fields.append({"name": field_name, "type": field_type, "max_length": length, "nullable": nullable, "primary": primary, "dimension": dimension})
    dynamic = description.get("enable_dynamic_field", (description.get("schema") or {}).get("enable_dynamic_field"))
    if dynamic is not True:
        raise ValueError(f"{name} dynamic-field contract mismatch")
    normalized = {"database": "lightrag", "collection": EXPECTED_COLLECTIONS[name], "enable_dynamic_field": True, "fields": normalized_fields}
    if normalized != expected_collection_schema(name):
        raise ValueError(f"{name} normalized schema differs from the readiness contract")
    return normalized


def _index_value(index: dict[str, Any], key: str) -> Any:
    if key in index:
        return index[key]
    for nested_key in ("params", "index_param", "index_params"):
        nested = index.get(nested_key)
        if isinstance(nested, dict):
            value = _index_value(nested, key)
            if value is not None:
                return value
    return None


def _index_records(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, list):
        return [item for item in value if isinstance(item, dict)]
    return [value] if isinstance(value, dict) else []


async def inspect_target(name: str, storage: Any, expected: set[str], prepared: int | None = None) -> dict[str, Any]:
    client, collection = storage._client, storage.final_namespace
    if collection != EXPECTED_COLLECTIONS[name] or os.environ.get("MILVUS_DB_NAME") != "lightrag":
        raise ValueError(f"{name} collection/database identity mismatch")
    normalized_schema = normalize_collection_schema(name, client.describe_collection(collection_name=collection))
    indexes: list[dict[str, Any]] = []
    for index_name in client.list_indexes(collection_name=collection):
        indexes.extend(_index_records(client.describe_index(collection_name=collection, index_name=index_name)))
    vector_indexes = [index for index in indexes if _index_value(index, "field_name") == "vector"]
    if len(vector_indexes) != 1 or str(_index_value(vector_indexes[0], "index_type")).upper() != "AUTOINDEX" or str(_index_value(vector_indexes[0], "metric_type")).upper() != "COSINE":
        raise ValueError(f"{name} vector index mismatch")
    client.load_collection(collection_name=collection)
    iterator = client.query_iterator(collection_name=collection, batch_size=1000, filter="", output_fields=["id"])
    actual: set[str] = set()
    try:
        while True:
            rows = iterator.next()
            if not rows:
                break
            for row in rows:
                value = str(row["id"])
                if value in actual:
                    raise ValueError(f"duplicate {name} primary key observed")
                actual.add(value)
    finally:
        iterator.close()
    if actual != expected or (prepared is not None and len(actual) != prepared):
        raise ValueError(f"{name} exact ID set/count mismatch")
    return {"collection": collection, "database": "lightrag", "dynamic_fields": True, "schema_sha256": digest(normalized_schema), "vector_field_type": "FLOAT_VECTOR", "vector_dimension": 1024, "primary_key_field": "id", "index_type": "AUTOINDEX", "metric_type": "COSINE", "expected_count": len(expected), "actual_count": len(actual), "expected_ids_sha256": digest(sorted(expected)), "actual_ids_sha256": digest(sorted(actual))}


async def setup_tool() -> tuple[Any, Any]:
    from lightrag.kg.shared_storage import initialize_share_data
    from lightrag.tools import rebuild_vdb
    initialize_share_data(workers=1)
    tool = rebuild_vdb.RebuildTool()
    if not await tool.setup_storages():
        raise RuntimeError("official storage initialization failed")
    return tool, rebuild_vdb


async def finalize_tool(tool: Any) -> None:
    errors: list[str] = []
    for storage in tool.all_storages():
        if storage is not None:
            try:
                await storage.finalize()
            except Exception as error:
                errors.append(type(error).__name__)
    from lightrag.kg.shared_storage import finalize_share_data
    finalize_share_data()
    if errors:
        raise RuntimeError("storage finalization failed")


def verified_legacy_evidence(args: argparse.Namespace, contract_hash: str) -> dict[str, Any]:
    required = os.environ.get("XLH_LEGACY_KNOWLEDGE", "") == "required"
    if not required:
        if os.environ.get("XLH_LEGACY_KNOWLEDGE") != "absent" or args.legacy_report:
            raise ValueError("legacy knowledge must be exactly required or proven absent")
        return {"applicable": False, "status": "not_applicable", "required": False, "manifest_sha256": None, "continuous_success_watermark": 0, "source_rows": 0, "accepted_rows": 0, "terminal_rows": 0, "reconciled": False, "retirement_eligible": False}
    if not args.legacy_report or not args.legacy_report_sha256:
        raise ValueError("legacy report and digest are required")
    value = read_canonical_json(pathlib.Path(args.legacy_report), "legacy import evidence")
    require_exact(value, {"schema_version", "evidence_type", "generation", "contract_sha256", "manifest_sha256", "continuous_success_watermark", "source_rows", "accepted_rows", "terminal_rows", "reconciled", "retirement_eligible"}, "legacy import evidence")
    if digest(value) != args.legacy_report_sha256 or value["schema_version"] != SCHEMA_VERSION or value["evidence_type"] != "xlh.lightrag_legacy_import.v2" or value["generation"] != args.generation or value["contract_sha256"] != contract_hash or value["manifest_sha256"] != os.environ.get("XLH_LEGACY_MANIFEST_SHA256") or not value["reconciled"] or not (value["continuous_success_watermark"] == value["source_rows"] == value["accepted_rows"] == value["terminal_rows"]):
        raise ValueError("legacy knowledge import gate is incomplete")
    require_digest(args.legacy_report_sha256, "legacy report digest")
    return {"applicable": True, "status": "succeeded", "required": True, "manifest_sha256": value["manifest_sha256"], "continuous_success_watermark": value["continuous_success_watermark"], "source_rows": value["source_rows"], "accepted_rows": value["accepted_rows"], "terminal_rows": value["terminal_rows"], "reconciled": True, "retirement_eligible": value["retirement_eligible"]}


def _signal_fixture_process_group(process: asyncio.subprocess.Process, signum: signal.Signals) -> None:
    try:
        os.killpg(process.pid, signum)
    except ProcessLookupError:
        pass


async def _wait_fixture_process_group(process: asyncio.subprocess.Process) -> None:
    if process.returncode is None:
        await process.wait()
    while True:
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            return
        await asyncio.sleep(0.05)


async def _terminate_fixture_process(process: asyncio.subprocess.Process) -> None:
    _signal_fixture_process_group(process, signal.SIGTERM)
    try:
        await asyncio.wait_for(
            _wait_fixture_process_group(process),
            timeout=FIXTURE_TERMINATION_GRACE_SECONDS,
        )
    except asyncio.TimeoutError:
        _signal_fixture_process_group(process, signal.SIGKILL)
        if process.returncode is None:
            await process.wait()
        try:
            await asyncio.wait_for(
                _wait_fixture_process_group(process),
                timeout=FIXTURE_TERMINATION_GRACE_SECONDS,
            )
        except asyncio.TimeoutError:
            raise RuntimeError("fixture process group did not terminate") from None


async def run_fixture(args: argparse.Namespace, contract_hash: str, attempt: str, operation: str, source_hash: str) -> dict[str, Any]:
    if operation == "bootstrap_empty":
        return {"applicable": False, "status": "not_applicable", "suite_version": None, "passed": 0, "failed": 0, "results_sha256": None}
    if not args.fixture_report or not args.fixture_command:
        raise ValueError("fixture report and command are required")
    environment = dict(os.environ)
    environment.update({"XLH_REBUILD_ATTEMPT_ID": attempt, "XLH_REBUILD_GENERATION": args.generation, "XLH_REBUILD_OPERATION": operation, "XLH_REBUILD_CONTRACT_SHA256": contract_hash, "XLH_REBUILD_SOURCE_SHA256": source_hash})
    creation = asyncio.create_task(
        asyncio.create_subprocess_exec(
            *args.fixture_command,
            env=environment,
            start_new_session=True,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )
    )
    try:
        process = await asyncio.shield(creation)
    except asyncio.CancelledError:
        try:
            process = await creation
        except BaseException:
            pass
        else:
            await _terminate_fixture_process(process)
        raise
    except BaseException:
        raise ClassifiedFailure("fixture_spawn_failed", retryable=True) from None
    try:
        returncode = await asyncio.wait_for(process.wait(), timeout=args.fixture_timeout)
    except asyncio.CancelledError:
        await _terminate_fixture_process(process)
        raise
    except asyncio.TimeoutError:
        try:
            await _terminate_fixture_process(process)
        except BaseException:
            raise ClassifiedFailure("fixture_cleanup_failed", retryable=True) from None
        raise ClassifiedFailure("fixture_timeout", retryable=True) from None
    except BaseException:
        try:
            await _terminate_fixture_process(process)
        except BaseException:
            raise ClassifiedFailure("fixture_cleanup_failed", retryable=True) from None
        raise ClassifiedFailure("fixture_failed", retryable=True) from None
    # The fixture contract is complete only when its entire process group is
    # quiescent. Terminate descendants that outlive the direct child before
    # reading evidence or publishing any terminal fence state.
    try:
        await _terminate_fixture_process(process)
    except BaseException:
        raise ClassifiedFailure("fixture_cleanup_failed", retryable=True) from None
    if returncode != 0:
        raise ClassifiedFailure("fixture_failed", retryable=True)
    value = read_canonical_json(pathlib.Path(args.fixture_report), "fixture evidence")
    require_exact(value, {"schema_version", "evidence_type", "attempt_id", "generation", "operation", "contract_sha256", "source_sha256", "suite_version", "passed", "failed", "results_sha256"}, "fixture evidence")
    if value["schema_version"] != SCHEMA_VERSION or value["evidence_type"] != "xlh.lightrag_fixture.v2" or value["attempt_id"] != attempt or value["generation"] != args.generation or value["operation"] != operation or value["contract_sha256"] != contract_hash or value["source_sha256"] != source_hash or value["suite_version"] != os.environ["XLH_LIGHTRAG_FIXTURE_SUITE_VERSION"] or not isinstance(value["passed"], int) or isinstance(value["passed"], bool) or value["passed"] <= 0 or not isinstance(value["failed"], int) or isinstance(value["failed"], bool) or value["failed"] != 0:
        raise ValueError("fixture evidence is invalid or belongs to another attempt")
    require_digest(value["results_sha256"], "fixture result digest")
    return {"applicable": True, "status": "succeeded", "suite_version": value["suite_version"], "passed": value["passed"], "failed": 0, "results_sha256": value["results_sha256"]}


def _bounded_error(error: BaseException) -> dict[str, Any]:
    if isinstance(error, ClassifiedFailure):
        code, retryable = error.code, error.retryable
    elif isinstance(error, asyncio.CancelledError):
        code, retryable = "operation_cancelled", True
    elif isinstance(error, ValueError):
        code, retryable = "validation_failed", False
    elif isinstance(error, OSError):
        code, retryable = "io_failed", True
    else:
        code, retryable = "unknown", False
    return {
        "stage": "controller",
        "target": "all",
        "code": code,
        "retryable": retryable,
    }


def _failed_stats(name: str, value: Any) -> dict[str, Any]:
    result = empty_stats(name)
    if not isinstance(value, dict):
        return result
    for key in ("source_total", "prepared", "rebuilt", "staged", "skipped", "duplicates", "batches", "failed_batches"):
        candidate = value.get(key)
        if isinstance(candidate, int) and not isinstance(candidate, bool) and candidate >= 0:
            result[key] = candidate
    raw_errors = value.get("errors")
    if isinstance(raw_errors, list):
        for _error in raw_errors[:32]:
            result["errors"].append(
                {
                    "stage": "rebuild",
                    "target": name,
                    "code": "unknown",
                    "retryable": True,
                }
            )
    return result


def _recover_abandoned(
    store: FenceStore,
    current: dict[str, Any] | None,
    _runtime_contract: dict[str, Any],
    *,
    started_ns: int | None = None,
    args: argparse.Namespace | None = None,
) -> dict[str, Any] | None:
    if current is None or current["state"] not in {"rebuilding", "stale"}:
        return current
    report_path = store.reports / f"{current['attempt_id']}.json"
    try:
        report = read_canonical_json(report_path, "abandoned attempt report")
    except FileNotFoundError:
        if current["state"] == "stale":
            return current
        context = store.read_attempt(current["attempt_id"])
        if any(context[key] != current[key] for key in ("attempt_id", "generation", "operation", "contract_sha256")):
            raise ValueError("abandoned attempt context does not match marker")
        raw_contract = contract_from_report(context["software"], context["contract"])
        report = empty_report(current["attempt_id"], current["generation"], current["operation"], raw_contract, current["contract_sha256"], context["started_at"], reason="abandoned_rebuilding")
        report["software"] = context["software"]
        report["contract"] = context["contract"]
        report["errors"] = [{"stage": "controller", "target": "all", "code": "abandoned_rebuilding", "retryable": True}]
        relative, report_hash = store.report(report)
    else:
        validate_report(report)
        if (
            any(report[key] != current[key] for key in ("attempt_id", "generation", "operation"))
            or report["contract"]["contract_sha256"] != current["contract_sha256"]
        ):
            raise ValueError("existing terminal attempt report does not match nonterminal marker")
        relative, report_hash = f"reports/{current['attempt_id']}.json", digest(report)
    assert_transition(current["state"], report["state"])
    terminal_marker = marker(report["state"], current["generation"], current["attempt_id"], current["operation"], current["contract_sha256"], report["reason_code"], relative, report_hash)
    store.publish(terminal_marker)
    if args is not None and terminal_marker["state"] == "failed":
        args._failed_marker_published = True
    emit_transition(
        terminal_marker,
        "recovered",
        _elapsed_ms(started_ns) if started_ns is not None else 0,
        report,
        args=args,
    )
    if args is not None:
        args._recovered_transition = True
    return terminal_marker


def _preflight_args(args: argparse.Namespace, operation: str, contract_hash: str) -> tuple[dict[str, Any], dict[str, Any] | None, dict[str, Any] | None]:
    require_identifier(args.attempt_id, "attempt ID")
    require_identifier(args.generation, "generation")
    if not args.writer_evidence or not args.writer_evidence_sha256:
        raise ValueError("external writer evidence and its digest are required")
    writer = load_writer_evidence(args.writer_evidence, args.writer_evidence_sha256, args.attempt_id, args.generation, operation, contract_hash)
    backup_envelope = backup_section = None
    if operation in {"migration_rebuild", "restore_verify"}:
        if not args.backup_report or not args.backup_report_sha256 or not args.backup_writer_evidence or not args.backup_writer_evidence_sha256:
            raise ValueError("backup report and backup writer evidence paths and digests are required")
        source_generation = args.expected_source_generation or (args.generation if operation == "migration_rebuild" else None)
        if not source_generation:
            raise ValueError("expected source generation is required")
        if operation == "restore_verify" and source_generation == args.generation:
            raise ValueError("restore target generation must differ from source generation")
        load_backup_writer_evidence(args.backup_writer_evidence, args.backup_writer_evidence_sha256, source_generation, contract_hash)
        backup_envelope, backup_section = load_backup(args.backup_report, args.backup_report_sha256, contract_hash, source_generation, args.backup_writer_evidence_sha256)
    return writer, backup_envelope, backup_section


async def execute(args: argparse.Namespace) -> int:
    if args.command == "contract-hash":
        print(digest(build_contract()))
        return 0
    if not args.fence_dir or not args.generation:
        raise ValueError("--fence-dir and --generation are required")
    if args.command == "readiness":
        store = FenceStore(pathlib.Path(args.fence_dir), create=False)
        with store.serving_lease(exclusive=False):
            store.verify_current(
                args.generation, require_env("XLH_EXPECTED_CONTRACT_SHA256")
            )
        return 0
    operation, contract = OPERATIONS[args.command], build_contract()
    contract_hash = digest(contract)
    require_identifier(args.attempt_id, "attempt ID")
    started_ns = time.monotonic_ns()
    store = FenceStore(pathlib.Path(args.fence_dir))
    # Lock ordering is part of the writer-fence contract: serving lease first,
    # controller serialization second.  Never invert this order.
    with store.serving_lease(exclusive=True), store:
        _, current = store.current_or_absent()
        current = _recover_abandoned(
            store, current, contract, started_ns=started_ns, args=args
        )
        # A successfully logged recovery belongs to the previous attempt. A
        # later pre-publication failure in this invocation still gets the
        # bounded stderr classification from main().
        args._failed_marker_published = False
        previous_state = current["state"] if current is not None else "absent"
        if (
            args.command in {"rebuild", "bootstrap", "restore-verify", "revalidate"}
            and current is not None
            and current["state"] == "verified"
            and current["attempt_id"] == args.attempt_id
            and current["generation"] == args.generation
            and current["operation"] == operation
            and current["contract_sha256"] == contract_hash
        ):
            store.verify_current(args.generation, contract_hash)
            if not getattr(args, "_recovered_transition", False):
                report = read_canonical_json(
                    store.root / current["report_path"],
                    "idempotent rebuild report",
                )
                validate_report(report, verified=True)
                emit_transition(
                    current,
                    "idempotent",
                    _elapsed_ms(started_ns),
                    report,
                    args=args,
                )
            return 0
        if args.command == "invalidate":
            if current is None:
                raise ValueError("cannot invalidate an absent fence")
            assert_transition(current["state"], "stale")
            transition = marker("stale", args.generation, args.attempt_id, operation, contract_hash, args.reason)
            store.publish(transition)
            emit_transition(transition, "ok", _elapsed_ms(started_ns), args=args)
            return 0
        if args.command == "prepare-restore":
            if not args.writer_evidence or not args.writer_evidence_sha256:
                raise ValueError("external writer evidence and its digest are required")
            load_writer_evidence(args.writer_evidence, args.writer_evidence_sha256, args.attempt_id, args.generation, operation, contract_hash)
            assert_transition(previous_state, "stale")
            transition = marker("stale", args.generation, args.attempt_id, operation, contract_hash, "restore_pending_verification")
            store.publish(transition)
            emit_transition(transition, "ok", _elapsed_ms(started_ns), args=args)
            _preflight_args(args, operation, contract_hash)
            return 0
        writer, _, backup = _preflight_args(args, operation, contract_hash)
        if args.command == "rebuild" and args.approval != "approved-destructive-paid-rebuild":
            raise ValueError("explicit destructive paid rebuild approval is required")
        started = utc_now()
        if args.command in {"restore-verify", "revalidate"}:
            if current is None or current["state"] != "stale" or current["attempt_id"] != args.attempt_id or current["generation"] != args.generation or current["operation"] != operation or current["contract_sha256"] != contract_hash or (args.command == "restore-verify" and current["reason_code"] != "restore_pending_verification"):
                raise ValueError("verification requires its exact stale marker")
        else:
            assert_transition(previous_state, "rebuilding")
            store.attempt(attempt_context(args.attempt_id, args.generation, operation, contract, contract_hash, started))
            transition = marker("rebuilding", args.generation, args.attempt_id, operation, contract_hash, "in_progress")
            store.publish(transition)
            emit_transition(transition, "ok", _elapsed_ms(started_ns), args=args)
        return await _run_operation(
            args, store, operation, contract, contract_hash, writer, backup,
            started=started, started_ns=started_ns,
        )


async def _run_operation(args: argparse.Namespace, store: FenceStore, operation: str, contract: dict[str, Any], contract_hash: str, writer: dict[str, Any], backup: dict[str, Any] | None, *, started: str | None = None, started_ns: int | None = None) -> int:
    started = started or utc_now()
    started_ns = started_ns if started_ns is not None else time.monotonic_ns()
    tool = rebuild_module = None
    stats: dict[str, Any] = {}
    worker_finalized = verifier_finalized = False
    terminal_report_created = False
    base_report = empty_report(args.attempt_id, args.generation, operation, contract, contract_hash, started)
    base_report["writer_fence"] = writer_report(writer, args.writer_evidence_sha256, backup["manifest_sha256"] if backup else None)
    if backup:
        base_report["backup"] = backup
    try:
        legacy = verified_legacy_evidence(args, contract_hash)
        base_report["legacy_import"] = legacy
        tool, rebuild_module = await setup_tool()
        expected_before, source_before = await expected_ids(tool, rebuild_module)
        if args.command == "bootstrap" and any(source_before[key] for key in ("graph_nodes", "graph_edges_raw", "text_chunks", "full_documents", "document_status")):
            raise ValueError("empty bootstrap has nonempty authoritative sources")
        duplicate_maxima, duplicate_section = load_duplicate_policy(args.duplicate_policy, args.duplicate_policy_sha256, args.attempt_id, args.generation, operation, contract_hash, source_before["sha256"])
        if args.command == "rebuild":
            calls = (
                ("entities", lambda: rebuild_module.rebuild_entities_vdb(tool.graph, tool.entities_vdb, tool.global_config, batch_size=args.batch_size)),
                ("relationships", lambda: rebuild_module.rebuild_relationships_vdb(tool.graph, tool.relationships_vdb, tool.global_config, batch_size=args.batch_size)),
                ("chunks", lambda: rebuild_module.rebuild_chunks_vdb(tool.text_chunks, tool.chunks_vdb, batch_size=args.batch_size)),
            )
            for name, call in calls:
                stats[name] = await call()
                validate_stats(name, stats[name], duplicate_maxima[name])
        await finalize_tool(tool)
        worker_finalized, tool = True, None
        tool, rebuild_module = await setup_tool()
        expected_after, source_after = await expected_ids(tool, rebuild_module)
        if source_before["sha256"] != source_after["sha256"] or expected_before != expected_after:
            raise ValueError("authoritative sources changed during operation")
        storages = {"entities": tool.entities_vdb, "relationships": tool.relationships_vdb, "chunks": tool.chunks_vdb}
        targets = {name: await inspect_target(name, storages[name], expected_after[name], stats[name]["prepared"] if name in stats else None) for name in TARGETS}
        if args.command == "bootstrap" and any(targets[name]["actual_count"] for name in TARGETS):
            raise ValueError("empty bootstrap found nonempty vector targets")
        consistency = await rebuild_module.check_vdb_consistency(tool.graph, tool.entities_vdb, tool.relationships_vdb, batch_size=args.batch_size)
        if consistency.get("consistent") is not True or any(consistency.get(key) != 0 for key in ("missing_entities", "missing_relations", "skipped_nodes", "skipped_edges")):
            raise ValueError("official graph consistency check failed")
        fixture = await run_fixture(args, contract_hash, args.attempt_id, operation, source_after["sha256"])
        await finalize_tool(tool)
        verifier_finalized, tool = True, None
        report = empty_report(args.attempt_id, args.generation, operation, contract, contract_hash, started, state="verified", reason="verified")
        report.update({
            "writer_fence": writer_report(writer, args.writer_evidence_sha256, backup["manifest_sha256"] if backup else None),
            "source": {"applicable": True, "status": "succeeded", "before_sha256": source_before["sha256"], "after_sha256": source_after["sha256"], "graph_nodes": source_after["graph_nodes"], "graph_edges_raw": source_after["graph_edges_raw"], "relationships_normalized": source_after["relationships_normalized"], "text_chunks": source_after["text_chunks"]},
            "targets": {"applicable": True, "status": "succeeded", **targets},
            "duplicate_policy": duplicate_section,
            "official_consistency": {"applicable": True, "status": "succeeded", **{key: int(consistency.get(key, 0)) for key in ("graph_entities", "graph_relations", "missing_entities", "missing_relations", "skipped_nodes", "skipped_edges")}},
            "legacy_import": legacy, "fixtures": fixture,
            "checks": {name: True for name in REQUIRED_CHECKS},
            "cleanup": {"applicable": True, "status": "succeeded", "worker_finalized": worker_finalized, "verifier_finalized": verifier_finalized, "report_reread": True},
            "errors": [], "finished_at": utc_now(),
        })
        if backup:
            report["backup"] = backup
        if operation == "migration_rebuild":
            report["rebuild"] = {"applicable": True, "status": "succeeded", **stats}
        validate_report(report, verified=True)
        relative, report_hash = store.report(report)
        terminal_report_created = True
        persisted_report = read_canonical_json(store.root / relative, "published rebuild report")
        if digest(persisted_report) != report_hash:
            raise ValueError("published rebuild report failed durable reread")
        validate_report(persisted_report, verified=True)
        assert_transition("rebuilding" if args.command in {"rebuild", "bootstrap"} else "stale", "verified")
        transition = marker("verified", args.generation, args.attempt_id, operation, contract_hash, "verified", relative, report_hash)
        store.publish(transition)
        emit_transition(
            transition, "ok", _elapsed_ms(started_ns), persisted_report, args=args
        )
        return 0
    except BaseException as error:
        if tool is not None:
            try:
                await asyncio.shield(finalize_tool(tool))
                if worker_finalized:
                    verifier_finalized = True
                else:
                    worker_finalized = True
            except BaseException:
                pass
        if terminal_report_created or (store.reports / f"{args.attempt_id}.json").exists():
            # Immutable terminal reports are never replaced. A failure after
            # their durable creation leaves rebuilding/stale current. The next
            # exact retry validates the report and publishes its marker.
            if isinstance(error, asyncio.CancelledError):
                raise RuntimeError("operation cancelled after terminal report publication") from None
            raise
        base_report["finished_at"] = utc_now()
        base_report["errors"] = [_bounded_error(error)]
        base_report["rebuild"].update({name: _failed_stats(name, stats.get(name)) for name in TARGETS})
        if operation == "migration_rebuild" and stats:
            base_report["rebuild"]["status"] = "failed"
        base_report["cleanup"] = {"applicable": True, "status": "failed", "worker_finalized": worker_finalized, "verifier_finalized": verifier_finalized, "report_reread": False}
        relative, report_hash = store.report(base_report)
        transition = marker("failed", args.generation, args.attempt_id, operation, contract_hash, "operation_failed", relative, report_hash)
        store.publish(transition)
        args._failed_marker_published = True
        emit_transition(
            transition, "error", _elapsed_ms(started_ns), base_report, args=args
        )
        if isinstance(error, asyncio.CancelledError):
            raise RuntimeError("operation cancelled") from None
        raise


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    result.add_argument("command", choices=("contract-hash", "rebuild", "bootstrap", "prepare-restore", "restore-verify", "revalidate", "invalidate", "readiness"))
    result.add_argument("--fence-dir")
    result.add_argument("--generation")
    result.add_argument("--attempt-id")
    result.add_argument("--reason", choices=sorted(REASON_CODES["stale"]), default="invalidated")
    result.add_argument("--approval")
    result.add_argument("--writer-evidence")
    result.add_argument("--writer-evidence-sha256")
    result.add_argument("--fixture-report")
    result.add_argument("--fixture-command", nargs=argparse.REMAINDER)
    result.add_argument("--fixture-timeout", type=int, default=600)
    result.add_argument("--legacy-report")
    result.add_argument("--legacy-report-sha256")
    result.add_argument("--backup-report")
    result.add_argument("--backup-report-sha256")
    result.add_argument("--backup-writer-evidence")
    result.add_argument("--backup-writer-evidence-sha256")
    result.add_argument("--expected-source-generation")
    result.add_argument("--duplicate-policy")
    result.add_argument("--duplicate-policy-sha256")
    result.add_argument("--batch-size", type=int, default=500)
    return result


async def _execute_with_signals(args: argparse.Namespace) -> int:
    loop = asyncio.get_running_loop()
    operation_task = asyncio.create_task(execute(args))
    cancellation_requested = False
    installed: list[signal.Signals] = []

    def request_cancellation() -> None:
        nonlocal cancellation_requested
        if cancellation_requested or operation_task.done():
            return
        cancellation_requested = True
        operation_task.cancel()

    try:
        for signum in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(signum, request_cancellation)
            installed.append(signum)
        try:
            return await operation_task
        except asyncio.CancelledError:
            raise RuntimeError("operation cancelled") from None
    finally:
        for signum in installed:
            loop.remove_signal_handler(signum)


def main() -> None:
    args = parser().parse_args()
    args._failed_marker_published = False
    args._recovered_transition = False
    if args.command not in {"contract-hash", "readiness"} and not args.attempt_id:
        raise SystemExit("--attempt-id is required")
    if args.batch_size < 1 or args.batch_size > 5000 or args.fixture_timeout < 1 or args.fixture_timeout > 3600:
        raise SystemExit("batch size and fixture timeout are out of range")
    try:
        exit_code = asyncio.run(_execute_with_signals(args))
    except BaseException as error:
        if not args._failed_marker_published:
            bounded = _bounded_error(error)
            event = {
                "schema_version": 1,
                "event": "lightrag.fence.failure",
                "command": args.command,
                "outcome": "error",
                "reason_code": bounded["code"],
                "retryable": bounded["retryable"],
            }
            print(canonical_bytes(event).decode("utf-8"), file=os.sys.stderr, flush=True)
        raise SystemExit(1)
    raise SystemExit(exit_code)


if __name__ == "__main__":
    main()
