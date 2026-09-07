import asyncio
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import sys
import tarfile
import tempfile
import unittest
from datetime import datetime, timezone
from types import SimpleNamespace
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("rebuild_fence.py")
SPEC = importlib.util.spec_from_file_location("rebuild_fence", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)

SHA_A = "sha256:" + "a" * 64
SHA_B = "sha256:" + "b" * 64
SHA_C = "sha256:" + "c" * 64
SHA_D = "sha256:" + "d" * 64
ISSUED_AT = "2026-09-06T23:00:00Z"
STARTED_AT = "2026-09-07T00:00:00Z"
FINISHED_AT = "2026-09-07T00:01:00Z"
EXPIRES_AT = "2027-09-07T00:00:00Z"
NOW = datetime(2026, 9, 7, 0, 0, 30, tzinfo=timezone.utc)

BASE_ENV = {
    "XLH_LIGHTRAG_IMAGE_DIGEST": SHA_A,
    "XLH_LIGHTRAG_WORKING_DIR_ID": "volume-1",
    "XLH_EMBEDDING_ENDPOINT_PROFILE": "openai-compatible-v1",
    "XLH_LIGHTRAG_FIXTURE_SUITE_VERSION": "fixtures-v1",
    "XLH_LEGACY_KNOWLEDGE": "absent",
    "MILVUS_DB_NAME": "lightrag",
    "WORKSPACE": "xiaolanhe_v1",
    "LIGHTRAG_KV_STORAGE": "JsonKVStorage",
    "LIGHTRAG_VECTOR_STORAGE": "MilvusVectorDBStorage",
    "LIGHTRAG_GRAPH_STORAGE": "NetworkXStorage",
    "LIGHTRAG_DOC_STATUS_STORAGE": "JsonDocStatusStorage",
    "EMBEDDING_BINDING": "openai",
    "EMBEDDING_MODEL": "text-embedding-v4",
    "EMBEDDING_DIM": "1024",
    "EMBEDDING_SEND_DIM": "false",
    "EMBEDDING_ASYMMETRIC": "false",
    "EMBEDDING_DOCUMENT_PREFIX": "",
    "EMBEDDING_QUERY_PREFIX": "",
    "MILVUS_INDEX_TYPE": "AUTOINDEX",
    "MILVUS_METRIC_TYPE": "COSINE",
}


def write_canonical(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(MODULE.canonical_bytes(value) + b"\n")
    return path


def raw_digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def valid_writer(
    contract_hash,
    *,
    attempt="attempt-1",
    generation="generation-1",
    operation="bootstrap_empty",
):
    return {
        "schema_version": 2,
        "evidence_type": "xlh.lightrag_writer_fence.v2",
        "attempt_id": attempt,
        "generation": generation,
        "operation": operation,
        "contract_sha256": contract_hash,
        "issued_at": ISSUED_AT,
        "expires_at": EXPIRES_AT,
        "pipeline_idle_observed": True,
        "server_replicas": 0,
        "automatic_restart_disabled": True,
        "observer": "docker-inspector",
        "uncontrolled_writers_attestation": "no_uncontrolled_writers",
    }


def valid_stats(name, count, *, source_total=None, duplicates=0):
    if source_total is None:
        source_total = count + duplicates
    return {
        "label": name,
        "source_total": source_total,
        "prepared": count,
        "rebuilt": count,
        "staged": 0,
        "skipped": 0,
        "duplicates": duplicates,
        "batches": 1 if source_total else 0,
        "failed_batches": 0,
        "errors": [],
    }


def valid_target(name, count):
    ids_hash = MODULE.digest([f"{name}-{offset}" for offset in range(count)])
    return {
        "collection": MODULE.EXPECTED_COLLECTIONS[name],
        "database": "lightrag",
        "dynamic_fields": True,
        "schema_sha256": MODULE.expected_schema_sha256(name),
        "vector_field_type": "FLOAT_VECTOR",
        "vector_dimension": 1024,
        "primary_key_field": "id",
        "index_type": "AUTOINDEX",
        "metric_type": "COSINE",
        "expected_count": count,
        "actual_count": count,
        "expected_ids_sha256": ids_hash,
        "actual_ids_sha256": ids_hash,
    }


def valid_failed_report(contract, contract_hash, *, attempt="attempt-1"):
    report = MODULE.empty_report(
        attempt,
        "generation-1",
        "bootstrap_empty",
        contract,
        contract_hash,
        STARTED_AT,
    )
    report["finished_at"] = FINISHED_AT
    report["errors"] = [
        {
            "stage": "controller",
            "target": "all",
            "code": "unknown",
            "retryable": False,
        }
    ]
    return report


def valid_verified_report(
    contract,
    contract_hash,
    *,
    operation="bootstrap_empty",
    attempt="attempt-1",
    generation="generation-1",
):
    report = MODULE.empty_report(
        attempt,
        generation,
        operation,
        contract,
        contract_hash,
        STARTED_AT,
        state="verified",
        reason="verified",
    )
    report["finished_at"] = FINISHED_AT
    backup_required = operation in {"migration_rebuild", "restore_verify"}
    backup_manifest_hash = SHA_B if backup_required else None
    report["writer_fence"] = {
        "applicable": True,
        "status": "succeeded",
        "evidence_type": "xlh.lightrag_writer_fence.v2",
        "evidence_sha256": SHA_A,
        "issued_at": ISSUED_AT,
        "expires_at": EXPIRES_AT,
        "pipeline_idle_observed": True,
        "server_replicas": 0,
        "automatic_restart_disabled": True,
        "observer": "docker-inspector",
        "uncontrolled_writers_attestation": "no_uncontrolled_writers",
        "backup_manifest_sha256": backup_manifest_hash,
    }
    if backup_required:
        report["backup"] = {
            "applicable": True,
            "status": "succeeded",
            "evidence_type": "xlh.lightrag_backup.v2",
            "evidence_sha256": SHA_C,
            "manifest_sha256": backup_manifest_hash,
            "source_generation": (
                generation if operation == "migration_rebuild" else "source-generation"
            ),
            "configuration_sha256": contract_hash,
            "writer_evidence_sha256": SHA_A,
            "component_count": 4,
        }
    counts = (0, 0, 0) if operation == "bootstrap_empty" else (2, 1, 3)
    source_hash = MODULE.digest({"source": operation})
    report["source"] = {
        "applicable": True,
        "status": "succeeded",
        "before_sha256": source_hash,
        "after_sha256": source_hash,
        "graph_nodes": counts[0],
        "graph_edges_raw": counts[1],
        "relationships_normalized": counts[1],
        "text_chunks": counts[2],
    }
    if operation == "migration_rebuild":
        report["rebuild"] = {
            "applicable": True,
            "status": "succeeded",
            "entities": valid_stats("entities", counts[0]),
            "relationships": valid_stats("relationships", counts[1]),
            "chunks": valid_stats("chunks", counts[2]),
        }
    report["targets"] = {
        "applicable": True,
        "status": "succeeded",
        "entities": valid_target("entities", counts[0]),
        "relationships": valid_target("relationships", counts[1]),
        "chunks": valid_target("chunks", counts[2]),
    }
    report["official_consistency"] = {
        "applicable": True,
        "status": "succeeded",
        "graph_entities": counts[0],
        "graph_relations": counts[1],
        "missing_entities": 0,
        "missing_relations": 0,
        "skipped_nodes": 0,
        "skipped_edges": 0,
    }
    if operation != "bootstrap_empty":
        report["fixtures"] = {
            "applicable": True,
            "status": "succeeded",
            "suite_version": contract["fixture_suite_version"],
            "passed": 2,
            "failed": 0,
            "results_sha256": SHA_D,
        }
    report["checks"] = {name: True for name in MODULE.REQUIRED_CHECKS}
    report["cleanup"] = {
        "applicable": True,
        "status": "succeeded",
        "worker_finalized": True,
        "verifier_finalized": True,
        "report_reread": True,
    }
    report["errors"] = []
    return report


def publish_pair(root, report, *, marker_time="2030-01-01T00:00:00Z"):
    root = pathlib.Path(root)
    report_path = write_canonical(
        root / "reports" / f"{report['attempt_id']}.json", report
    )
    with mock.patch.object(MODULE, "utc_now", return_value=marker_time):
        current = MODULE.marker(
            report["state"],
            report["generation"],
            report["attempt_id"],
            report["operation"],
            report["contract"]["contract_sha256"],
            report["reason_code"],
            f"reports/{report['attempt_id']}.json",
            MODULE.digest(report),
        )
    write_canonical(root / "current.json", current)
    return report_path, current


def write_tar(path, entries):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(path, mode="w") as archive:
        for entry in entries:
            info = tarfile.TarInfo(entry["name"])
            info.type = entry.get("type", tarfile.REGTYPE)
            info.mode = 0o600
            info.mtime = 0
            if info.type in {tarfile.SYMTYPE, tarfile.LNKTYPE}:
                info.linkname = entry.get("linkname", "target")
                archive.addfile(info)
            elif info.type in {tarfile.CHRTYPE, tarfile.BLKTYPE}:
                info.devmajor = 1
                info.devminor = 1
                archive.addfile(info)
            elif info.type == tarfile.FIFOTYPE:
                archive.addfile(info)
            else:
                data = entry.get("data", b"")
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
    return path


def component_from_archive(
    root,
    name,
    contract_hash,
    writer_hash,
    source_generation,
    *,
    entries=None,
    members=None,
):
    if entries is None:
        entries = [
            {"name": f"{name}/a.bin", "data": f"{name}-a".encode()},
            {"name": f"{name}/b.bin", "data": f"{name}-b".encode()},
        ]
    archive_path = pathlib.PurePosixPath("archives") / f"{name}.tar"
    archive = write_tar(pathlib.Path(root) / archive_path, entries)
    if members is None:
        members = [
            {
                "path": entry["name"],
                "size_bytes": len(entry.get("data", b"")),
                "sha256": raw_digest(entry.get("data", b"")),
            }
            for entry in entries
            if entry.get("type", tarfile.REGTYPE) == tarfile.REGTYPE
        ]
        members.sort(key=lambda member: member["path"])
    archive_data = archive.read_bytes()
    return {
        "archive_path": archive_path.as_posix(),
        "size_bytes": len(archive_data),
        "sha256": raw_digest(archive_data),
        "member_count": len(members),
        "members_sha256": MODULE.digest(members),
        "members": members,
        "source_generation": source_generation,
        "contract_sha256": contract_hash,
        "configuration_sha256": contract_hash,
        "writer_evidence_sha256": writer_hash,
    }


def write_backup(
    root,
    contract_hash,
    writer_hash,
    *,
    source_generation="generation-1",
):
    root = pathlib.Path(root)
    components = {
        name: component_from_archive(
            root, name, contract_hash, writer_hash, source_generation
        )
        for name in ("working_directory", "milvus", "etcd", "minio")
    }
    manifest = {
        "schema_version": 2,
        "source_generation": source_generation,
        "contract_sha256": contract_hash,
        "configuration_sha256": contract_hash,
        "writer_evidence_sha256": writer_hash,
        "components": components,
    }
    envelope = {
        "schema_version": 2,
        "evidence_type": "xlh.lightrag_backup.v2",
        "manifest": manifest,
        "manifest_sha256": MODULE.digest(manifest),
    }
    path = write_canonical(root / "backup.json", envelope)
    return path, envelope, MODULE.digest(envelope)


def rewrite_backup(path, envelope):
    envelope["manifest_sha256"] = MODULE.digest(envelope["manifest"])
    write_canonical(path, envelope)


def parse_json_lines(output):
    return [json.loads(line) for line in output.getvalue().splitlines() if line]
    return MODULE.digest(envelope)


def valid_duplicate_policy(
    contract_hash,
    source_hash,
    *,
    attempt="attempt-1",
    generation="generation-1",
    operation="migration_rebuild",
):
    return {
        "schema_version": 2,
        "evidence_type": "xlh.lightrag_duplicate_policy.v2",
        "attempt_id": attempt,
        "generation": generation,
        "operation": operation,
        "contract_sha256": contract_hash,
        "source_sha256": source_hash,
        "reviewer": "reviewer-1",
        "issued_at": ISSUED_AT,
        "expires_at": EXPIRES_AT,
        "max_duplicates": {"entities": 1, "relationships": 2, "chunks": 3},
    }


class FenceTestCase(unittest.TestCase):
    def setUp(self):
        self.environment = mock.patch.dict(os.environ, BASE_ENV, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.contract = MODULE.build_contract()
        self.contract_hash = MODULE.digest(self.contract)


class TransitionObservabilityTests(FenceTestCase):
    def test_transition_event_has_exact_schema_and_rebuild_counts(self):
        report = valid_verified_report(
            self.contract, self.contract_hash, operation="migration_rebuild"
        )
        transition = MODULE.marker(
            "verified",
            report["generation"],
            report["attempt_id"],
            report["operation"],
            self.contract_hash,
            "verified",
            f"reports/{report['attempt_id']}.json",
            MODULE.digest(report),
        )
        event = MODULE.transition_event(transition, "ok", 17, report)

        self.assertEqual(
            set(event),
            {
                "schema_version",
                "event",
                "operation",
                "state",
                "reason_code",
                "outcome",
                "duration_ms",
                "targets",
            },
        )
        self.assertEqual(event["schema_version"], 1)
        self.assertEqual(event["event"], "lightrag.fence.transition")
        self.assertEqual(event["operation"], "migration_rebuild")
        self.assertEqual(event["state"], "verified")
        self.assertEqual(event["reason_code"], "verified")
        self.assertEqual(event["outcome"], "ok")
        self.assertEqual(event["duration_ms"], 17)
        self.assertEqual(set(event["targets"]), set(MODULE.TARGETS))
        for name, expected in zip(MODULE.TARGETS, (2, 1, 3)):
            self.assertEqual(
                set(event["targets"][name]),
                set(MODULE.TRANSITION_COUNT_FIELDS),
            )
            self.assertEqual(event["targets"][name]["source_total"], expected)
            self.assertEqual(event["targets"][name]["prepared"], expected)
            self.assertEqual(event["targets"][name]["rebuilt"], expected)

    def test_transition_event_uses_fixed_zero_counts_when_not_applicable(self):
        transition = MODULE.marker(
            "stale",
            "generation-1",
            "attempt-1",
            "revalidate",
            self.contract_hash,
            "invalidated",
        )

    def test_failed_rebuild_event_preserves_safe_partial_counts(self):
        report = valid_failed_report(self.contract, self.contract_hash)
        report["operation"] = "migration_rebuild"
        report["rebuild"]["applicable"] = True
        report["rebuild"]["status"] = "failed"
        report["rebuild"]["entities"] = valid_stats("entities", 2)
        transition = MODULE.marker(
            "failed",
            report["generation"],
            report["attempt_id"],
            report["operation"],
            self.contract_hash,
            "operation_failed",
            f"reports/{report['attempt_id']}.json",
            MODULE.digest(report),
        )
        event = MODULE.transition_event(transition, "error", 5, report)
        self.assertEqual(event["targets"]["entities"]["source_total"], 2)
        self.assertEqual(event["targets"]["entities"]["rebuilt"], 2)
        self.assertEqual(
            event["targets"]["relationships"],
            {field: 0 for field in MODULE.TRANSITION_COUNT_FIELDS},
        )
        event = MODULE.transition_event(transition, "ok", 0)
        self.assertEqual(
            event["targets"],
            {
                name: {field: 0 for field in MODULE.TRANSITION_COUNT_FIELDS}
                for name in MODULE.TARGETS
            },
        )

    def test_transition_event_rejects_unbounded_vocabulary_and_is_private(self):
        canary = "credential-canary-attempt"
        report = valid_verified_report(
            self.contract,
            self.contract_hash,
            operation="migration_rebuild",
            attempt=canary,
        )
        transition = MODULE.marker(
            "verified",
            report["generation"],
            report["attempt_id"],
            report["operation"],
            self.contract_hash,
            "verified",
            f"reports/{canary}.json",
            MODULE.digest(report),
        )
        encoded = json.dumps(
            MODULE.transition_event(transition, "recovered", 1, report),
            sort_keys=True,
        )
        self.assertNotIn(canary, encoded)
        for forbidden in (
            "attempt_id",
            "generation",
            "sha256",
            "report_path",
            "content",
            "credential",
        ):
            self.assertNotIn(forbidden, encoded)
        with self.assertRaises(ValueError):
            MODULE.transition_event(transition, "unknown", 0, report)
        with self.assertRaises(ValueError):
            MODULE.transition_event(transition, "ok", -1, report)

    def test_publish_failure_emits_no_transition(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            report = valid_verified_report(self.contract, self.contract_hash)
            publish_pair(root, report)
            args = MODULE.parser().parse_args(
                [
                    "invalidate",
                    "--fence-dir",
                    str(root),
                    "--generation",
                    "generation-2",
                    "--attempt-id",
                    "attempt-invalidate",
                ]
            )
            with mock.patch.object(
                MODULE.FenceStore,
                "publish",
                side_effect=OSError("private canary"),
            ), mock.patch.object(MODULE, "emit_transition") as emit:
                with self.assertRaises(OSError):
                    asyncio.run(MODULE.execute(args))
            emit.assert_not_called()
            store = MODULE.FenceStore(root, create=False)
            current = store.read_current()
            self.assertEqual(current["state"], "verified")
            self.assertEqual(current["attempt_id"], report["attempt_id"])

    def test_main_suppresses_plain_error_only_after_failed_marker_publication(self):
        cases = ((False, True), (True, False))
        for failed_marker_published, expect_plain_error in cases:
            with self.subTest(failed_marker_published=failed_marker_published):
                args = SimpleNamespace(
                    command="readiness",
                    attempt_id=None,
                    batch_size=500,
                    fixture_timeout=600,
                    _failed_marker_published=failed_marker_published,
                    _recovered_transition=False,
                )

                async def fail(_args):
                    _args._failed_marker_published = failed_marker_published
                    raise RuntimeError("raw credential canary")

                errors = io.StringIO()
                with mock.patch.object(
                    MODULE, "parser"
                ) as parser, mock.patch.object(
                    MODULE, "_execute_with_signals", side_effect=fail
                ), contextlib.redirect_stderr(errors), self.assertRaises(SystemExit) as raised:
                    parser.return_value.parse_args.return_value = args
                    MODULE.main()

                self.assertEqual(raised.exception.code, 1)
                self.assertEqual(bool(errors.getvalue()), expect_plain_error)
                self.assertNotIn("raw credential canary", errors.getvalue())
                if expect_plain_error:
                    event = json.loads(errors.getvalue())
                    self.assertEqual(
                        event,
                        {
                            "schema_version": 1,
                            "event": "lightrag.fence.failure",
                            "command": "readiness",
                            "outcome": "error",
                            "reason_code": "unknown",
                            "retryable": False,
                        },
                    )

    def test_serving_lock_rejects_symlink_and_nonregular_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            target = root / "target"
            target.write_text("canary", encoding="utf-8")
            (root / "serving.lock").symlink_to(target)
            with self.assertRaises(OSError):
                MODULE.FenceStore(root, create=False).serving_lease(
                    exclusive=False
                ).__enter__()
            self.assertEqual(target.read_text(encoding="utf-8"), "canary")

        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "serving.lock").mkdir()
            with self.assertRaises((OSError, ValueError)):
                MODULE.FenceStore(root, create=False).serving_lease(
                    exclusive=False
                ).__enter__()


class CanonicalJSONTests(FenceTestCase):
    def test_canonical_json_accepts_exact_utf8_and_has_stable_digest(self):
        value = {"z": None, "message": "小蓝盒", "a": [True, 2]}
        with tempfile.TemporaryDirectory() as directory:
            path = write_canonical(pathlib.Path(directory) / "value.json", value)
            self.assertEqual(MODULE.read_canonical_json(path, "value"), value)
        self.assertEqual(MODULE.digest(value), MODULE.digest(copy.deepcopy(value)))
        self.assertNotIn(b" ", MODULE.canonical_bytes(value))

    def test_canonical_json_rejects_noncanonical_or_nonobject_bytes(self):
        cases = {
            "unsorted": b'{"z":1,"a":2}\n',
            "whitespace": b'{"a": 2}\n',
            "missing-newline": b'{"a":2}',
            "extra-newline": b'{"a":2}\n\n',
            "duplicate-key": b'{"a":1,"a":1}\n',
            "array-root": b'[]\n',
            "invalid-utf8": b'{"a":"\xff"}\n',
            "nan": b'{"a":NaN}\n',
            "empty": b"",
        }
        with tempfile.TemporaryDirectory() as directory:
            for name, data in cases.items():
                with self.subTest(name=name):
                    path = pathlib.Path(directory) / f"{name}.json"
                    path.write_bytes(data)
                    with self.assertRaises((OSError, ValueError)):
                        MODULE.read_canonical_json(path, name)

    def test_regular_file_reader_rejects_symlink_directory_and_size_limit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            target = write_canonical(root / "target.json", {"a": 1})
            link = root / "link.json"
            link.symlink_to(target)
            for name, path in (("symlink", link), ("directory", root)):
                with self.subTest(name=name):
                    with self.assertRaises((OSError, ValueError)):
                        MODULE.read_regular(path)
            oversized = root / "oversized"
            oversized.write_bytes(b"12345")
            with self.assertRaises(ValueError):
                MODULE.read_regular(oversized, maximum=4)


class MarkerAndReportTests(FenceTestCase):
    def test_python_schema_digests_match_go_readiness_goldens(self):
        self.assertEqual(
            {name: MODULE.expected_schema_sha256(name) for name in MODULE.TARGETS},
            {
                "entities": "sha256:c0aba0e7cbb6b0792646c9d7f0ce0a1f12eafe7d5cf1b83e73637f07c729bb23",
                "relationships": "sha256:76e276011ed87bf863ef57efed356a882839fc46abd24aff515ef92e26323724",
                "chunks": "sha256:4c9aebff6589f3e1f28faa176f79a162dfea9aa65e862037faa449b781d2a7c5",
            },
        )

    def test_marker_enforces_exact_schema_and_terminal_report_identity(self):
        with mock.patch.object(MODULE, "utc_now", return_value=FINISHED_AT):
            terminal = MODULE.marker(
                "verified",
                "generation-1",
                "attempt-1",
                "bootstrap_empty",
                self.contract_hash,
                "verified",
                "reports/attempt-1.json",
                SHA_A,
            )
            nonterminal = MODULE.marker(
                "rebuilding",
                "generation-1",
                "attempt-1",
                "migration_rebuild",
                self.contract_hash,
                "in_progress",
            )
        self.assertEqual(set(terminal), MODULE.MARKER_KEYS)
        self.assertIsNone(nonterminal["report_path"])

        candidates = {}
        extra = dict(terminal)
        extra["extra"] = True
        candidates["unknown-key"] = extra
        missing = dict(terminal)
        missing.pop("updated_at")
        candidates["missing-key"] = missing
        for name, value in (
            ("wrong-report-directory", "other/attempt-1.json"),
            ("traversal", "reports/../attempt-1.json"),
            ("absolute", "/reports/attempt-1.json"),
            ("wrong-attempt", "reports/attempt-2.json"),
        ):
            candidate = dict(terminal)
            candidate["report_path"] = value
            candidates[name] = candidate
        bad_reason = dict(terminal)
        bad_reason["reason_code"] = "operation_failed"
        candidates["wrong-reason"] = bad_reason
        bad_operation = dict(terminal)
        bad_operation["operation"] = "caller_selected"
        candidates["unknown-operation"] = bad_operation
        populated_nonterminal = dict(nonterminal)
        populated_nonterminal["report_path"] = "reports/attempt-1.json"
        populated_nonterminal["report_sha256"] = SHA_A
        candidates["nonterminal-report"] = populated_nonterminal

        for name, candidate in candidates.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                MODULE.validate_marker(candidate)

    def test_verified_report_accepts_every_fixed_operation(self):
        for operation in (
            "migration_rebuild",
            "bootstrap_empty",
            "restore_verify",
            "revalidate",
        ):
            with self.subTest(operation=operation):
                report = valid_verified_report(
                    self.contract, self.contract_hash, operation=operation
                )
                self.assertIs(MODULE.validate_report(report, verified=True), report)
                self.assertEqual(set(report), MODULE.REPORT_KEYS)

    def test_verified_report_rejects_schema_and_semantic_mutations(self):
        baseline = valid_verified_report(self.contract, self.contract_hash)
        candidates = {}

        candidate = copy.deepcopy(baseline)
        candidate["unknown"] = None
        candidates["unknown-top-level"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate.pop("cleanup")
        candidates["missing-top-level"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["writer_fence"]["unknown"] = None
        candidates["unknown-nested"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["checks"]["fixtures_valid"] = False
        candidates["false-check"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["errors"] = [
            {"stage": "x", "target": "all", "code": "x", "retryable": False}
        ]
        candidates["verified-error"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["cleanup"]["report_reread"] = False
        candidates["cleanup-incomplete"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["source"]["after_sha256"] = SHA_D
        candidates["source-changed"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["targets"]["chunks"]["actual_ids_sha256"] = SHA_D
        candidates["target-id-digest"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["targets"]["entities"]["collection"] = "wrong"
        candidates["target-identity"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["targets"]["entities"]["schema_sha256"] = SHA_D
        candidates["target-schema-digest"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["fixtures"]["applicable"] = True
        candidate["fixtures"]["status"] = "succeeded"
        candidate["fixtures"]["suite_version"] = "fixtures-v1"
        candidate["fixtures"]["passed"] = 1
        candidate["fixtures"]["results_sha256"] = SHA_A
        candidates["bootstrap-fixtures"] = candidate

        for name, candidate in candidates.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                MODULE.validate_report(candidate, verified=True)

    def test_failed_report_requires_bounded_error_and_failed_semantics(self):
        report = valid_failed_report(self.contract, self.contract_hash)
        self.assertIs(MODULE.validate_report(report), report)

        no_error = copy.deepcopy(report)
        no_error["errors"] = []
        all_checks_true = copy.deepcopy(report)
        all_checks_true["checks"] = {name: True for name in MODULE.REQUIRED_CHECKS}
        succeeded_cleanup = copy.deepcopy(report)
        succeeded_cleanup["cleanup"] = {
            "applicable": True,
            "status": "succeeded",
            "worker_finalized": True,
            "verifier_finalized": True,
            "report_reread": True,
        }
        unknown_error_code = copy.deepcopy(report)
        unknown_error_code["errors"][0]["code"] = "private-provider-canary"
        for name, candidate in (
            ("missing-error", no_error),
            ("true-checks", all_checks_true),
            ("successful-cleanup", succeeded_cleanup),
            ("unknown-error-code", unknown_error_code),
        ):
            with self.subTest(name=name), self.assertRaises(ValueError):
                MODULE.validate_report(candidate)


class ReadinessTests(FenceTestCase):
    def test_readiness_accepts_canonical_verified_identity_and_digest(self):
        report = valid_verified_report(self.contract, self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            publish_pair(directory, report)
            MODULE.FenceStore(pathlib.Path(directory), create=False).verify_current(
                "generation-1", self.contract_hash
            )

    def test_readiness_rejects_marker_contract_generation_state_and_path(self):
        report = valid_verified_report(self.contract, self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            _, current = publish_pair(root, report)
            store = MODULE.FenceStore(root, create=False)
            for name, generation, contract_hash in (
                ("wrong-generation", "generation-2", self.contract_hash),
                ("wrong-contract", "generation-1", SHA_D),
            ):
                with self.subTest(name=name), self.assertRaises(ValueError):
                    store.verify_current(generation, contract_hash)

            stale = dict(current)
            stale.update(
                state="stale",
                reason_code="invalidated",
                report_path=None,
                report_sha256=None,
            )
            write_canonical(root / "current.json", stale)
            with self.assertRaises(ValueError):
                store.verify_current("generation-1", self.contract_hash)

            escaped = dict(current)
            escaped["report_path"] = "reports/../attempt-1.json"
            write_canonical(root / "current.json", escaped)
            with self.assertRaises(ValueError):
                store.verify_current("generation-1", self.contract_hash)

    def test_readiness_rejects_noncanonical_missing_or_tampered_files(self):
        report = valid_verified_report(self.contract, self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            report_path, current = publish_pair(root, report)
            store = MODULE.FenceStore(root, create=False)

            (root / "current.json").write_text(json.dumps(current), encoding="utf-8")
            with self.assertRaises(ValueError):
                store.verify_current("generation-1", self.contract_hash)

            write_canonical(root / "current.json", current)
            report_path.write_text(json.dumps(report), encoding="utf-8")
            with self.assertRaises(ValueError):
                store.verify_current("generation-1", self.contract_hash)

            write_canonical(report_path, report)
            report_path.unlink()
            with self.assertRaises(FileNotFoundError):
                store.verify_current("generation-1", self.contract_hash)

    def test_readiness_rejects_report_digest_identity_time_and_checks(self):
        baseline = valid_verified_report(self.contract, self.contract_hash)
        cases = []

        def digest_mismatch(root, report, current):
            current["report_sha256"] = SHA_D

        cases.append(("digest", digest_mismatch))

        def identity_mismatch(root, report, current):
            report["generation"] = "generation-2"
            write_canonical(root / current["report_path"], report)
            current["report_sha256"] = MODULE.digest(report)

        cases.append(("identity", identity_mismatch))

        def marker_predates(root, report, current):
            current["updated_at"] = "2026-09-06T00:00:00Z"

        cases.append(("marker-time", marker_predates))

        def false_check(root, report, current):
            report["checks"]["schema_valid"] = False
            write_canonical(root / current["report_path"], report)
            current["report_sha256"] = MODULE.digest(report)

        cases.append(("false-check", false_check))

        def nested_contract(root, report, current):
            report["contract"]["embedding_contract"]["endpoint_semantics"] = "changed"
            write_canonical(root / current["report_path"], report)
            current["report_sha256"] = MODULE.digest(report)

        cases.append(("nested-contract", nested_contract))

        for name, mutate in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                report = copy.deepcopy(baseline)
                _, current = publish_pair(root, report)
                mutate(root, report, current)
                write_canonical(root / "current.json", current)
                with self.assertRaises(ValueError):
                    MODULE.FenceStore(root, create=False).verify_current(
                        "generation-1", self.contract_hash
                    )


class ServingLeaseTests(FenceTestCase):
    def test_shared_serving_lease_blocks_controller_then_releases_for_transition(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            report = valid_verified_report(self.contract, self.contract_hash)
            publish_pair(root, report)
            store = MODULE.FenceStore(root)
            args = MODULE.parser().parse_args(
                [
                    "invalidate",
                    "--fence-dir",
                    str(root),
                    "--generation",
                    "generation-2",
                    "--attempt-id",
                    "attempt-invalidate",
                ]
            )

            with store.serving_lease(exclusive=False):
                with self.assertRaises(MODULE.ClassifiedFailure) as raised:
                    asyncio.run(MODULE.execute(args))
                self.assertEqual(raised.exception.code, "serving_lock_busy")
                self.assertTrue(raised.exception.retryable)
                self.assertEqual(store.read_current()["state"], "verified")
                # Lifecycle admission fails before taking the controller mutex.
                with MODULE.FenceStore(root):
                    pass

            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(asyncio.run(MODULE.execute(args)), 0)
            self.assertEqual(store.read_current()["state"], "stale")

    def test_controller_lock_failures_have_fixed_private_classification(self):
        private = type("PrivateCredentialCanary", (RuntimeError,), {})
        cases = (
            (
                MODULE.ClassifiedFailure("serving_lock_busy", retryable=True),
                "serving_lock_busy",
            ),
            (
                MODULE.ClassifiedFailure("controller_lock_busy", retryable=True),
                "controller_lock_busy",
            ),
            (private("raw secret path canary"), "unknown"),
        )
        for error, expected in cases:
            with self.subTest(expected=expected):
                bounded = MODULE._bounded_error(error)
                self.assertEqual(bounded["code"], expected)
                encoded = json.dumps(bounded)
                self.assertNotIn("PrivateCredentialCanary", encoded)
                self.assertNotIn("raw secret path canary", encoded)


class WriterEvidenceTests(FenceTestCase):
    def _load(self, path, value, expected_hash=None, **overrides):
        return MODULE.load_writer_evidence(
            str(path),
            expected_hash or MODULE.digest(value),
            overrides.get("attempt", "attempt-1"),
            overrides.get("generation", "generation-1"),
            overrides.get("operation", "bootstrap_empty"),
            overrides.get("contract_hash", self.contract_hash),
            now=overrides.get("now", NOW),
        )

    def test_writer_evidence_accepts_exact_canonical_bound_artifact(self):
        value = valid_writer(self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            path = write_canonical(pathlib.Path(directory) / "writer.json", value)
            self.assertEqual(self._load(path, value), value)

    def test_writer_evidence_rejects_digest_identity_and_unknown_keys(self):
        baseline = valid_writer(self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "writer.json"
            write_canonical(path, baseline)
            with self.assertRaises(ValueError):
                self._load(path, baseline, expected_hash=SHA_D)

            for field, value in (
                ("attempt_id", "attempt-2"),
                ("generation", "generation-2"),
                ("operation", "revalidate"),
                ("contract_sha256", SHA_D),
            ):
                with self.subTest(field=field):
                    candidate = copy.deepcopy(baseline)
                    candidate[field] = value
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        self._load(path, candidate)

            candidate = copy.deepcopy(baseline)
            candidate["extra"] = True
            write_canonical(path, candidate)
            with self.assertRaises(ValueError):
                self._load(path, candidate)

    def test_writer_evidence_enforces_issue_and_expiry_boundaries(self):
        baseline = valid_writer(self.contract_hash)
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "writer.json"
            write_canonical(path, baseline)
            at_issue = datetime(2026, 9, 6, 23, 0, tzinfo=timezone.utc)
            self.assertEqual(self._load(path, baseline, now=at_issue), baseline)
            at_expiry = datetime(2027, 9, 7, 0, 0, tzinfo=timezone.utc)
            with self.assertRaises(ValueError):
                self._load(path, baseline, now=at_expiry)

            for name, issued, expires in (
                ("future", "2026-09-08T00:00:00Z", EXPIRES_AT),
                ("reversed", EXPIRES_AT, ISSUED_AT),
                ("equal", FINISHED_AT, FINISHED_AT),
            ):
                with self.subTest(name=name):
                    candidate = copy.deepcopy(baseline)
                    candidate["issued_at"] = issued
                    candidate["expires_at"] = expires
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        self._load(path, candidate)

    def test_writer_evidence_requires_all_quiescence_observations(self):
        baseline = valid_writer(self.contract_hash)
        cases = (
            ("pipeline_idle_observed", False),
            ("server_replicas", 1),
            ("server_replicas", False),
            ("automatic_restart_disabled", False),
            ("uncontrolled_writers_attestation", "self_attested"),
            ("observer", "invalid observer"),
        )
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "writer.json"
            for field, value in cases:
                with self.subTest(field=field, value=value):
                    candidate = copy.deepcopy(baseline)
                    candidate[field] = value
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        self._load(path, candidate)

    def test_preflight_has_no_environment_writer_self_attestation_fallback(self):
        args = MODULE.parser().parse_args(
            [
                "bootstrap",
                "--fence-dir",
                "unused",
                "--generation",
                "generation-1",
                "--attempt-id",
                "attempt-1",
            ]
        )
        with mock.patch.dict(
            os.environ, {"XLH_LIGHTRAG_WRITERS_STOPPED": "true"}
        ), self.assertRaises(ValueError):
            MODULE._preflight_args(args, "bootstrap_empty", self.contract_hash)


class BackupEvidenceTests(FenceTestCase):
    def test_backup_accepts_exact_four_component_tar_consistency_unit(self):
        with tempfile.TemporaryDirectory() as directory:
            path, envelope, evidence_hash = write_backup(
                directory, self.contract_hash, SHA_A
            )
            loaded, section = MODULE.load_backup(
                str(path),
                evidence_hash,
                self.contract_hash,
                "generation-1",
                SHA_A,
            )
            self.assertEqual(loaded, envelope)
            self.assertEqual(section["component_count"], 4)
            self.assertEqual(section["manifest_sha256"], envelope["manifest_sha256"])

    def test_backup_rejects_missing_extra_and_mixed_components(self):
        mutations = {}

        def missing(envelope):
            envelope["manifest"]["components"].pop("minio")

        mutations["missing"] = missing

        def extra(envelope):
            envelope["manifest"]["components"]["redis"] = copy.deepcopy(
                envelope["manifest"]["components"]["etcd"]
            )

        mutations["extra"] = extra

        def mixed_generation(envelope):
            envelope["manifest"]["components"]["milvus"][
                "source_generation"
            ] = "generation-2"

        mutations["mixed-generation"] = mixed_generation

        def mixed_contract(envelope):
            envelope["manifest"]["components"]["etcd"][
                "contract_sha256"
            ] = SHA_D

        mutations["mixed-contract"] = mixed_contract

        def mixed_configuration(envelope):
            envelope["manifest"]["components"]["minio"][
                "configuration_sha256"
            ] = SHA_D

        mutations["mixed-configuration"] = mixed_configuration

        def mixed_writer(envelope):
            envelope["manifest"]["components"]["working_directory"][
                "writer_evidence_sha256"
            ] = SHA_D

        mutations["mixed-writer"] = mixed_writer

        for name, mutate in mutations.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                path, envelope, _ = write_backup(
                    directory, self.contract_hash, SHA_A
                )
                mutate(envelope)
                evidence_hash = rewrite_backup(path, envelope)
                with self.assertRaises(ValueError):
                    MODULE.load_backup(
                        str(path),
                        evidence_hash,
                        self.contract_hash,
                        "generation-1",
                        SHA_A,
                    )

    def test_backup_rejects_manifest_binding_and_envelope_tampering(self):
        fields = (
            ("source_generation", "generation-2"),
            ("contract_sha256", SHA_D),
            ("configuration_sha256", SHA_D),
            ("writer_evidence_sha256", SHA_D),
        )
        for field, value in fields:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                path, envelope, _ = write_backup(
                    directory, self.contract_hash, SHA_A
                )
                envelope["manifest"][field] = value
                evidence_hash = rewrite_backup(path, envelope)
                with self.assertRaises(ValueError):
                    MODULE.load_backup(
                        str(path),
                        evidence_hash,
                        self.contract_hash,
                        "generation-1",
                        SHA_A,
                    )

        with tempfile.TemporaryDirectory() as directory:
            path, envelope, evidence_hash = write_backup(
                directory, self.contract_hash, SHA_A
            )
            envelope["manifest_sha256"] = SHA_D
            write_canonical(path, envelope)
            with self.assertRaises(ValueError):
                MODULE.load_backup(
                    str(path),
                    MODULE.digest(envelope),
                    self.contract_hash,
                    "generation-1",
                    SHA_A,
                )
            with self.assertRaises(ValueError):
                MODULE.load_backup(
                    str(path), evidence_hash, self.contract_hash, "generation-1", SHA_A
                )

    def test_backup_rejects_missing_tampered_and_malformed_archives(self):
        for name in ("missing", "tampered", "malformed"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                path, envelope, _ = write_backup(root, self.contract_hash, SHA_A)
                component = envelope["manifest"]["components"]["milvus"]
                archive = root / component["archive_path"]
                if name == "missing":
                    archive.unlink()
                elif name == "tampered":
                    with archive.open("ab") as output:
                        output.write(b"tamper")
                else:
                    archive.write_bytes(b"not a tar archive")
                    data = archive.read_bytes()
                    component["size_bytes"] = len(data)
                    component["sha256"] = raw_digest(data)
                    component["members"] = []
                    component["member_count"] = 0
                    component["members_sha256"] = MODULE.digest([])
                evidence_hash = rewrite_backup(path, envelope)
                with self.assertRaises(ValueError):
                    MODULE.load_backup(
                        str(path),
                        evidence_hash,
                        self.contract_hash,
                        "generation-1",
                        SHA_A,
                    )

    def test_backup_rejects_member_manifest_mismatches(self):
        def remove_declared(component):
            component["members"].pop()

        def add_missing(component):
            component["members"].append(
                {"path": "milvus/z.bin", "size_bytes": 1, "sha256": SHA_D}
            )

        def duplicate_declared(component):
            component["members"].append(copy.deepcopy(component["members"][0]))

        def unsorted(component):
            component["members"].reverse()

        def wrong_member_hash(component):
            component["members"][0]["sha256"] = SHA_D

        def wrong_member_size(component):
            component["members"][0]["size_bytes"] += 1

        mutations = (
            ("undeclared-archive-member", remove_declared),
            ("missing-archive-member", add_missing),
            ("duplicate-declaration", duplicate_declared),
            ("unsorted-declaration", unsorted),
            ("member-hash", wrong_member_hash),
            ("member-size", wrong_member_size),
        )
        for name, mutate in mutations:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                path, envelope, _ = write_backup(
                    directory, self.contract_hash, SHA_A
                )
                component = envelope["manifest"]["components"]["milvus"]
                mutate(component)
                component["member_count"] = len(component["members"])
                component["members_sha256"] = MODULE.digest(component["members"])
                evidence_hash = rewrite_backup(path, envelope)
                with self.assertRaises(ValueError):
                    MODULE.load_backup(
                        str(path),
                        evidence_hash,
                        self.contract_hash,
                        "generation-1",
                        SHA_A,
                    )

    def test_backup_rejects_unsafe_tar_paths_and_member_types(self):
        unsafe_entries = (
            ("absolute", [{"name": "/absolute.bin", "data": b"x"}]),
            ("parent", [{"name": "../parent.bin", "data": b"x"}]),
            ("backslash", [{"name": "bad\\name.bin", "data": b"x"}]),
            ("symlink", [{"name": "link", "type": tarfile.SYMTYPE}]),
            ("hardlink", [{"name": "link", "type": tarfile.LNKTYPE}]),
            ("fifo", [{"name": "fifo", "type": tarfile.FIFOTYPE}]),
            ("device", [{"name": "device", "type": tarfile.CHRTYPE}]),
            (
                "duplicate-normalized",
                [
                    {"name": "./same.bin", "data": b"x"},
                    {"name": "same.bin", "data": b"x"},
                ],
            ),
        )
        for name, entries in unsafe_entries:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                path, envelope, _ = write_backup(root, self.contract_hash, SHA_A)
                component = component_from_archive(
                    root,
                    "milvus",
                    self.contract_hash,
                    SHA_A,
                    "generation-1",
                    entries=entries,
                    members=(
                        [
                            {
                                "path": "same.bin",
                                "size_bytes": 1,
                                "sha256": raw_digest(b"x"),
                            }
                        ]
                        if name == "duplicate-normalized"
                        else []
                    ),
                )
                envelope["manifest"]["components"]["milvus"] = component
                evidence_hash = rewrite_backup(path, envelope)
                with self.assertRaises(ValueError):
                    MODULE.load_backup(
                        str(path),
                        evidence_hash,
                        self.contract_hash,
                        "generation-1",
                        SHA_A,
                    )

    def test_backup_rejects_unsafe_archive_path(self):
        with tempfile.TemporaryDirectory() as directory:
            path, envelope, _ = write_backup(directory, self.contract_hash, SHA_A)
            envelope["manifest"]["components"]["minio"]["archive_path"] = (
                "../minio.tar"
            )
            evidence_hash = rewrite_backup(path, envelope)
            with self.assertRaises(ValueError):
                MODULE.load_backup(
                    str(path),
                    evidence_hash,
                    self.contract_hash,
                    "generation-1",
                    SHA_A,
                )

    def test_restore_preflight_rejects_equal_source_and_target_generations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            writer = valid_writer(
                self.contract_hash, operation="restore_verify"
            )
            writer_path = write_canonical(root / "writer.json", writer)
            writer_hash = MODULE.digest(writer)
            backup_path, _, backup_hash = write_backup(
                root, self.contract_hash, writer_hash
            )
            args = MODULE.parser().parse_args(
                [
                    "restore-verify",
                    "--fence-dir",
                    str(root / "fence"),
                    "--generation",
                    "generation-1",
                    "--attempt-id",
                    "attempt-1",
                    "--writer-evidence",
                    str(writer_path),
                    "--writer-evidence-sha256",
                    writer_hash,
                    "--backup-report",
                    str(backup_path),
                    "--backup-report-sha256",
                    backup_hash,
                    "--expected-source-generation",
                    "generation-1",
                ]
            )
            with self.assertRaises(ValueError):
                MODULE._preflight_args(args, "restore_verify", self.contract_hash)

    def test_backup_writer_evidence_is_separate_and_exactly_bound(self):
        baseline = valid_writer(
            self.contract_hash,
            attempt="backup-attempt",
            generation="generation-1",
            operation="backup",
        )
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "backup-writer.json"
            write_canonical(path, baseline)
            self.assertEqual(
                MODULE.load_backup_writer_evidence(
                    str(path),
                    MODULE.digest(baseline),
                    "generation-1",
                    self.contract_hash,
                ),
                baseline,
            )
            for field, value in (
                ("generation", "generation-2"),
                ("operation", "migration_rebuild"),
                ("contract_sha256", SHA_D),
                ("server_replicas", 1),
            ):
                with self.subTest(field=field):
                    candidate = copy.deepcopy(baseline)
                    candidate[field] = value
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        MODULE.load_backup_writer_evidence(
                            str(path),
                            MODULE.digest(candidate),
                            "generation-1",
                            self.contract_hash,
                        )

    def test_preflight_requires_backup_writer_artifact_for_distinct_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            writer = valid_writer(
                self.contract_hash, operation="migration_rebuild"
            )
            writer_path = write_canonical(root / "writer.json", writer)
            writer_hash = MODULE.digest(writer)
            backup_path, _, backup_hash = write_backup(
                root, self.contract_hash, SHA_D
            )
            args = MODULE.parser().parse_args(
                [
                    "rebuild",
                    "--fence-dir",
                    str(root / "fence"),
                    "--generation",
                    "generation-1",
                    "--attempt-id",
                    "attempt-1",
                    "--writer-evidence",
                    str(writer_path),
                    "--writer-evidence-sha256",
                    writer_hash,
                    "--backup-report",
                    str(backup_path),
                    "--backup-report-sha256",
                    backup_hash,
                    "--backup-writer-evidence-sha256",
                    SHA_D,
                ]
            )
            with self.assertRaises(ValueError):
                MODULE._preflight_args(args, "migration_rebuild", self.contract_hash)


class StatsAndDuplicatePolicyTests(FenceTestCase):
    def test_stats_accept_complete_results_and_reviewed_duplicates(self):
        MODULE.validate_stats("entities", valid_stats("entities", 3))
        MODULE.validate_stats(
            "entities",
            valid_stats("entities", 2, source_total=3, duplicates=1),
            duplicate_maximum=1,
        )

    def test_failed_stats_redacts_upstream_error_types_to_fixed_vocabulary(self):
        canary = "provider-password-and-path-canary"
        stats = valid_stats("entities", 0)
        stats["errors"] = [{"error_type": canary}, type(canary, (), {})()]
        failed = MODULE._failed_stats("entities", stats)
        self.assertEqual(
            [error["code"] for error in failed["errors"]],
            ["unknown", "unknown"],
        )
        self.assertNotIn(canary, json.dumps(failed))

    def test_stats_reject_exact_shape_types_errors_and_bad_predicates(self):
        baseline = valid_stats("entities", 3)
        candidates = {}
        candidate = copy.deepcopy(baseline)
        candidate.pop("batches")
        candidates["missing-key"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["extra"] = 0
        candidates["extra-key"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["label"] = "chunks"
        candidates["wrong-label"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["source_total"] = True
        candidates["boolean-count"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["prepared"] = -1
        candidates["negative-count"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["errors"] = "none"
        candidates["non-list-errors"] = candidate
        candidate = copy.deepcopy(baseline)
        candidate["errors"] = [
            {"stage": "rebuild", "target": "entities", "code": "x", "retryable": False}
        ]
        candidates["reported-error"] = candidate
        for field, value in (
            ("rebuilt", 2),
            ("staged", 1),
            ("skipped", 1),
            ("failed_batches", 1),
            ("duplicates", 1),
        ):
            candidate = copy.deepcopy(baseline)
            candidate[field] = value
            candidates[field] = candidate

        for name, candidate in candidates.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                MODULE.validate_stats("entities", candidate)

    def test_duplicate_policy_defaults_to_zero_and_accepts_exact_review(self):
        maxima, section = MODULE.load_duplicate_policy(
            None,
            None,
            "attempt-1",
            "generation-1",
            "migration_rebuild",
            self.contract_hash,
            SHA_B,
            now=NOW,
        )
        self.assertEqual(maxima, {name: 0 for name in MODULE.TARGETS})
        self.assertFalse(section["applicable"])

        policy = valid_duplicate_policy(self.contract_hash, SHA_B)
        with tempfile.TemporaryDirectory() as directory:
            path = write_canonical(pathlib.Path(directory) / "duplicates.json", policy)
            maxima, section = MODULE.load_duplicate_policy(
                str(path),
                MODULE.digest(policy),
                "attempt-1",
                "generation-1",
                "migration_rebuild",
                self.contract_hash,
                SHA_B,
                now=NOW,
            )
        self.assertEqual(maxima, policy["max_duplicates"])
        self.assertTrue(section["applicable"])

    def test_duplicate_policy_rejects_partial_identity_expiry_and_bad_maxima(self):
        policy = valid_duplicate_policy(self.contract_hash, SHA_B)
        with self.assertRaises(ValueError):
            MODULE.load_duplicate_policy(
                "policy.json",
                None,
                "attempt-1",
                "generation-1",
                "migration_rebuild",
                self.contract_hash,
                SHA_B,
                now=NOW,
            )
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "duplicates.json"
            cases = (
                ("attempt_id", "attempt-2"),
                ("generation", "generation-2"),
                ("operation", "revalidate"),
                ("contract_sha256", SHA_D),
                ("source_sha256", SHA_D),
                ("reviewer", "bad reviewer"),
                ("expires_at", ISSUED_AT),
            )
            for field, value in cases:
                with self.subTest(field=field):
                    candidate = copy.deepcopy(policy)
                    candidate[field] = value
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        MODULE.load_duplicate_policy(
                            str(path),
                            MODULE.digest(candidate),
                            "attempt-1",
                            "generation-1",
                            "migration_rebuild",
                            self.contract_hash,
                            SHA_B,
                            now=NOW,
                        )

            for value in (True, -1):
                with self.subTest(maximum=value):
                    candidate = copy.deepcopy(policy)
                    candidate["max_duplicates"]["entities"] = value
                    write_canonical(path, candidate)
                    with self.assertRaises(ValueError):
                        MODULE.load_duplicate_policy(
                            str(path),
                            MODULE.digest(candidate),
                            "attempt-1",
                            "generation-1",
                            "migration_rebuild",
                            self.contract_hash,
                            SHA_B,
                            now=NOW,
                        )

            candidate = copy.deepcopy(policy)
            candidate["max_duplicates"].pop("chunks")
            write_canonical(path, candidate)
            with self.assertRaises(ValueError):
                MODULE.load_duplicate_policy(
                    str(path),
                    MODULE.digest(candidate),
                    "attempt-1",
                    "generation-1",
                    "migration_rebuild",
                    self.contract_hash,
                    SHA_B,
                    now=NOW,
                )


class TransitionAndParserTests(FenceTestCase):
    def test_transition_matrix_is_exact_and_unknown_states_fail(self):
        states = ("absent", "stale", "rebuilding", "failed", "verified")
        for previous in states:
            for following in states:
                with self.subTest(previous=previous, following=following):
                    if (previous, following) in MODULE.ALLOWED_TRANSITIONS:
                        MODULE.assert_transition(previous, following)
                    else:
                        with self.assertRaises(ValueError):
                            MODULE.assert_transition(previous, following)
        for transition in (("unknown", "failed"), ("failed", "unknown")):
            with self.assertRaises(ValueError):
                MODULE.assert_transition(*transition)

    def test_commands_have_fixed_operations_and_no_operation_override(self):
        self.assertEqual(
            MODULE.OPERATIONS,
            {
                "rebuild": "migration_rebuild",
                "bootstrap": "bootstrap_empty",
                "prepare-restore": "restore_verify",
                "restore-verify": "restore_verify",
                "revalidate": "revalidate",
                "invalidate": "revalidate",
            },
        )
        for command in MODULE.OPERATIONS:
            self.assertEqual(MODULE.parser().parse_args([command]).command, command)
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            MODULE.parser().parse_args(
                ["restore-verify", "--operation", "migration_rebuild"]
            )

    def test_parser_preserves_fixture_command_remainder(self):
        args = MODULE.parser().parse_args(
            [
                "revalidate",
                "--fixture-command",
                "python3",
                "fixture.py",
                "--mode",
                "all",
            ]
        )
        self.assertEqual(
            args.fixture_command, ["python3", "fixture.py", "--mode", "all"]
        )

    def test_main_requires_attempt_and_enforces_numeric_bounds(self):
        cases = (
            (["rebuild"], "--attempt-id is required"),
            (["bootstrap", "--attempt-id", "a", "--batch-size", "0"], None),
            (["bootstrap", "--attempt-id", "a", "--batch-size", "5001"], None),
            (["bootstrap", "--attempt-id", "a", "--fixture-timeout", "0"], None),
            (["bootstrap", "--attempt-id", "a", "--fixture-timeout", "3601"], None),
        )
        for argv, expected in cases:
            with self.subTest(argv=argv), mock.patch.object(
                sys, "argv", [str(MODULE_PATH), *argv]
            ), self.assertRaises(SystemExit) as raised:
                MODULE.main()
            if expected is not None:
                self.assertEqual(str(raised.exception), expected)


class SignalAndFixtureTests(FenceTestCase):
    def test_signal_wrapper_installs_both_handlers_and_cancels_once(self):
        async def exercise(trigger):
            loop = asyncio.get_running_loop()
            handlers = {}
            removed = []
            entered = asyncio.Event()
            cancellation_counts = []

            async def fake_execute(_args):
                entered.set()
                try:
                    await asyncio.Event().wait()
                except asyncio.CancelledError:
                    task = asyncio.current_task()
                    cancellation_counts.append(
                        task.cancelling() if hasattr(task, "cancelling") else 1
                    )
                    raise

            def add_handler(signum, callback):
                handlers[signum] = callback

            def remove_handler(signum):
                removed.append(signum)
                return True

            with mock.patch.object(MODULE, "execute", new=fake_execute), mock.patch.object(
                loop, "add_signal_handler", side_effect=add_handler
            ), mock.patch.object(
                loop, "remove_signal_handler", side_effect=remove_handler
            ):
                wrapper = asyncio.create_task(
                    MODULE._execute_with_signals(SimpleNamespace())
                )
                await entered.wait()
                self.assertEqual(set(handlers), {MODULE.signal.SIGTERM, MODULE.signal.SIGINT})
                handlers[trigger]()
                handlers[trigger]()
                with self.assertRaisesRegex(RuntimeError, "^operation cancelled$"):
                    await wrapper

            self.assertEqual(cancellation_counts, [1])
            self.assertEqual(removed, [MODULE.signal.SIGTERM, MODULE.signal.SIGINT])

        for signum in (MODULE.signal.SIGTERM, MODULE.signal.SIGINT):
            with self.subTest(signum=signum):
                asyncio.run(exercise(signum))

    def _fixture_args(self, root, *, timeout=600):
        return MODULE.parser().parse_args(
            [
                "revalidate",
                "--generation",
                "generation-1",
                "--fixture-report",
                str(root / "fixture.json"),
                "--fixture-timeout",
                str(timeout),
                "--fixture-command",
                "fixture-program",
                "--all",
            ]
        )

    def test_async_fixture_success_preserves_argument_and_environment_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._fixture_args(root)
            source_hash = SHA_B
            evidence = {
                "schema_version": 2,
                "evidence_type": "xlh.lightrag_fixture.v2",
                "attempt_id": "attempt-1",
                "generation": "generation-1",
                "operation": "revalidate",
                "contract_sha256": self.contract_hash,
                "source_sha256": source_hash,
                "suite_version": "fixtures-v1",
                "passed": 3,
                "failed": 0,
                "results_sha256": SHA_C,
            }
            write_canonical(root / "fixture.json", evidence)

            class Process:
                returncode = 0
                pid = 101

                async def wait(self):
                    return 0

            create = mock.AsyncMock(return_value=Process())
            signals = []

            def killpg(_pid, signum):
                signals.append(signum)
                if signum == 0:
                    raise ProcessLookupError

            with mock.patch.object(
                MODULE.asyncio, "create_subprocess_exec", create
            ), mock.patch.object(MODULE.os, "killpg", side_effect=killpg):
                result = asyncio.run(
                    MODULE.run_fixture(
                        args, self.contract_hash, "attempt-1", "revalidate", source_hash
                    )
                )

            self.assertEqual(result["passed"], 3)
            positional, keyword = create.await_args
            self.assertEqual(positional, ("fixture-program", "--all"))
            self.assertIs(keyword["start_new_session"], True)
            self.assertIs(keyword["stdout"], MODULE.asyncio.subprocess.DEVNULL)
            self.assertIs(keyword["stderr"], MODULE.asyncio.subprocess.DEVNULL)
            self.assertEqual(keyword["env"]["XLH_REBUILD_ATTEMPT_ID"], "attempt-1")
            self.assertEqual(keyword["env"]["XLH_REBUILD_SOURCE_SHA256"], source_hash)
            self.assertEqual(signals, [MODULE.signal.SIGTERM, 0])

    def test_async_fixture_success_terminates_descendants_before_reading_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._fixture_args(root)
            source_hash = SHA_B
            evidence = {
                "schema_version": 2,
                "evidence_type": "xlh.lightrag_fixture.v2",
                "attempt_id": "attempt-1",
                "generation": "generation-1",
                "operation": "revalidate",
                "contract_sha256": self.contract_hash,
                "source_sha256": source_hash,
                "suite_version": "fixtures-v1",
                "passed": 1,
                "failed": 0,
                "results_sha256": SHA_C,
            }
            write_canonical(root / "fixture.json", evidence)

            class Process:
                returncode = 0
                pid = 105

                async def wait(self):
                    return 0

            process = Process()
            descendants_alive = True
            events = []

            def killpg(_pid, signum):
                nonlocal descendants_alive
                events.append(signum)
                if signum == MODULE.signal.SIGTERM:
                    descendants_alive = False
                elif signum == 0 and not descendants_alive:
                    raise ProcessLookupError

            original_read = MODULE.read_canonical_json

            def read(path, label):
                self.assertFalse(descendants_alive)
                events.append("read")
                return original_read(path, label)

            with mock.patch.object(
                MODULE.asyncio, "create_subprocess_exec", return_value=process
            ), mock.patch.object(
                MODULE.os, "killpg", side_effect=killpg
            ), mock.patch.object(
                MODULE, "read_canonical_json", side_effect=read
            ):
                result = asyncio.run(
                    MODULE.run_fixture(
                        args, self.contract_hash, "attempt-1", "revalidate", source_hash
                    )
                )

            self.assertEqual(result["passed"], 1)
            self.assertEqual(events, [MODULE.signal.SIGTERM, 0, "read"])

    def test_async_fixture_cancellation_terminates_and_reaps_child(self):
        async def exercise(args):
            started = asyncio.Event()

            class Process:
                returncode = None
                pid = 102
                terminated = False
                killed = False
                wait_calls = 0

                async def wait(self):
                    self.wait_calls += 1
                    if self.terminated:
                        self.returncode = -15
                        return self.returncode
                    started.set()
                    await asyncio.Event().wait()

            process = Process()

            def killpg(_pid, signum):
                if signum == MODULE.signal.SIGTERM:
                    process.terminated = True
                elif signum == MODULE.signal.SIGKILL:
                    process.killed = True
                elif signum == 0 and process.returncode is not None:
                    raise ProcessLookupError

            with mock.patch.object(
                MODULE.asyncio, "create_subprocess_exec", return_value=process
            ), mock.patch.object(
                MODULE.os, "killpg", side_effect=killpg
            ):
                task = asyncio.create_task(
                    MODULE.run_fixture(
                        args, self.contract_hash, "attempt-1", "revalidate", SHA_B
                    )
                )
                await started.wait()
                task.cancel()
                with self.assertRaises(asyncio.CancelledError):
                    await task
            self.assertTrue(process.terminated)
            self.assertFalse(process.killed)
            self.assertEqual(process.wait_calls, 2)

        with tempfile.TemporaryDirectory() as directory:
            asyncio.run(exercise(self._fixture_args(pathlib.Path(directory))))

    def test_async_fixture_cancellation_during_spawn_reaps_created_child(self):
        async def exercise(args):
            creation_started = asyncio.Event()
            release_creation = asyncio.Event()

            class Process:
                returncode = None
                pid = 103
                terminated = False

                async def wait(self):
                    self.returncode = -15
                    return self.returncode

            process = Process()

            async def create(*_command, **_kwargs):
                creation_started.set()
                await release_creation.wait()
                return process

            def killpg(_pid, signum):
                if signum == MODULE.signal.SIGTERM:
                    process.terminated = True
                elif signum == MODULE.signal.SIGKILL:
                    raise AssertionError("graceful termination should reap the child")
                elif signum == 0 and process.returncode is not None:
                    raise ProcessLookupError

            with mock.patch.object(
                MODULE.asyncio, "create_subprocess_exec", side_effect=create
            ), mock.patch.object(MODULE.os, "killpg", side_effect=killpg):
                task = asyncio.create_task(
                    MODULE.run_fixture(
                        args, self.contract_hash, "attempt-1", "revalidate", SHA_B
                    )
                )
                await creation_started.wait()
                task.cancel()
                release_creation.set()
                with self.assertRaises(asyncio.CancelledError):
                    await task
            self.assertTrue(process.terminated)
            self.assertEqual(process.returncode, -15)

        with tempfile.TemporaryDirectory() as directory:
            asyncio.run(exercise(self._fixture_args(pathlib.Path(directory))))

    def test_async_fixture_timeout_escalates_to_kill_and_reaps_child(self):
        class Process:
            returncode = None
            pid = 104
            terminated = False
            killed = False
            wait_calls = 0

            async def wait(self):
                self.wait_calls += 1
                if self.killed:
                    self.returncode = -9
                    return self.returncode
                await asyncio.Event().wait()

        with tempfile.TemporaryDirectory() as directory:
            args = self._fixture_args(pathlib.Path(directory), timeout=0)
            process = Process()

            def killpg(_pid, signum):
                if signum == MODULE.signal.SIGTERM:
                    process.terminated = True
                elif signum == MODULE.signal.SIGKILL:
                    process.killed = True
                elif signum == 0 and process.killed:
                    raise ProcessLookupError

            with mock.patch.object(
                MODULE.asyncio, "create_subprocess_exec", return_value=process
            ), mock.patch.object(
                MODULE.os, "killpg", side_effect=killpg
            ), mock.patch.object(MODULE, "FIXTURE_TERMINATION_GRACE_SECONDS", 0.01):
                with self.assertRaises(MODULE.ClassifiedFailure) as raised:
                    asyncio.run(
                        MODULE.run_fixture(
                            args,
                            self.contract_hash,
                            "attempt-1",
                            "revalidate",
                            SHA_B,
                        )
                    )
            self.assertEqual(raised.exception.code, "fixture_timeout")
            self.assertTrue(process.terminated)
            self.assertTrue(process.killed)
            self.assertGreaterEqual(process.wait_calls, 1)

    def test_real_fixture_output_is_discarded_and_failure_is_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self._fixture_args(pathlib.Path(directory), timeout=5)
            stdout_canary = "fixture-stdout-private-canary"
            stderr_canary = "fixture-stderr-private-canary"
            args.fixture_command = [
                sys.executable,
                "-c",
                (
                    "import sys; "
                    f"sys.stdout.write({stdout_canary!r} * 100000); "
                    f"sys.stderr.write({stderr_canary!r} * 100000); "
                    "sys.exit(9)"
                ),
            ]
            output = io.StringIO()
            errors = io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                with self.assertRaises(MODULE.ClassifiedFailure) as raised:
                    asyncio.run(
                        MODULE.run_fixture(
                            args,
                            self.contract_hash,
                            "attempt-1",
                            "revalidate",
                            SHA_B,
                        )
                    )
            self.assertEqual(raised.exception.code, "fixture_failed")
            combined = output.getvalue() + errors.getvalue()
            self.assertNotIn(stdout_canary, combined)
            self.assertNotIn(stderr_canary, combined)
            self.assertNotIn("Traceback", combined)


class AbandonedRecoveryTests(FenceTestCase):
    def test_recovery_is_noop_without_rebuilding_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MODULE.FenceStore(pathlib.Path(directory))
            self.assertIsNone(MODULE._recover_abandoned(store, None, self.contract))
            with mock.patch.object(MODULE, "utc_now", return_value=FINISHED_AT):
                stale = MODULE.marker(
                    "stale",
                    "generation-1",
                    "attempt-1",
                    "revalidate",
                    self.contract_hash,
                    "invalidated",
                )
            self.assertIs(MODULE._recover_abandoned(store, stale, self.contract), stale)

    def test_rebuilding_marker_becomes_failed_with_immutable_abandoned_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.FenceStore(root)
            store.attempt(
                MODULE.attempt_context(
                    "attempt-old",
                    "generation-1",
                    "migration_rebuild",
                    self.contract,
                    self.contract_hash,
                    STARTED_AT,
                )
            )
            with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                rebuilding = MODULE.marker(
                    "rebuilding",
                    "generation-1",
                    "attempt-old",
                    "migration_rebuild",
                    self.contract_hash,
                    "in_progress",
                )
            store.publish(rebuilding)
            failed = MODULE._recover_abandoned(store, rebuilding, self.contract)
            self.assertEqual(failed["state"], "failed")
            self.assertEqual(failed["reason_code"], "abandoned_rebuilding")
            report = MODULE.read_canonical_json(root / failed["report_path"], "report")
            self.assertEqual(MODULE.digest(report), failed["report_sha256"])
            self.assertEqual(report["attempt_id"], "attempt-old")
            self.assertEqual(report["errors"][0]["code"], "abandoned_rebuilding")
            self.assertEqual(store.read_current(), failed)

    def test_abandoned_recovery_handles_runtime_contract_change(self):
        with mock.patch.dict(os.environ, {"XLH_LIGHTRAG_IMAGE_DIGEST": SHA_B}):
            old_contract = MODULE.build_contract()
        old_hash = MODULE.digest(old_contract)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.FenceStore(root)
            store.attempt(
                MODULE.attempt_context(
                    "attempt-old",
                    "generation-old",
                    "migration_rebuild",
                    old_contract,
                    old_hash,
                    STARTED_AT,
                )
            )
            with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                rebuilding = MODULE.marker(
                    "rebuilding",
                    "generation-old",
                    "attempt-old",
                    "migration_rebuild",
                    old_hash,
                    "in_progress",
                )
            store.publish(rebuilding)
            failed = MODULE._recover_abandoned(store, rebuilding, self.contract)
            report = MODULE.read_canonical_json(root / failed["report_path"], "report")
            self.assertEqual(failed["state"], "failed")
            self.assertEqual(report["attempt_id"], "attempt-old")
            self.assertEqual(report["contract"]["contract_sha256"], failed["contract_sha256"])

    def test_abandoned_recovery_reuses_existing_failed_report_after_crash(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.FenceStore(root)
            report = valid_failed_report(
                self.contract, self.contract_hash, attempt="attempt-old"
            )
            report["reason_code"] = "abandoned_rebuilding"
            report["errors"] = [
                {
                    "stage": "controller",
                    "target": "all",
                    "code": "abandoned_rebuilding",
                    "retryable": True,
                }
            ]
            relative, report_hash = store.report(report)
            with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                rebuilding = MODULE.marker(
                    "rebuilding",
                    "generation-1",
                    "attempt-old",
                    "bootstrap_empty",
                    self.contract_hash,
                    "in_progress",
                )
            store.publish(rebuilding)
            failed = MODULE._recover_abandoned(store, rebuilding, self.contract)
            self.assertEqual(failed["state"], "failed")
            self.assertEqual(failed["report_path"], relative)
            self.assertEqual(failed["report_sha256"], report_hash)
            self.assertEqual(store.read_current(), failed)

    def test_abandoned_recovery_reuses_matching_operation_failed_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.FenceStore(root)
            report = valid_failed_report(
                self.contract, self.contract_hash, attempt="attempt-old"
            )
            relative, report_hash = store.report(report)
            with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                rebuilding = MODULE.marker(
                    "rebuilding",
                    "generation-1",
                    "attempt-old",
                    "bootstrap_empty",
                    self.contract_hash,
                    "in_progress",
                )
            store.publish(rebuilding)
            failed = MODULE._recover_abandoned(store, rebuilding, self.contract)
            self.assertEqual(failed["state"], "failed")
            self.assertEqual(failed["reason_code"], "operation_failed")
            self.assertEqual(failed["report_path"], relative)
            self.assertEqual(failed["report_sha256"], report_hash)

    def test_abandoned_recovery_fails_closed_without_matching_attempt_context(self):
        for name, context_generation in (("missing", None), ("mismatched", "generation-2")):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                store = MODULE.FenceStore(root)
                if context_generation is not None:
                    store.attempt(
                        MODULE.attempt_context(
                            "attempt-old",
                            context_generation,
                            "migration_rebuild",
                            self.contract,
                            self.contract_hash,
                            STARTED_AT,
                        )
                    )
                with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                    rebuilding = MODULE.marker(
                        "rebuilding",
                        "generation-1",
                        "attempt-old",
                        "migration_rebuild",
                        self.contract_hash,
                        "in_progress",
                    )
                store.publish(rebuilding)
                with self.assertRaises((FileNotFoundError, ValueError)):
                    MODULE._recover_abandoned(store, rebuilding, self.contract)
                self.assertEqual(store.read_current(), rebuilding)
                self.assertFalse((root / "reports" / "attempt-old.json").exists())

    def test_recovery_publishes_matching_verified_report_from_rebuilding_or_stale(self):
        for state, operation, reason in (
            ("rebuilding", "bootstrap_empty", "in_progress"),
            ("stale", "revalidate", "invalidated"),
        ):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                store = MODULE.FenceStore(root)
                report = valid_verified_report(
                    self.contract,
                    self.contract_hash,
                    operation=operation,
                    attempt="attempt-old",
                )
                relative, report_hash = store.report(report)
                with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                    nonterminal = MODULE.marker(
                        state,
                        "generation-1",
                        "attempt-old",
                        operation,
                        self.contract_hash,
                        reason,
                    )
                store.publish(nonterminal)
                with mock.patch.object(MODULE, "utc_now", return_value=FINISHED_AT):
                    recovered = MODULE._recover_abandoned(
                        store, nonterminal, self.contract
                    )
                self.assertEqual(recovered["state"], "verified")
                self.assertEqual(recovered["report_path"], relative)
                self.assertEqual(recovered["report_sha256"], report_hash)
                self.assertEqual(store.read_current(), recovered)
                store.verify_current("generation-1", self.contract_hash)

    def test_abandoned_recovery_rejects_unrelated_existing_terminal_report(self):
        unrelated = valid_failed_report(
            self.contract, self.contract_hash, attempt="attempt-old"
        )
        unrelated["generation"] = "generation-2"
        for name, report in (("mismatched-generation", unrelated),):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                store = MODULE.FenceStore(root)
                store.report(copy.deepcopy(report))
                with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                    rebuilding = MODULE.marker(
                        "rebuilding",
                        "generation-1",
                        "attempt-old",
                        "bootstrap_empty",
                        self.contract_hash,
                        "in_progress",
                    )
                store.publish(rebuilding)
                with self.assertRaises(ValueError):
                    MODULE._recover_abandoned(store, rebuilding, self.contract)
                self.assertEqual(store.read_current(), rebuilding)

    def test_recovery_logs_only_after_durable_terminal_publication(self):
        canary = "attempt-secret-canary"
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.FenceStore(root)
            store.attempt(
                MODULE.attempt_context(
                    canary,
                    "generation-secret-canary",
                    "bootstrap_empty",
                    self.contract,
                    self.contract_hash,
                    STARTED_AT,
                )
            )
            rebuilding = MODULE.marker(
                "rebuilding",
                "generation-secret-canary",
                canary,
                "bootstrap_empty",
                self.contract_hash,
                "in_progress",
            )
            store.publish(rebuilding)
            events = []
            output = io.StringIO()
            original_publish = MODULE.FenceStore.publish

            def publish(store_, value):
                events.append("publish")
                return original_publish(store_, value)

            def emit(*args, **kwargs):
                events.append("emit")
                return original_emit(*args, **kwargs)

            original_emit = MODULE.emit_transition
            with contextlib.redirect_stdout(output), mock.patch.object(
                MODULE.FenceStore, "publish", autospec=True, side_effect=publish
            ), mock.patch.object(MODULE, "emit_transition", side_effect=emit):
                recovered = MODULE._recover_abandoned(
                    store, rebuilding, self.contract, started_ns=0
                )

            self.assertEqual(events, ["publish", "emit"])
            self.assertEqual(store.read_current(), recovered)
            lines = parse_json_lines(output)
            self.assertEqual(len(lines), 1)
            self.assertEqual(lines[0]["outcome"], "recovered")
            self.assertEqual(lines[0]["state"], "failed")
            self.assertEqual(lines[0]["reason_code"], "abandoned_rebuilding")
            self.assertNotIn(canary, output.getvalue())
            self.assertNotIn("generation-secret-canary", output.getvalue())


class ExecuteBootstrapTests(FenceTestCase):
    def _writer_and_args(self, root, attempt):
        writer = valid_writer(
            self.contract_hash,
            attempt=attempt,
            operation="bootstrap_empty",
        )
        writer_path = write_canonical(root / f"{attempt}-writer.json", writer)
        writer_hash = MODULE.digest(writer)
        args = MODULE.parser().parse_args(
            [
                "bootstrap",
                "--fence-dir",
                str(root / "fence"),
                "--generation",
                "generation-1",
                "--attempt-id",
                attempt,
                "--writer-evidence",
                str(writer_path),
                "--writer-evidence-sha256",
                writer_hash,
            ]
        )
        return args

    def test_execute_bootstrap_sequences_fresh_verifier_and_publishes_verified(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._writer_and_args(root, "attempt-bootstrap")
            events = []
            worker = SimpleNamespace(
                name="worker",
                graph=object(),
                text_chunks=object(),
                entities_vdb=object(),
                relationships_vdb=object(),
                chunks_vdb=object(),
            )
            verifier = SimpleNamespace(
                name="verifier",
                graph=object(),
                text_chunks=object(),
                entities_vdb=object(),
                relationships_vdb=object(),
                chunks_vdb=object(),
            )

            async def consistency(*_args, **_kwargs):
                events.append("consistency")
                return {
                    "consistent": True,
                    "graph_entities": 0,
                    "graph_relations": 0,
                    "missing_entities": 0,
                    "missing_relations": 0,
                    "skipped_nodes": 0,
                    "skipped_edges": 0,
                }

            rebuild_module = SimpleNamespace(check_vdb_consistency=consistency)
            setup_results = iter(((worker, rebuild_module), (verifier, rebuild_module)))

            async def setup():
                result = next(setup_results)
                events.append(f"setup:{result[0].name}")
                return result

            empty_source = {
                "graph_nodes": 0,
                "graph_edges_raw": 0,
                "relationships_normalized": 0,
                "text_chunks": 0,
                "full_documents": 0,
                "document_status": 0,
                "sha256": SHA_B,
            }

            async def expected(tool, _module):
                events.append(f"source:{tool.name}")
                return ({name: set() for name in MODULE.TARGETS}, copy.deepcopy(empty_source))

            async def finalize(tool):
                events.append(f"finalize:{tool.name}")

            async def inspect(name, _storage, _expected, _prepared):
                events.append(f"inspect:{name}")
                return valid_target(name, 0)

            original_publish = MODULE.FenceStore.publish
            original_report = MODULE.FenceStore.report
            original_attempt = MODULE.FenceStore.attempt

            def attempt(store, value):
                events.append(f"attempt:{value['attempt_id']}")
                return original_attempt(store, value)

            def publish(store, value):
                events.append(f"publish:{value['state']}")
                return original_publish(store, value)

            def emit(transition, *emit_args, **emit_kwargs):
                events.append(f"emit:{transition['state']}")

            def report(store, value):
                events.append(f"report:{value['state']}")
                return original_report(store, value)

            with mock.patch.object(MODULE, "setup_tool", side_effect=setup), mock.patch.object(
                MODULE, "expected_ids", side_effect=expected
            ), mock.patch.object(
                MODULE, "finalize_tool", side_effect=finalize
            ), mock.patch.object(
                MODULE, "inspect_target", side_effect=inspect
            ), mock.patch.object(
                MODULE.FenceStore, "attempt", autospec=True, side_effect=attempt
            ), mock.patch.object(
                MODULE.FenceStore, "publish", autospec=True, side_effect=publish
            ), mock.patch.object(
                MODULE.FenceStore, "report", autospec=True, side_effect=report
            ), mock.patch.object(
                MODULE, "emit_transition", side_effect=emit
            ):
                self.assertEqual(asyncio.run(MODULE.execute(args)), 0)

            self.assertEqual(
                events,
                [
                    "attempt:attempt-bootstrap",
                    "publish:rebuilding",
                    "emit:rebuilding",
                    "setup:worker",
                    "source:worker",
                    "finalize:worker",
                    "setup:verifier",
                    "source:verifier",
                    "inspect:entities",
                    "inspect:relationships",
                    "inspect:chunks",
                    "consistency",
                    "finalize:verifier",
                    "report:verified",
                    "publish:verified",
                    "emit:verified",
                ],
            )
            store = MODULE.FenceStore(root / "fence", create=False)
            store.verify_current("generation-1", self.contract_hash)

    def test_exact_retry_recovers_verified_report_without_reexecuting(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._writer_and_args(root, "attempt-bootstrap")
            store = MODULE.FenceStore(root / "fence")
            report = valid_verified_report(
                self.contract, self.contract_hash, attempt="attempt-bootstrap"
            )
            relative, report_hash = store.report(report)
            with mock.patch.object(MODULE, "utc_now", return_value=STARTED_AT):
                store.publish(
                    MODULE.marker(
                        "rebuilding",
                        "generation-1",
                        "attempt-bootstrap",
                        "bootstrap_empty",
                        self.contract_hash,
                        "in_progress",
                    )
                )
            output = io.StringIO()
            with contextlib.redirect_stdout(output), mock.patch.object(
                MODULE, "utc_now", return_value=FINISHED_AT
            ), mock.patch.object(MODULE, "setup_tool", new_callable=mock.AsyncMock) as setup:
                self.assertEqual(asyncio.run(MODULE.execute(args)), 0)
            setup.assert_not_awaited()
            events = parse_json_lines(output)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0]["outcome"], "recovered")
            self.assertEqual(events[0]["state"], "verified")
            current = store.read_current()
            self.assertEqual(current["state"], "verified")
            self.assertEqual(current["report_path"], relative)
            self.assertEqual(current["report_sha256"], report_hash)
            store.verify_current("generation-1", self.contract_hash)

    def test_already_verified_retry_is_idempotent_after_strict_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = MODULE.parser().parse_args(
                [
                    "rebuild",
                    "--fence-dir",
                    str(root / "fence"),
                    "--generation",
                    "generation-1",
                    "--attempt-id",
                    "attempt-rebuild",
                ]
            )
            store = MODULE.FenceStore(root / "fence")
            report = valid_verified_report(
                self.contract,
                self.contract_hash,
                operation="migration_rebuild",
                attempt="attempt-rebuild",
            )
            publish_pair(root / "fence", report)
            output = io.StringIO()
            order = []
            original_verify = MODULE.FenceStore.verify_current
            original_emit = MODULE.emit_transition

            def verify(store_, generation, contract_hash):
                order.append("verify")
                return original_verify(store_, generation, contract_hash)

            def emit(*emit_args, **emit_kwargs):
                order.append("emit")
                return original_emit(*emit_args, **emit_kwargs)

            with contextlib.redirect_stdout(output), mock.patch.object(
                MODULE.FenceStore, "verify_current", autospec=True, side_effect=verify
            ), mock.patch.object(
                MODULE, "emit_transition", side_effect=emit
            ), mock.patch.object(
                MODULE, "setup_tool", new_callable=mock.AsyncMock
            ) as setup:
                self.assertEqual(asyncio.run(MODULE.execute(args)), 0)

            setup.assert_not_awaited()
            self.assertEqual(order, ["verify", "emit"])
            events = parse_json_lines(output)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0]["outcome"], "idempotent")
            self.assertEqual(events[0]["state"], "verified")
            for name, expected in zip(MODULE.TARGETS, (2, 1, 3)):
                self.assertEqual(
                    events[0]["targets"][name]["source_total"], expected
                )
                self.assertEqual(
                    events[0]["targets"][name]["rebuilt"], expected
                )

    def test_execute_bootstrap_failure_finalizes_and_publishes_failed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._writer_and_args(root, "attempt-failure")
            events = []
            worker = SimpleNamespace(
                name="worker",
                graph=object(),
                text_chunks=object(),
                entities_vdb=object(),
                relationships_vdb=object(),
                chunks_vdb=object(),
            )
            rebuild_module = SimpleNamespace()

            async def setup():
                events.append("setup")
                return worker, rebuild_module

            async def expected(*_args):
                events.append("source")
                raise RuntimeError("source failed with secret detail")

            async def finalize(_tool):
                events.append("finalize")

            original_publish = MODULE.FenceStore.publish
            original_report = MODULE.FenceStore.report
            original_attempt = MODULE.FenceStore.attempt

            def attempt(store, value):
                events.append(f"attempt:{value['attempt_id']}")
                return original_attempt(store, value)

            def publish(store, value):
                events.append(f"publish:{value['state']}")
                return original_publish(store, value)

            def emit(transition, *emit_args, **emit_kwargs):
                events.append(f"emit:{transition['state']}")

            def report(store, value):
                events.append(f"report:{value['state']}")
                return original_report(store, value)

            with mock.patch.object(MODULE, "setup_tool", side_effect=setup), mock.patch.object(
                MODULE, "expected_ids", side_effect=expected
            ), mock.patch.object(
                MODULE, "finalize_tool", side_effect=finalize
            ), mock.patch.object(
                MODULE, "inspect_target", new_callable=mock.AsyncMock
            ) as inspect, mock.patch.object(
                MODULE.FenceStore, "attempt", autospec=True, side_effect=attempt
            ), mock.patch.object(
                MODULE.FenceStore, "publish", autospec=True, side_effect=publish
            ), mock.patch.object(
                MODULE.FenceStore, "report", autospec=True, side_effect=report
            ), mock.patch.object(
                MODULE, "emit_transition", side_effect=emit
            ):
                with self.assertRaises(RuntimeError):
                    asyncio.run(MODULE.execute(args))
                inspect.assert_not_awaited()

            self.assertEqual(
                events,
                [
                    "attempt:attempt-failure",
                    "publish:rebuilding",
                    "emit:rebuilding",
                    "setup",
                    "source",
                    "finalize",
                    "report:failed",
                    "publish:failed",
                    "emit:failed",
                ],
            )
            store = MODULE.FenceStore(root / "fence", create=False)
            current = store.read_current()
            self.assertEqual(current["state"], "failed")
            report = MODULE.read_canonical_json(
                root / "fence" / current["report_path"], "failed report"
            )
            self.assertEqual(report["errors"][0]["code"], "unknown")
            self.assertNotIn("secret detail", json.dumps(report))

    def test_task_cancellation_finalizes_and_publishes_failed_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = self._writer_and_args(root, "attempt-cancelled")
            events = []
            entered = None
            worker = SimpleNamespace(
                graph=object(),
                text_chunks=object(),
                entities_vdb=object(),
                relationships_vdb=object(),
                chunks_vdb=object(),
            )

            async def setup():
                events.append("setup")
                return worker, SimpleNamespace()

            async def expected(*_args):
                events.append("source")
                entered.set()
                await asyncio.Event().wait()

            async def finalize(_tool):
                events.append("finalize")

            async def exercise():
                nonlocal entered
                entered = asyncio.Event()
                task = asyncio.create_task(MODULE.execute(args))
                await entered.wait()
                task.cancel()
                with self.assertRaisesRegex(RuntimeError, "^operation cancelled$"):
                    await task

            original_publish = MODULE.FenceStore.publish
            original_report = MODULE.FenceStore.report

            def publish(store, value):
                events.append(f"publish:{value['state']}")
                return original_publish(store, value)

            def emit(transition, *emit_args, **emit_kwargs):
                events.append(f"emit:{transition['state']}")

            def report(store, value):
                events.append(f"report:{value['state']}")
                return original_report(store, value)

            with mock.patch.object(MODULE, "setup_tool", side_effect=setup), mock.patch.object(
                MODULE, "expected_ids", side_effect=expected
            ), mock.patch.object(
                MODULE, "finalize_tool", side_effect=finalize
            ), mock.patch.object(
                MODULE.FenceStore, "publish", autospec=True, side_effect=publish
            ), mock.patch.object(
                MODULE.FenceStore, "report", autospec=True, side_effect=report
            ), mock.patch.object(
                MODULE, "emit_transition", side_effect=emit
            ):
                asyncio.run(exercise())

            self.assertEqual(
                events,
                [
                    "publish:rebuilding",
                    "emit:rebuilding",
                    "setup",
                    "source",
                    "finalize",
                    "report:failed",
                    "publish:failed",
                    "emit:failed",
                ],
            )
            store = MODULE.FenceStore(root / "fence", create=False)
            current = store.read_current()
            self.assertEqual(current["state"], "failed")
            report = MODULE.read_canonical_json(
                root / "fence" / current["report_path"], "failed report"
            )
            self.assertEqual(
                report["errors"][0]["code"], "operation_cancelled"
            )
            self.assertTrue(report["cleanup"]["worker_finalized"])
            self.assertFalse(report["cleanup"]["verifier_finalized"])


if __name__ == "__main__":
    unittest.main()
