# LightRAG Milvus Rebuild Fence Contract

- Status: `IMPLEMENTED — LIVE LIFECYCLE VERIFICATION BLOCKED`
- Authoritative spec: `./spec.md`
- Applies to: LightRAG `v1.5.7` at commit
  `28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c` and Milvus `2.6.11`
- Owner: deployment/operator tooling and application readiness

## Purpose And Boundary

This document defines the repository-owned, deployment-side fence for creating,
rebuilding, restoring and admitting LightRAG's Milvus vector projections. LightRAG
does not persist or consume this fence. A successful `lightrag-rebuild-vdb` process,
an HTTP health response, or the existence of Milvus collections is not equivalent to a
verified deployment.

The four-store ownership boundary remains unchanged:

| Data | Authority | Rebuild role |
|---|---|---|
| documents, chunks and caches | `JsonKVStorage` in `WORKING_DIR` | `text_chunks` is the authoritative chunk-vector source |
| entity-relation graph | `NetworkXStorage` in `WORKING_DIR` | graph nodes and edges are the authoritative entity/relation-vector sources |
| ingestion/document status | `JsonDocStatusStorage` in `WORKING_DIR` | verified independently during restore and runtime readiness |
| chunk, entity and relationship vectors | `MilvusVectorDBStorage` | derived projections only |

The Go application never rebuilds, queries or mutates Milvus directly. Collection
inspection is restricted to deployment verification tooling. Normal knowledge access
continues through the official authenticated LightRAG API.

## Fixed Deployment Contract

The fence binds a deployment generation to this complete semantic contract, not merely
to the collection names:

```text
LIGHTRAG_KV_STORAGE=JsonKVStorage
LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage
LIGHTRAG_GRAPH_STORAGE=NetworkXStorage
LIGHTRAG_DOC_STATUS_STORAGE=JsonDocStatusStorage
WORKSPACE=xiaolanhe_v1
WORKING_DIR=/app/data/rag_storage
MILVUS_DB_NAME=lightrag
MILVUS_WORKSPACE=<unset; inherits WORKSPACE>
MILVUS_INDEX_TYPE=AUTOINDEX
MILVUS_METRIC_TYPE=COSINE
EMBEDDING_MODEL=text-embedding-v4
EMBEDDING_DIM=1024
EMBEDDING_SEND_DIM=false
EMBEDDING_ASYMMETRIC=false
EMBEDDING_DOCUMENT_PREFIX=<unset>
EMBEDDING_QUERY_PREFIX=<unset>
```

`contract_sha256` is the lowercase SHA-256 of canonical UTF-8 JSON with sorted object
keys, no insignificant whitespace, explicit nulls for unset values and no secrets. Its
input contains:

- schema version for this fence contract;
- LightRAG version, commit and immutable image digest;
- Milvus version and database name;
- workspace and a stable deployment identity for `WORKING_DIR`, never its contents;
- all four storage class names;
- embedding provider/binding/endpoint semantic identifier, model, dimension,
  send-dimension flag, asymmetric mode and both prefixes;
- Milvus index and metric types;
- upstream vector-ID canonicalization version;
- fixture suite version;
- legacy knowledge manifest identity when legacy import applies.

Credentials, tokens, DSNs and raw endpoints containing credentials are excluded. Any
semantic change to these fields requires a new generation and makes an older marker
ineligible even if collection names happen to differ or remain the same.

## Five-State Machine

The logical states are `absent`, `stale`, `rebuilding`, `failed` and `verified`.
`absent` means that no readable current marker exists; it is not serialized. Only
`verified` for the exact required generation and contract can satisfy readiness.

```text
absent -------------------+
stale --------------------+--> rebuilding --> verified
failed -------------------+         |
verified + invalidation --+         +--------> failed
```

| From | To | Required cause and action |
|---|---|---|
| `absent` | `rebuilding` | writer fence, backup and preflight pass; publish before any destructive rebuild call |
| `stale` | `rebuilding` | changed generation/contract or restored state requires rebuilding rather than verification-only |
| `failed` | `rebuilding` | operator starts a new attempt with a new attempt ID after prerequisites pass |
| `verified` | `stale` | generation, contract, restore epoch or required legacy manifest changes |
| `rebuilding` | `verified` | all official stats and independent postconditions pass and the immutable report is durable |
| `rebuilding` | `failed` | exception, cancellation, timeout, invalid stats, source mutation, failed verification or cleanup |
| `stale` | `verified` | verification-only restore/bootstrap path proves every applicable postcondition without destructive rebuilding |

An observed `rebuilding` marker whose owning process no longer holds the deployment
lock is an abandoned attempt. The next controller records a failed immutable report for
that attempt before creating a new attempt. It never silently promotes or resumes it.

Invalid or unreadable JSON, an unknown schema/state, a missing report, a digest mismatch,
an unknown generation, or a missing marker is readiness failure. There is no time-based
automatic promotion.

## Filesystem Layout And Atomic Protocol

The fence lives on a small deployment-owned persistent filesystem outside the
LightRAG, Milvus, etcd and MinIO data volumes:

```text
/rebuild-fence/
  serving.lock
  rebuild.lock
  current.json
  attempts/
    <attempt-id>.json
  reports/
    <attempt-id>.json
```

The host operator selects one nonzero numeric shared GID in
`XLH_LIGHTRAG_SHARED_GID`. When it is omitted, the bootstrap wrapper uses the invoking
operator's primary GID. A non-root operator may explicitly select only a GID in its
supplementary-group set; root must explicitly select a dedicated nonzero GID. The wrapper
prints `XLH_LIGHTRAG_SHARED_GID=<gid>` alongside its four evidence values, and deployment
automation persists and reuses that exact value for later ownership preparation and
reader-container enrollment in the generation. It must not silently recompute a
different group under another operator account.

Before starting the controller, the wrapper runs one bounded ownership initializer as
root from the exact pinned LightRAG image with its entry point overridden. That
initializer is scoped to the canonical fence and writer-evidence bind mounts. It assigns
the fence tree to LightRAG UID `1000` and the selected shared GID, while assigning the
writer-evidence tree to the invoking operator UID and LightRAG GID `1000`; it then
applies the modes below and exits. The wrapper creates the attempt evidence as the host
operator with mode `0600` before running the initializer, which converts the final
evidence artifact to mode `0640` before the controller reads it. To make this privileged
transition race-resistant, the initializer first performs a complete read-only,
no-follow descriptor validation of both mount trees and rejects unexpected nodes, aliases
and hard links before any metadata mutation. It also validates the bounded file count and
content-size budget and snapshots each regular file before mutation. Non-blocking
exclusive `flock` leases on the two stable root-directory inodes exclude another
initializer before any child mutation. It then freezes every validated directory as
`root:root`/`0700`, revalidates the retained descriptors, and copy-ups every mutable
managed regular file into a private, descriptor-relative `O_EXCL` inode. Only that new
inode is chowned/chmodded, fsynced and atomically replaced into the managed name; a
pre-held descriptor or late external hard link therefore still refers to an unchanged old
inode. Missing `serving.lock` and `rebuild.lock` files are privately created once while the
roots are frozen and are locked before either root is reopened. Once present, each lock
must already have the exact owner, group, mode and single-link shape; its inode is never
replaced or metadata-mutated by the initializer. A descriptor opened before a later
initializer and one opened by path afterwards therefore remain in the same `flock` domain.
The initializer validates and fsyncs every child directory, stages both roots with
their final ownership while mode `0000` keeps them non-traversable, publishes and
fsyncs the writer root first, and uses one final
`fchmod` of the fence root as the commit point. Before that syscall the lifecycle
fence is sealed; after it succeeds the complete tree already has its final contract,
so no fallible post-publish verification or best-effort rollback is required for
safety. Initialization leases remain held through that commit and are released by
descriptor close. A root-owned `0700` state or either-owner `0000` transition state is
fail-closed for an unprivileged retry and requires explicit privileged operator
inspection/recovery; it is never silently repaired by host preflight. The normal
bootstrap/controller and steady services do not
set Compose `user:`: their official image entry point must remain able to initialize and
chown `/app/data/rag_storage` as root and then drop to LightRAG UID/GID `1000`. The
controller does not call `chown` or `chgrp`; setgid fence directories preserve the
initialized shared group for new files.

The host operator identity and Docker daemon are part of the trusted deployment
boundary. The descriptor-based host preflight closes its descriptors before Docker
resolves bind-source pathnames, so another process with the same operator UID could
replace an otherwise valid leaf in that interval. The privileged initializer therefore
revalidates the actual mounted trees independently and rejects unsafe structure, but it
does not claim that the mount inode is cryptographically bound to the earlier host
preflight. Do not run bootstrap on a host where the operator UID or Docker control plane
is shared with an untrusted principal.

| Path | Owner/group | Exact mode | Purpose |
|---|---|---:|---|
| fence root | `1000:<shared-gid>` | `02750` | controller owner writes; shared group only reads/traverses; setgid preserves group |
| `reports/`, `attempts/` | `1000:<shared-gid>` | `02750` | controller writes; group readers traverse/read; setgid preserves group |
| writer-evidence directory | `<operator-uid>:1000` | `02750` | host operator writes; setgid preserves the LightRAG reader group |
| writer evidence | `<operator-uid>:1000` | `0640` | host operator writes; LightRAG GID `1000` reads |
| `current.json`, `reports/*.json`, `attempts/*.json` | `1000:<shared-gid>` | `0640` | controller writes; shared group reads |
| `serving.lock`, `rebuild.lock` | `1000:<shared-gid>` | `0640` | owner/controller locks; shared group can open the existing inode |

No fence or writer-evidence path is world-readable, world-writable or
world-traversable. Expanding these modes to `0777`, `0666`, or any other `other` access
is not an interoperability workaround.

Only the bounded ownership initializer and migration controller mount the fence
read-write. Steady LightRAG and readiness consumers mount it read-only; readiness
consumers receive the selected shared GID without changing their primary identity.
In particular, a containerized Go application keeps its image UID/GID, bind-mounts the
fence read-only, and uses `--group-add "$XLH_LIGHTRAG_SHARED_GID"` (or the equivalent
orchestrator supplementary-group setting). `serving.lock` is a pre-created mode-`0640`
regular file: the steady-state supervisor opens it without following symlinks, takes a
non-blocking shared lease before fence verification, and retains that lease until the
complete LightRAG process group has exited. Every mutating controller invocation first
takes a non-blocking exclusive `serving.lock` lease and then the exclusive
`rebuild.lock`; contention fails closed with the fixed `serving_lock_busy` code. The
fixed lock order is therefore `serving.lock(EX) -> rebuild.lock(EX)`, and a running
steady writer cannot race a verified-to-stale/rebuilding transition. A read-only mount
is sufficient for the steady process to lock the existing inode; only the controller
creates or changes fence files. The controller retains both leases for its entire
preflight, rebuild, verification, report-publication and cleanup sequence.
`rebuild.lock` serializes controllers, while `serving.lock` closes the local
verify-to-start and running-writer race; independently signed writer evidence remains
required for the wider deployment writer fence.

The guarded steady entry point logs `exec/attempt/starting`, starts Gunicorn in a new
process group, forwards `SIGTERM` and `SIGINT`, escalates to `SIGKILL` after a bounded
grace period, reaps the group, and propagates its exit status or signal semantics. It
never logs successful execution before a process has actually run.

Every current-marker replacement uses this sequence:

1. Create a unique temporary file in the same directory with mode `0640` and exclusive
   creation.
2. Write complete canonical JSON and reject partial/unknown data.
3. Flush userspace buffers and `fsync` the file.
4. Atomically rename it over `current.json`.
5. `fsync` the parent directory.

Before publishing `rebuilding`, the controller creates an immutable attempt journal at
`attempts/<attempt-id>.json`. It has the exact keys `schema_version`, `attempt_id`,
`generation`, `operation`, `contract_sha256`, `started_at`, `software`, and `contract`;
the `software` and `contract` objects have the same exact schemas as the terminal report.
The controller reconstructs the canonical contract from those objects and requires its
digest and all four identity fields to match the marker. The journal contains no secrets.
It is mode `0640`, canonical JSON with exactly one trailing newline, and uses the same
temporary-file, file `fsync`, exclusive no-overwrite link, and parent-directory `fsync`
protocol as an immutable report. The journal must be durable before `current.json` can
become `rebuilding`; a missing, malformed, mismatched, or pre-existing journal fails
closed before that transition.

Immutable reports use mode `0640`, the same file and directory durability rules, and
exclusive no-overwrite semantics. A report is fully durable before a current marker may
reference its digest. A rerun always receives a new cryptographically random attempt ID.

Before the first call that can drop a vector target, the controller atomically replaces
any previous current marker with `rebuilding`. On every handled failure it writes an
immutable failed report and atomically publishes `failed`; it never leaves an earlier
`verified` marker current. A process crash or uncatchable kill leaves `rebuilding`, which
also fails closed. The next lock owner uses the immutable journal, not its possibly changed
runtime configuration, to produce the old attempt's contract-bound
`failed/abandoned_rebuilding` report. If that exact failed report was already durable but
its marker publication was interrupted, recovery validates and reuses it idempotently; it
never overwrites a report or relabels a verified or mismatched report. Failure to
close/finalize storage or persist the report is failure.

Signal handling requests cancellation, prevents subsequent target calls, finalizes all
opened storage, and publishes `failed` when durable publication remains possible.

## Current Marker Schema

`current.json` is intentionally small and bounded:

```json
{
  "schema_version": 2,
  "state": "verified",
  "generation": "2026-09-07-milvus-cutover-01",
  "attempt_id": "019...",
  "operation": "migration_rebuild",
  "contract_sha256": "sha256:...",
  "report_path": "reports/019....json",
  "report_sha256": "sha256:...",
  "updated_at": "2026-09-07T12:34:56Z",
  "reason_code": "verified"
}
```

Allowed operations are `migration_rebuild`, `bootstrap_empty`, `restore_verify` and
`revalidate`. They are fixed by the selected command and are not caller-overridable. For
`rebuilding`, `stale` and `failed`, `reason_code` is a bounded enum and must not contain
exception text. `report_path` is relative, must remain beneath `reports/`, and is
mandatory for `verified` and `failed`. Schema version 2 is an exact-key contract; missing
or unknown marker/report keys and noncanonical report JSON are rejected by both Python
and Go readers.

## Immutable Attempt Report

Every terminal attempt produces one canonical JSON report with these required fields:

```json
{
  "schema_version": 2,
  "attempt_id": "019...",
  "generation": "2026-09-07-milvus-cutover-01",
  "operation": "migration_rebuild",
  "state": "verified",
  "reason_code": "verified",
  "started_at": "2026-09-07T12:00:00Z",
  "finished_at": "2026-09-07T12:34:56Z",
  "software": {
    "lightrag_version": "1.5.7",
    "lightrag_commit": "28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c",
    "lightrag_image_digest": "sha256:...",
    "milvus_version": "2.6.11",
    "etcd_version": "3.5.25",
    "minio_version": "RELEASE.2025-09-07T16-13-09Z"
  },
  "contract": {
    "contract_sha256": "sha256:...",
    "workspace": "xiaolanhe_v1",
    "working_directory_identity": "deployment-volume-id",
    "kv_storage": "JsonKVStorage",
    "vector_storage": "MilvusVectorDBStorage",
    "graph_storage": "NetworkXStorage",
    "doc_status_storage": "JsonDocStatusStorage",
    "milvus_database": "lightrag",
    "embedding_contract": {
      "provider_binding": "versioned-provider-binding",
      "endpoint_semantics": "versioned-endpoint-profile",
      "model": "text-embedding-v4",
      "dimension": 1024,
      "send_dimension": false,
      "asymmetric": false,
      "document_prefix": null,
      "query_prefix": null
    },
    "index_type": "AUTOINDEX",
    "metric_type": "COSINE",
    "id_canonicalization_version": "lightrag-v1.5.7",
    "fixture_suite_version": "...",
    "legacy_manifest_sha256": null
  },
  "writer_fence": {
    "pipeline_idle_observed": true,
    "server_replicas": 0,
    "automatic_restart_disabled": true,
    "backup_manifest_sha256": "sha256:..."
  },
  "source": {
    "before_sha256": "sha256:...",
    "after_sha256": "sha256:...",
    "graph_nodes": 0,
    "graph_edges_raw": 0,
    "relationships_normalized": 0,
    "text_chunks": 0
  },
  "rebuild": {
    "entities": {},
    "relationships": {},
    "chunks": {}
  },
  "targets": {
    "entities": {},
    "relationships": {},
    "chunks": {}
  },
  "legacy_import": {
    "required": false,
    "manifest_sha256": null,
    "continuous_success_watermark": null,
    "source_rows": 0,
    "accepted_rows": 0,
    "terminal_rows": 0,
    "reconciled": true,
    "retirement_eligible": false
  },
  "checks": {
    "source_unchanged": true,
    "stats_valid": true,
    "schema_valid": true,
    "exact_id_sets_valid": true,
    "graph_consistency_valid": true,
    "fixtures_valid": true,
    "legacy_import_valid": true,
    "cleanup_valid": true
  },
  "fixtures": {
    "suite_version": "...",
    "passed": 0,
    "failed": 0,
    "results_sha256": "sha256:..."
  },
  "errors": []
}
```

The exact top-level schema additionally always contains `backup`, `duplicate_policy`,
`official_consistency` and `cleanup`. Every evidence section has explicit
`applicable` and bounded `status`; inapplicable sections retain their exact keys with
null/zero values rather than disappearing. Verified reports require an empty `errors`
array, all eight exact checks true and successful cleanup/fresh-verifier finalization.
Failed and abandoned reports use the same top-level and nested schema, mark unexecuted
sections `not_run`/`not_applicable`, set checks false, and include only bounded error
records `{stage,target,code,retryable}`. Timestamps are UTC RFC3339 and ordered.

Each target entry records the upstream-resolved collection name, schema digest, vector
field type/dimension, primary-key field, index/metric, expected and actual counts, and
expected/actual sorted-ID-set digests. Each rebuild entry preserves every field returned
by the pinned official function: `label`, `source_total`, `prepared`, `rebuilt`,
`staged`, `skipped`, `duplicates`, `batches`, `failed_batches` and `errors`.

## Rebuild Preconditions

Before entering `rebuilding`, the controller must:

1. Obtain explicit operator approval for destructive target replacement and paid
   embedding calls.
2. Quiesce all knowledge mutations, observe the document pipeline idle, stop every
   LightRAG server/writer replica and disable automatic restart.
3. Confirm no uncontrolled process can write the same `WORKING_DIR` or Milvus
   namespace.
4. Capture and verify a backup of the authoritative Nano-era `WORKING_DIR` and exact
   versioned configuration. A raw Milvus consistency-unit backup additionally requires
   cleanly stopping Milvus, etcd and MinIO before volume snapshots.
5. Verify the pinned software, workspace, storage classes, embedding contract, target
   database, permissions and healthy Milvus dependencies.
6. Compute canonical source counts and digests for graph nodes, graph edges and
   `text_chunks`.
7. Verify the legacy import gate when this deployment has legacy PostgreSQL knowledge.
8. Acquire the deployment lock and atomically publish `rebuilding`.

The authoritative graph/KV/status files remain unchanged during rebuild. Failed or
partial Milvus targets are disposable derived state and may be rebuilt again under a new
attempt after the same preconditions are re-established.

## Official Library Invocation

Automation imports the pinned `lightrag.tools.rebuild_vdb` module and uses its official
initialization/factory path to obtain the graph, `text_chunks` KV and three initialized
target storage objects. It must not drive the interactive menu and must not reimplement
vector ID, payload, collection drop, batching, embedding or upsert algorithms.

The one-shot worker calls all three official functions explicitly:

```python
entities = await rebuild_entities_vdb(
    graph, entities_vdb, global_config,
    batch_size=batch_size,
)
relationships = await rebuild_relationships_vdb(
    graph, relationships_vdb, global_config,
    batch_size=batch_size,
)
chunks = await rebuild_chunks_vdb(
    text_chunks, chunks_vdb,
    batch_size=batch_size,
)
```

The pinned API accepts an optional progress callback, but the non-interactive
controller intentionally omits it and treats the returned structured statistics as
the authoritative completion evidence.

It may then call:

```python
consistency = await check_vdb_consistency(
    graph, entities_vdb, relationships_vdb, batch_size=batch_size,
)
```

`check_vdb_consistency` is supporting evidence only: it checks graph-to-vector entity
and relationship presence, but does not verify chunks or reverse orphans.

The worker returns a bounded structured result and finalizes every storage object. A
fresh verifier process then opens new source/Milvus connections. The controller checks
the structured result regardless of process exit status. Exit zero is necessary but not
sufficient: the interactive upstream CLI can return zero after cancellation, and a
process can exit cleanly without producing all three valid result objects.

## Official Statistics Predicates

For each of `entities`, `relationships` and `chunks`, every field must exist, integer
fields must be nonnegative, `label` must identify the expected target, and all of these
must hold:

```text
errors == []
failed_batches == 0
staged == 0
rebuilt == prepared
prepared + skipped + duplicates == source_total
```

Production policy requires `skipped == 0`; this release has no skipped-record exception.
`duplicates` defaults to zero for every target and is never silently discarded; a
nonzero value requires a separate canonical reviewed policy artifact whose digest,
reviewer, expiry, exact attempt/generation/operation/contract/source binding and
per-target maximum are validated and retained in the report. Relationship target count
is the official normalized unique relation-ID count (`prepared`), not the raw graph-edge
count. `rebuilt` is credited by v1.5.7 only after `index_done_callback()` succeeds, but
these stats remain self-reported evidence rather than independent verification.

## Independent Postconditions

All applicable checks must pass before publishing `verified`.

### Source stability

Re-enumerate graph nodes, graph edges and `text_chunks` after rebuilding. Their counts
and versioned canonical digests must equal the pre-rebuild snapshot. A mismatch proves
the writer fence was incomplete and fails the attempt.

### Collection identity, schema and index

- Resolve names from the initialized upstream storage objects and require the exact
  pinned results: `xiaolanhe_v1_entities_text_embedding_v4_1024d`,
  `xiaolanhe_v1_relationships_text_embedding_v4_1024d` and
  `xiaolanhe_v1_chunks_text_embedding_v4_1024d`.
- Confirm all three collections are in database `lightrag`, dynamic fields are enabled,
  and the field set has neither omissions nor additions.
- Common fields are non-null `id VARCHAR(64) PRIMARY KEY`, non-null
  `vector FLOAT_VECTOR(1024)` and non-null `created_at INT64`. Entity nullable fields
  are `entity_name VARCHAR(512)`, `content VARCHAR(65535)`,
  `source_id VARCHAR(65535)`, `file_path VARCHAR(32768)`; relationship nullable fields
  are `src_id VARCHAR(512)`, `tgt_id VARCHAR(512)`, `content VARCHAR(65535)`,
  `source_id VARCHAR(65535)`, `file_path VARCHAR(32768)`; chunk nullable fields are
  `full_doc_id VARCHAR(64)`, `content VARCHAR(65535)`, `file_path VARCHAR(32768)`.
- Confirm the vector field is `FLOAT_VECTOR` with dimension `1024`.
- Confirm `AUTOINDEX` and `COSINE`.
- Load each collection and execute a bounded query through a fresh connection.

### Exact ID sets

Independently enumerate expected entity, relationship and chunk IDs from the
authoritative graph/KV sources. Reuse the pinned upstream normalization and ID/hash
helpers rather than duplicating those algorithms. Enumerate actual Milvus primary keys
with bounded pagination and require exact set equality for every target:

```text
actual_ids == expected_unique_ids
actual_count == len(expected_unique_ids) == rebuild_stats.prepared
```

Store counts and digests of sorted ID sets, not the raw sets. This catches missing IDs,
reverse orphans and equal-count substitutions, including chunk-vector failures that the
official graph consistency helper cannot detect.

### Graph consistency and black-box behavior

- Require the optional official consistency check to report no missing graph entity or
  relationship records.
- Run the versioned fixture suite through the normal authenticated LightRAG API.
- Exercise every approved retrieval mode and require the expected fixture documents,
  chunks, entities, relationships, managed citations and quality thresholds.
- Store only fixture identifiers, booleans, aggregate counts and a result digest. Do not
  persist retrieved content.

### Finalization

All storage callbacks/finalizers and verifier connections must close successfully. The
immutable report must reread with the expected digest. Only then may the controller
atomically publish `verified`.

Historical rebuild counts are not a normal runtime invariant: successful post-cutover
ingestion legitimately changes source and target counts. Routine readiness therefore
checks identity/schema and live service state, not equality to the old rebuild snapshot.

## Legacy PostgreSQL Knowledge Import Gate

Legacy import is a separate prerequisite and reconciliation boundary; a valid rebuild
fence cannot excuse an incomplete import, and a complete import cannot certify Milvus.
When legacy `knowledge_document` rows exist:

1. Freeze the source read-only and create an immutable manifest bound to source
   database identity/snapshot, schema version, ordered row count, stable row-key/content
   digests and import-tool version.
2. Import each document through the official authenticated LightRAG API with source key
   `xlh-legacy-<id>.txt`. Do not copy legacy `knowledge_chunk` rows or 1536-dimensional
   pgvector values; they are derived data and LightRAG re-chunks/re-embeds content.
3. Advance a continuous-success watermark only after each contiguous manifest prefix is
   accepted and reaches an approved terminal LightRAG document state. Gaps, duplicate
   incompatible source keys, pending/failed documents or changed source digests block
   the gate.
4. Reconcile every manifested source row to the expected LightRAG source key and
   terminal status, then run the approved retrieval/citation fixtures.
5. Bind the final manifest digest and reconciliation report digest into the rebuild
   contract/report. If import changes graph/KV after a rebuild, the fence becomes
   `stale` and all three vector targets require rebuild/reverification.
6. Permit relational cutover only when both the legacy import gate and rebuild fence are
   valid for the same contract generation. Declare legacy source tables retirement-
   eligible only after this joint gate. Keep them read-only through the rollback window;
   physical deletion requires separate approval.

Fresh installations with a proven absence of legacy source rows record
`legacy_import.required=false` and the evidence used to establish that absence. They do
not invent an empty import manifest.

## Fresh Empty Bootstrap

A fresh deployment may avoid paid embedding calls only under this exact procedure:

1. Hold the deployment lock and prove LightRAG writers have never started.
2. Initialize the authoritative stores and target storages under the final production
   contract.
3. Independently prove graph nodes, graph edges and `text_chunks` are empty.
4. Require every target collection to be absent or empty. A nonempty target paired with
   empty sources is a failure, never an empty bootstrap.
5. If needed, allow only official storage initialization to create empty collections.
6. Verify all three collection schemas/indexes and zero actual IDs through a fresh
   connection.
7. Verify document/status stores are empty and the legacy import gate is not required.
8. Persist a `bootstrap_empty` report and atomically publish `verified`.

If any authoritative source is nonempty, bootstrap cannot self-certify and must enter
the full rebuild path. Later normal ingestion does not invalidate the bootstrap marker;
it certifies the deployment contract, not perpetual emptiness.

## Backup And Restore Marker Rules

The knowledge consistency unit is the complete `WORKING_DIR`, Milvus data, etcd data,
MinIO data and versioned LightRAG/Milvus/embedding configuration. Partial backup or
restore is rejected. For raw volume archives, stop LightRAG first and then cleanly stop
the complete Milvus/etcd/MinIO stack; stopping only LightRAG writers is insufficient for
a crash-consistent raw Milvus volume archive.

The fence store may be backed up for audit, but a restored `verified` marker is never
trusted. Restore follows this sequence before LightRAG can start:

1. Supply a new required generation/restore epoch from deployment configuration that is
   not sourced from the restored data set.
2. Atomically publish `stale` with reason `restore_pending_verification`; if the marker
   store itself was not restored, absence already fails closed but the controller still
   creates the new generation.
3. Prove every consistency-unit component and its versioned configuration is present.
4. Start etcd and MinIO, then Milvus; keep LightRAG writers stopped.
5. Recompute expected IDs/counts from the restored graph and `text_chunks`, and freshly
   verify document/status consistency, schema/indexes, exact ID sets, official graph
   consistency and retrieval/citation fixtures. Do not compare only with historical
   rebuild counts.
6. On success, write a new immutable `restore_verify` report and publish `verified` for
   the new generation. On failure, publish `failed`.
7. If repair requires a destructive rebuild, re-enter the normal approval, backup,
   credential/cost authorization and `rebuilding` flow.

The backup report is an exact canonical envelope with a digest of its inner manifest.
The manifest binds source generation, contract/configuration and the backup-time
writer-evidence digest. The controller receives both the backup-time evidence artifact
and its digest, validates its exact canonical schema, digest, `operation=backup`, source
generation, contract and quiescence observations, and then requires the same digest in
the manifest and every component. This evidence is distinct from the fresh attempt-bound
writer evidence used by `prepare-restore` and `restore-verify`; it need not still be
unexpired at restore time, but its issue/expiry interval must be ordered.
Each required component (`working_directory`, `milvus`, `etcd`, `minio`) contains the
archive's relative path, size and SHA-256 plus a sorted per-regular-file path/size/SHA-256
list and its aggregate digest. Validation re-opens the tar and rejects absolute, parent,
link or device members before recomputing all member hashes. Restore also receives the
expected source generation out of band so a separately self-consistent manifest copied
from another generation cannot pass. The target generation must differ from the source.
The `restore-verify` parser always emits operation `restore_verify`; no `--operation`
value can downgrade this requirement or bypass the backup gate.

## External Writer Evidence

No environment variable or controller self-assertion proves quiescence. Before each
attempt an external lifecycle/operator step selects the attempt ID, proves pipeline idle,
zero managed LightRAG replicas and disabled automatic restart, and writes a canonical
artifact containing schema/type, attempt, generation, operation, contract digest, UTC
issue/expiry times, those observations, a bounded observer identity and explicit
uncontrolled-writer attestation. The controller requires the independently supplied
artifact digest, rejects artifacts older than issue time or at/after expiry, and records
the digest/observations in every terminal report. The lifecycle helper is permitted to
create this artifact only after Docker inspection shows the service stopped/absent and
restart policy disabled; static Compose configuration or a fixed environment value is
not proof.

A normal restart without restore may retain a valid marker only when the required
generation and contract hash are unchanged and live readiness checks pass.

## Layered Readiness

Readiness is the conjunction of independent layers:

1. **Fence integrity:** canonical marker parses; state is `verified`; generation equals
   the deployment-required generation; contract hash equals the runtime-computed hash;
   referenced report is beneath `reports/`, exists, hashes correctly, has the same
   attempt/generation/contract and records every required check as true.
2. **Legacy gate:** when required, the current manifest and reconciliation digests match
   the verified report, the continuous-success watermark covers the full manifest and no
   source mutation or pending/failed target document exists.
3. **Dependency health:** etcd, MinIO and Milvus semantic health pass; Milvus reports the
   pinned supported version.
4. **Live target contract:** all three upstream-resolved collections exist, are loadable
   and retain the expected database, workspace identity, vector dimension, index and
   metric. Routine readiness does not require historical counts.
5. **LightRAG service contract:** authenticated `/auth/verify`, `/health` and
   `/documents/pipeline_status` pass; version/API, four storage classes, workspace,
   working directory, server mode/workers and recovery state match policy.
6. **Application readiness:** only after layers 1-5 pass may the Go `/readyz` knowledge
   dependency report ready. An unhealthy knowledge dependency produces explicit bounded
   degradation where allowed and never enables a SQL/pgvector fallback.

Liveness remains separate and may prove only that a process responds. No single layer,
including `/health`, Milvus `/healthz`, marker existence or CLI exit status, substitutes
for the conjunction.

## Failure, Retry And Rollback

- Cancellation and timeouts propagate to embedding and storage operations. No new target
  begins after cancellation.
- Retry is attempt-level and operator-controlled. It uses a new attempt ID and rechecks
  writer fence, source digests, contract and backup; it does not resume from self-claimed
  batch success.
- On rebuild/verification failure, keep LightRAG stopped. Because graph/KV sources are
  unchanged, the derived targets can be rebuilt again.
- Before production migration, retain the complete NanoVectorDB-era workspace and
  versioned configuration. Rollback before new Milvus-era writes restores that backup
  and the pinned Nano configuration while keeping the Milvus fence ineligible.
- After new knowledge writes are accepted, automatic rollback is forbidden. Freeze
  writers and make an explicit consistency/reconciliation decision.

## Security, Privacy And Observability

- Use a separate narrowly scoped identity to create database `lightrag`; the steady-state
  LightRAG identity receives only required database/collection permissions.
- Keep Milvus, etcd, MinIO and LightRAG on private networking. Protect the fence volume
  with least privilege; readiness consumers have read-only access.
- Encrypt backup and report storage according to the deployment retention policy.
- Never write API keys, passwords, tokens, DSNs, raw document/chunk/entity/relation
  content, embeddings, prompts, user IDs, source database row data or unbounded provider
  errors to markers, reports, logs or metrics.
- Error fields are bounded `{stage, target, code, retryable}` records. Free-form upstream
  messages are redacted or retained only in separately protected operator diagnostics.
- Metrics use fixed labels such as operation, target, state and bounded reason code.
  Attempt IDs, collection names, source keys and hashes are log/report fields, not metric
  labels.
- Record duration, source/prepared/rebuilt/skipped/duplicate counts, failed batches,
  verification outcomes and embedding usage/cost only when the provider supplies them.
  Missing usage remains unknown and is never estimated.
- Alert on `stale`, abandoned `rebuilding`, `failed`, contract/report digest mismatch,
  repeated rebuild failure, source mutation, missing collections, schema drift, recovery
  required and failed legacy reconciliation.

## Required Verification Scenarios

The implementation must prove at minimum:

- valid five-state transitions and rejection of illegal/unknown transitions;
- atomic marker replacement, concurrent-controller exclusion and crash persistence at
  every publication boundary;
- cancellation before and during each of the three official rebuild functions;
- zero exit without three valid result objects remains failed;
- malformed stats, skipped records, duplicate-policy violation and failed finalization;
- source mutation during rebuild; missing, extra and equal-count-wrong Milvus IDs;
- wrong workspace/database/model/dimension/index/metric and missing chunk collection;
- fixture retrieval/citation failure despite matching counts;
- fresh empty bootstrap, nonempty-source bootstrap rejection and nonempty orphan target;
- complete restore, partial restore rejection and stale restored-marker rejection;
- legacy import gaps, changed manifests, failed terminal documents and attempted
  retirement before the joint gate;
- normal restart and post-cutover ingestion without historical-count false failures;
- privacy assertions covering markers, reports, logs and metric labels.

Approval of this implementation contract does not authorize a production rebuild,
restore, paid embedding call, destructive target replacement, legacy-source retirement
or traffic enablement. Each rollout action still requires separate explicit approval.
