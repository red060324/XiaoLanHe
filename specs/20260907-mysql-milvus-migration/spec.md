# MySQL 8.4 + LightRAG Milvus 迁移 Spec

- Status: `APPROVED — IMPLEMENTATION AUTHORIZED`
- Owner: red060324
- Source: 2026-09-07 user correction: business database is MySQL and the
  LightRAG-supported vector database is Milvus
- Branch: `codex/clean-architecture-refactor`
- Mode: `FULL`
- Supersedes storage decisions in:
  `../20260904-advanced-ai-architecture/spec.md`

## Goal

Migrate XiaoLanHe's relational system of record from PostgreSQL/pgx to MySQL
8.4/InnoDB without weakening ownership, idempotency, inventory, one-per-user or
flash-sale concurrency guarantees. Remove the legacy PostgreSQL/pgvector knowledge
runtime and make official HKUDS LightRAG 1.5.7 the only knowledge boundary in all
modes. Configure LightRAG's vector storage as official `MilvusVectorDBStorage` while
keeping its document/KV, graph and document-status ownership intact.

The resulting service topology is:

```text
XiaoLanHe Go service
  -> MySQL 8.4: relational business data and Assistant conversation/profile memory
  -> Redis 7.4 + Lua: atomic flash-sale admission and one-user-one-order fast path
  -> RocketMQ 5.3.2: asynchronous flash-sale order processing
  -> official LightRAG 1.5.7: all knowledge ingestion, graph retrieval and citations
       -> Milvus 2.6.11: LightRAG entity/relation/chunk vectors only
       -> JsonKVStorage: LightRAG full documents/chunks/cache
       -> NetworkXStorage: LightRAG knowledge graph
       -> JsonDocStatusStorage: LightRAG ingestion status
```

## Corrected Storage Boundary

This spec replaces two earlier decisions: PostgreSQL is no longer the final business
database, and `NanoVectorDBStorage` is no longer the LightRAG vector backend. It does
not move relational business data into LightRAG and it does not treat Milvus as a
general-purpose database.

- MySQL owns accounts, sessions, catalog, community, coupons, orders, payments,
  entitlements, flash-sale durable records, conversations, summaries and explicit
  Assistant profiles.
- Redis owns only time-bounded flash-sale admission state and recovery markers.
- RocketMQ transports at-least-once reservation events; MySQL remains the durable
  idempotency and order/inventory reconciliation boundary.
- LightRAG owns knowledge documents, chunks, extraction results, graph, ingestion
  status and retrieval.
- Milvus owns only the vector projections that LightRAG creates for chunks, entities
  and relationships. The Go application never queries or writes Milvus directly.
- LightRAG KV, graph and document-status data remain on its persistent
  `WORKING_DIR`; they are not stored in the business MySQL database.

Because three LightRAG stores remain local-file implementations, this release still
uses one LightRAG service replica and one persistent workspace volume. Moving only
the vector store to Milvus removes NanoVectorDB's corpus limit but does not make the
whole knowledge system horizontally scalable or highly available. Milvus standalone
also remains a single-node deployment.

## Decisions

1. Target MySQL Community Server 8.4 with InnoDB, strict SQL mode, UTC
   `DATETIME(6)`, `utf8mb4`, foreign keys and checked constraints. Runtime access uses
   `database/sql` plus `github.com/go-sql-driver/mysql`; pgx is removed from the
   application and normal operator commands.
2. Keep the existing `XLH_DATABASE_URL` public configuration key for deployment
   compatibility, but its value becomes a MySQL DSN and startup rejects non-MySQL or
   unsafe DSNs. It must use `parseTime=true`, `loc=UTC`, bounded read/write/dial
   timeouts, `clientFoundRows=false`, and no `multiStatements=true` or
   `interpolateParams=true`. Production requires the named `tls=xlh-verified` DSN
   contract with a private connector TLS configuration that performs
   certificate and hostname verification; plaintext/`tls=skip-verify` is local-test
   only. A connector wrapper initializes and verifies `time_zone='+00:00'` and the
   versioned strict `sql_mode` on every newly opened physical connection, including
   pool growth and replacement, and startup verifies MySQL 8.4/InnoDB.
3. Create a new MySQL migration baseline and checksum namespace. PostgreSQL
   migrations 001-007 remain immutable historical input and are never executed by
   MySQL or assigned new checksums. MySQL migrations use one statement per embedded
   file so DDL boundaries are explicit.
4. The MySQL migration runner acquires a namespaced `GET_LOCK` on one fixed
   `*sql.Conn`, records `dirty` before DDL, records checksum/version success after
   DDL, and always calls `RELEASE_LOCK`. Because MySQL DDL implicitly commits, a
   dirty or checksum-mismatched version fails startup and requires an explicit
   operator repair; no atomic rollback is claimed. `GET_LOCK()` must return `1`; `0`
   is a timeout and `NULL` is an error. `RELEASE_LOCK()` must return `1`; every other
   result is surfaced. Repair is never automatic and may mark a version clean only
   after its statement-specific postcondition and checksum have been verified.
5. Repositories move from `repository/postgres` and `internal/adapter/postgres` to
   real `mysql` packages. SQL uses `?`, `sql.ErrNoRows`, `sql.Result`,
   `LastInsertId`, and typed MySQL duplicate/deadlock errors. A package named
   `postgres` must not secretly use MySQL.
6. All promotion, order, payment, Assistant profile CAS, flash-sale activation,
   reservation finalization, expiry and release-job claim transactions explicitly
   use `sql.LevelReadCommitted` to preserve the existing lock-after-wait visibility
   contract. Deadlock 1213 and lock-wait timeout 1205 are retried only around
   classified, idempotent whole-transaction closures with bounded attempts, jitter
   and context cancellation. Every failed attempt is explicitly rolled back and its
   `*sql.Tx` discarded before retry. Retry closures contain no Redis, RocketMQ,
   payment-provider or other external side effects. An ambiguous commit is reconciled
   in a fresh transaction by a durable idempotency key and is never blindly replayed.
7. PostgreSQL business advisory locks are replaced by durable InnoDB row locks:
   user-scoped coupon/order/payment operations lock the existing `user_account` row;
   flash-sale activation locks one unique scope row keyed by edition, region and
   currency. Activity creation atomically upserts that scope row in the same
   transaction; migration backfills every distinct existing scope before enabling
   writes. Activation locks scope before activity rows in the global lock order.
   MySQL `GET_LOCK` is used only by the migration runner, never as a pooled request
   lock.
8. PostgreSQL partial uniqueness is recreated explicitly. MySQL generated nullable
   discriminator columns enforce one active price and one active entitlement; native
   nullable unique keys enforce optional order, coupon and reservation references.
   Status-prefixed ordinary indexes replace partial query indexes.
9. Application-normalized identifiers use an explicit binary collation so MySQL's
   default case/accent folding cannot broaden equality. Usernames/slugs/edition codes
   remain lowercase ASCII; coupon/activity/currency/region codes remain uppercase
   ASCII. Display and free-text columns keep a Unicode collation.
10. PostgreSQL `RETURNING`, `ON CONFLICT`, JSONB operators, `LATERAL`, `ANY`,
    aggregate `FILTER`, `bool_or`, `UPDATE ... FROM`, modifying CTEs, vector casts and
    advisory locks are redesigned, not mechanically translated. Critical upserts do
    not use `INSERT IGNORE`; IDs come from the same `sql.Result` or are re-read by a
    unique key in the same transaction.
11. The legacy `knowledge_document`/`knowledge_chunk`/pgvector path leaves the
    application runtime. Basic and advanced Assistant knowledge search both use the
    official LightRAG client; disabling the advanced Multi-Agent path changes
    orchestration, not the knowledge owner. The old PostgreSQL knowledge source may
    remain only in a separately built migration utility during the cutover window.
12. Pin official LightRAG 1.5.7/API 0344 and its existing immutable image digest. Set
    `LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage`,
    `MILVUS_URI=http://milvus:19530`, `MILVUS_DB_NAME=lightrag`, common
    `WORKSPACE=xiaolanhe_v1`, `AUTOINDEX` and `COSINE`. Do not set
    `MILVUS_WORKSPACE`, so it inherits the common workspace.
13. Run Milvus standalone 2.6.11 with etcd 3.5.25 and MinIO
    `RELEASE.2025-09-07T16-13-09Z`. Persist and back up all three data volumes. Milvus
    readiness uses `http://localhost:9091/healthz`; etcd and MinIO have semantic
    health probes. Port 19530 is private to the Compose network.
14. Migrating an existing LightRAG workspace from NanoVectorDB to Milvus requires a
    controlled offline rebuild: stop every LightRAG writer, retain the exact
    `WORKSPACE`, `WORKING_DIR`, KV, graph, embedding model/dimension/prefixes, select
    Milvus, invoke the three pinned official rebuild library functions, verify all
    three vector stores, and atomically publish a deployment-owned `verified` fence
    before starting the server. A configuration flip without rebuild is forbidden.
15. The embedding contract remains `text-embedding-v4`, dimension 1024 for this
    migration, with `EMBEDDING_SEND_DIM=false`, `EMBEDDING_ASYMMETRIC=false`, and both
    document/query prefixes unset. Any change to provider/binding/endpoint semantics,
    model, dimension, send-dimension flag, asymmetric mode, document prefix or query
    prefix invalidates the fence and requires a full three-target rebuild/re-ingestion,
    even when the model/dimension-derived collection name changes. A rebuild performs
    real embedding calls and therefore needs separate credential/cost authorization.
16. Redis Lua, RocketMQ transactional delivery, compensation workers and the
    Assistant's permanent read-only tool boundary remain intact. No Agent gains
    coupon, order, payment, reservation, post or comment mutation capability.
17. The migration changes storage implementation, not public REST/SSE payloads. IDs,
    money, timestamps, error codes, idempotency keys and async flash-sale/knowledge
    behavior remain compatible.
18. This repository supplies fresh-install MySQL schema, an explicit PostgreSQL to
    MySQL cutover tool/runbook, reconciliation reports and rollback steps. It does not
    silently auto-copy production data during application startup.
19. Existing PostgreSQL `knowledge_document` rows must be frozen, manifested, imported
    and reconciled through the official LightRAG API before their source tables are
    declared retired. Source keys remain `xlh-legacy-<id>.txt`; LightRAG re-chunks and
    re-embeds content, so legacy `knowledge_chunk` rows and 1536-dimensional pgvector
    values are deliberately discarded as derived data. The source tables remain
    read-only through the rollback window; physical deletion needs separate approval.
20. Milvus database `lightrag` is created by an explicit deployment init step using a
    narrowly scoped bootstrap identity. The steady-state LightRAG identity is granted
    only the permissions needed for its database/collections and does not depend on
    database-create privilege.
21. Go-to-LightRAG transport defaults to HTTPS: `XLH_LIGHTRAG_BASE_URL` defaults to
    `https://127.0.0.1:9621` and `XLH_LIGHTRAG_ALLOW_INSECURE` defaults to `false`. An
    HTTP URL is accepted only when the override strictly parses as boolean `true`, for
    an explicitly controlled local environment. Production uses HTTPS and keeps the
    override false so the LightRAG API key and query/request bodies are not sent over
    cleartext transport. Runtime and migration-tool configuration loaders enforce the
    rule, and both the shared repository client and migration verifier reject an
    unapproved HTTP URL at their own construction boundary. URL credentials, query
    strings and fragments remain rejected.
22. Runtime dependency readiness has one bounded overall response budget and gives
    every registered dependency its own deadline; checks run concurrently so one slow
    dependency cannot consume the budget of a later dependency. Cancellation is
    signaled to every outstanding check and public responses never expose provider
    details. The pinned RocketMQ admin API does not propagate its context internally,
    so its adapter permits at most one residual probe until that SDK call returns.
    RocketMQ startup is not wrapped in an orphanable timeout goroutine: the
    pinned SDK's synchronous `Start` remains on the startup goroutine, and because v2.1.2
    exposes no total cancellation hook, a separate hard-deadline watchdog fail-stops the
    whole process rather than allowing a timed-out client to continue. Container smoke
    must traverse the Go knowledge and baseline
    Assistant routes into LightRAG and verify knowledge-admin authorization. A
    flash-sale smoke is evidence only when it runs against real Redis and RocketMQ; an
    unavailable integration environment is reported as not run, never as pass.
23. Flash-sale dependency configuration also fails closed when the feature is enabled.
    Redis must use an authenticated `rediss://` URL unless
    `XLH_REDIS_ALLOW_INSECURE=true` explicitly permits plaintext `redis://` in a
    controlled local/CI environment. RocketMQ must receive a non-empty, valid access
    key and secret key pair unless `XLH_ROCKETMQ_ALLOW_INSECURE=true` explicitly
    permits the local ACL-free broker. Partial credential pairs are always rejected,
    even with the override. Both overrides default to `false`, parse strictly as
    booleans, and are ignored while `XLH_FLASH_SALE_ENABLED=false` so the opt-in
    feature remains dependency-free when disabled.
24. `GET /api/knowledge/search` remains intentionally unauthenticated, but its entry
    boundary applies one process-wide, constant-memory token bucket and one concurrent
    execution semaphore after validating the public query and before calling the
    knowledge UseCase/LightRAG. The safe default
    is one request per second, burst five and four in-flight searches. Rate or
    concurrency rejection returns `429 capacity_exceeded` with an integer-seconds
    `Retry-After`; cancellation is observed before provider invocation and every
    acquired slot is released. The guard is global rather than keyed by caller, so it
    neither allocates unbounded per-client state nor trusts spoofable
    `X-Forwarded-For`. A future trusted-proxy policy is required before any client-keyed
    limit may use forwarded addresses. Multi-process or multi-replica aggregate budgets
    require a shared edge/distributed limiter and are not claimed by this in-process
    release.
25. Rebuild, bootstrap, revalidation and restore verification never trust a process
    environment variable as proof that writers stopped. Every attempt uses an
    externally produced, checksum-bound writer-evidence artifact whose exact schema
    binds the attempt ID, target generation, operation, contract digest, issue/expiry
    times, pipeline-idle observation, zero managed server replicas, disabled automatic
    restart and an explicit bounded attestation that no uncontrolled writer remains.
    The isolated lifecycle runner may create this artifact only after inspecting that
    the managed service is stopped/absent and cannot auto-restart. Missing, expired,
    wrong-attempt, wrong-generation, wrong-operation, wrong-contract or digest-mismatched
    evidence fails before a target is opened or mutated.
26. The raw-volume backup is one checksum-bound consistency-unit manifest, not four
    merely nonempty tar files. It binds the source generation and contract/configuration
    digest and records, for the LightRAG working directory plus Milvus, etcd and MinIO,
    each archive's byte size/SHA-256 and every regular member's normalized relative
    path, byte size and SHA-256. Restore verifies the manifest and every archive/member
    before extraction, restores only into fresh isolated volumes, uses a new deployment
    generation, first publishes `stale/restore_pending_verification`, and can publish
    `verified` only through the `restore-verify` command with operation fixed to
    `restore_verify`. Missing components, truncated archives, unsafe members, mixed
    generations or a restored old `verified` marker are rejected.
27. Fence markers and immutable reports use an exact versioned schema shared by the
    Python controller and Go readiness reader. Commands map to one operation and cannot
    override it. State transitions are explicitly allowlisted; acquiring the controller
    lock while `rebuilding` is current records a schema-complete immutable
    `failed/abandoned_rebuilding` report before a new attempt begins. Before publishing
    `rebuilding`, the controller durably creates an exact, immutable, contract-bound
    attempt journal so recovery remains correct after runtime contract drift and can
    idempotently finish publication after a report-before-marker crash. A verified report
    has exact operation/reason/timestamps, writer and backup evidence references,
    source snapshots, per-target exact schema/ID evidence, rebuild statistics, fixture
    and legacy evidence, all required checks, empty bounded errors and successful
    cleanup. Missing, extra or inapplicable-field misuse fails readiness.
28. The pinned LightRAG namespace/schema is verified exactly in Milvus database
    `lightrag`: `xiaolanhe_v1_{entities,relationships,chunks}_text_embedding_v4_1024d`.
    Every collection has dynamic fields enabled and the common non-null fields
    `id VARCHAR(64) PRIMARY KEY`, `vector FLOAT_VECTOR(1024)` and `created_at INT64`.
    Entity fields are nullable `entity_name VARCHAR(512)`, `content VARCHAR(65535)`,
    `source_id VARCHAR(65535)`, `file_path VARCHAR(32768)`; relationship fields are
    nullable `src_id VARCHAR(512)`, `tgt_id VARCHAR(512)`, `content VARCHAR(65535)`,
    `source_id VARCHAR(65535)`, `file_path VARCHAR(32768)`; chunk fields are nullable
    `full_doc_id VARCHAR(64)`, `content VARCHAR(65535)`, `file_path VARCHAR(32768)`.
    Field-set, type, length, nullability, primary-key, vector index `AUTOINDEX` and
    metric `COSINE` drift all fail. Rebuild duplicates default to rejection for every
    target; nonzero duplicates are accepted only by a separate checksum-bound,
    attempt/generation/contract/source-bound reviewed policy artifact with named
    approver, expiry and per-target maximum.
29. Executing relational `copy` or `resume` requires an externally issued Ed25519
    write-freeze attestation with schema `xlh.postgres_write_freeze.v1`. Its signed
    canonical payload binds attestation ID, issuer, issue/expiry time, trusted key ID,
    operator cluster ID, observed source database/schema/snapshot digests, stopped
    application writers, stopped background workers, drained CDC/outbox and zero active
    business writers. The tool rejects stale, source-mismatched, incomplete, unknown-field
    or incorrectly signed evidence before a MySQL data write and revalidates the frozen
    source and the same evidence before completion. The verifier key is supplied either
    as raw-base64 environment data or through a non-symlink regular file owned by the
    effective UID with mode `0400` or `0600`; file validation and reading use one open
    descriptor. `verify` requires the trusted key and key ID to validate the attestation
    embedded in the manifest and the immutable referenced reconciliation report, but it
    takes no external attestation path and remains strictly read-only. Target migration
    provenance is derived from the exact embedded migration filenames/names/SHA-256 values
    and matched against the live `schema_migration` rows and target schema; no operator
    supplied target-migration commit is accepted.
30. Executing relational `copy` or `resume` also requires a separately trusted,
    externally issued Ed25519 target-writer attestation with schema
    `xlh.mysql_target_writer_fence.v1`. Its canonical signed payload binds the target
    MySQL instance/database, exact migration and schema identity (and its SHA-256), the
    deployment generation, issue/expiry interval, trusted key ID, zero application and
    background writers, disabled automatic restart and disabled write traffic. The
    source-freeze and target-fence keys, key IDs, artifacts and CLI inputs are not
    interchangeable. The tool reloads and authenticates the target fence against the
    live target before every target mutation, including copied/deferred batches and
    auto-increment advancement, and again before publishing a successful reconciliation
    or `Completed/CutoverReady`. A resume may use a freshly issued fence only when its
    generation and target identity still match; the newly accepted canonical evidence
    replaces the manifest binding before any resumed target write. The manifest embeds
    the complete signed evidence, exact canonical payload bytes and SHA-256 digests of
    both payload and attestation. Read-only `verify` accepts no target evidence path and
    authenticates only that embedded evidence with the independently supplied trusted
    target public key, key ID and expected deployment generation. MySQL `GET_LOCK`
    serializes migration tools only and is never evidence that application writers are
    stopped.

## Scope

### In scope

- MySQL connection/config validation, pool lifecycle, readiness and migration runner.
- A complete MySQL 8.4 schema covering currently used relational tables, constraints,
  generated uniqueness columns, indexes and Assistant memory fields.
- MySQL repositories for account, conversation, catalog, community, promotion,
  order/payment, flash sale and Assistant memory/profile.
- MySQL seed command and an operator-invoked PostgreSQL-to-MySQL data migration path
  with dry run, checkpoints, validation and reconciliation.
- An operator-only legacy PostgreSQL knowledge import with immutable source manifest,
  continuous-success watermark, target reconciliation and explicit retirement gate.
- Removal of pgx/pgvector and local knowledge retrieval from the normal runtime.
- LightRAG/Milvus/etcd/MinIO Compose, configuration, health/readiness, persistence,
  backup/restore and NanoVectorDB-to-Milvus rebuild tooling/runbook.
- Strict external writer/backup/duplicate-policy evidence, legal fence transitions,
  schema-complete immutable reports and matching Python/Go readiness validation.
- MySQL and Milvus updates to CI, local verification, environment samples, Render
  limitations, README, architecture and deployment documentation.
- Regression and concurrency verification for Redis Lua, RocketMQ and all durable
  MySQL finalization paths.
- Fail-closed production Redis transport and RocketMQ authentication configuration,
  with explicit local/CI-only insecure opt-ins.
- Constant-memory rate and concurrency protection for the unauthenticated public
  knowledge search endpoint, including cancellation and `Retry-After` behavior.

### Non-goals

- No business tables in LightRAG or Milvus.
- No LightRAG KV, graph or document-status tables in business MySQL.
- No direct Go-to-Milvus repository or custom vector search implementation.
- No continued PostgreSQL/pgvector fallback after cutover and no ongoing dual write.
- No attempt to translate pgvector bytes into Milvus; vectors are rebuilt from
  LightRAG graph and text-chunk sources using the pinned embedding configuration.
- No switch from Redis Lua to database-only stock deduction and no removal of
  RocketMQ.
- No Milvus distributed cluster, multi-replica LightRAG, zero-downtime data-store
  cutover or high-availability claim in this release.
- No automatic live production migration, DNS switch, cloud purchase, destructive
  PostgreSQL retirement or paid embedding execution without separate authorization.
- No Agent side effects and no unrelated product/UI redesign.
- No per-IP/per-user accounting, trusted-proxy parsing, distributed quota service or
  durable billing ledger for public knowledge search in this release.

## Current-State Evidence

### Reusable

- Module UseCase/Entity ports already isolate most business behavior from persistence.
- Redis Lua admission/release scripts, RocketMQ transport, recovery/expiry workers,
  idempotency keys and HTTP contracts remain the intended flash-sale architecture.
- The official LightRAG HTTP adapter, knowledge facade, source-key namespace,
  Multi-Agent contracts, Skills, layered-memory behavior and deterministic evals are
  reusable after their storage expectation changes to Milvus.
- The pinned LightRAG 1.5.7 server already supports `MilvusVectorDBStorage`; no fork
  or LightRAG upgrade is required.

### Migration debt to replace

- The composition root, seed/import commands and all relational repositories use pgx
  and PostgreSQL SQL directly.
- Migrations 001-007 use PostgreSQL-only types, pgvector, PL/pgSQL, partial/expression
  indexes and transactional migration assumptions.
- Promotion/order/flash-sale correctness uses PostgreSQL advisory locks and default
  Read Committed behavior.
- The legacy knowledge adapter uses pgvector casts and cosine distance.
- Compose, GitHub Actions, environment examples, Render Blueprint and deployment docs
  provision or describe PostgreSQL/pgvector.
- LightRAG deployment and readiness currently require `NanoVectorDBStorage` and
  explicitly reject Milvus configuration.

### Verified upstream constraints

- LightRAG 1.5.7 resolves to commit
  `28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c` and declares
  `pymilvus>=2.6.2,<4.0.0`.
- Its official CPU standalone template uses Milvus 2.6.11, etcd 3.5.25 and the pinned
  MinIO release selected here.
- The Milvus adapter derives vector dimension from the embedding function, checks an
  existing collection for dimension mismatch, and defaults to `AUTOINDEX`/`COSINE`.
- At the pinned commit its collection suffix is the sanitized embedding model plus
  dimension (`text_embedding_v4_1024d`); the three exact namespaces and field contracts
  are fixed in Decision 28, including `created_at`, nullability and VARCHAR limits.
- The official rebuild tool treats graph nodes/edges and `text_chunks` KV as
  authoritative and rebuilds entity, relationship and chunk vector stores. It requires
  all writers to stop and warns that no persisted rebuild-readiness marker exists.

## Acceptance Criteria

- **AC1 — MySQL-only business runtime:** A clean application startup, seed and all
  normal runtime paths use MySQL 8.4 through `database/sql`; no production package or
  runtime dependency imports pgx, queries PostgreSQL or requires pgvector.
- **AC2 — Faithful MySQL schema:** Fresh MySQL migration creates all required tables,
  foreign keys, checks, generated/nullable uniqueness rules and indexes. UTC
  `DATETIME(6)`, JSON and binary digest round trips preserve public behavior.
- **AC3 — Fail-closed migration history:** Fresh, repeat, concurrent, checksum
  mismatch, dirty-state and interrupted-DDL cases prove a single fixed-connection
  migration lock, exact `GET_LOCK`/`RELEASE_LOCK` result handling, explicit dirty
  repair authorization/postconditions and no false atomicity claim.
- **AC4 — Relational behavior compatibility:** Account, conversation, catalog,
  community, promotion, order/payment, entitlement, Assistant memory/profile and seed
  behavior pass against MySQL with unchanged external contracts.
- **AC5 — Transactional invariants:** Under concurrent MySQL 8.4 execution there is
  no coupon oversell, duplicate active price/entitlement, cross-user mutation, stale
  price acceptance, duplicate order/payment or overlapping active flash-sale window.
  An active entitlement collision is a replay only when the paid order,
  `source_order_id`, provider reference and payment idempotency identity agree; a
  different order conflicts and rolls back. Required transactions use Read Committed,
  deterministic row-lock order and bounded side-effect-free whole-transaction retry.
- **AC6 — Flash-sale integrity:** Redis Lua still atomically checks active window,
  stock and one-user-one-order; RocketMQ remains at-least-once; MySQL idempotently
  finalizes reservations/orders and durable compensation. Multi-worker `SKIP LOCKED`,
  expiry, retry and Redis/MySQL reconciliation tests pass.
- **AC7 — LightRAG-only knowledge:** Basic and advanced knowledge retrieval plus
  create/list/status/delete use only the official LightRAG API. The application has no
  local relational knowledge/vector store, hidden PostgreSQL fallback or direct Milvus
  client.
- **AC8 — Official Milvus vector backend:** Combined application and deployment
  readiness proves LightRAG 1.5.7/API 0344 with `JsonKVStorage`,
  `MilvusVectorDBStorage`, `NetworkXStorage`, `JsonDocStatusStorage`, workspace
  `xiaolanhe_v1`, configured embedding `text-embedding-v4`/1024, a matching live
  collection schema and healthy Milvus 2.6.11. The authenticated LightRAG health
  contract, pinned configuration, Milvus health and collection inspection are distinct
  evidence; `/health` alone is not claimed to expose all of them. NanoVectorDB is
  rejected. Milvus database bootstrap uses a separate least-privilege init identity.
  Operator inspection requires database `lightrag`, the three exact pinned final
  namespaces and exact field/type/length/nullability/primary/dynamic/index/metric
  contracts; substring/suffix collection discovery is not evidence.
- **AC9 — Milvus data lifecycle:** A live isolated run ingests a relationship corpus,
  retrieves entity/relation/chunk evidence in supported modes, survives clean restart,
  and restores from a backup containing the LightRAG workspace plus Milvus, etcd and
  MinIO volumes. The backup manifest binds source generation/configuration and archive
  plus per-file sizes/digests. Restore uses fresh volumes and a new generation, publishes
  `restore_pending_verification` before checking, and forces `operation=restore_verify`;
  missing, truncated or mixed-generation inputs fail before LightRAG starts.
- **AC10 — Controlled vector migration:** An existing NanoVectorDB workspace is
  migrated only while all controlled writers are stopped. The deployment fence moves
  through `absent/stale/rebuilding/failed/verified`, and only `verified` for the exact
  deployment generation and embedding contract is readiness-eligible. The pinned
  official entity, relationship and chunk rebuild functions regenerate all three
  Milvus stores from unchanged graph/KV sources; their structured stats, exact expected
  ID sets, schemas/indexes, retrieval and citations pass before restart. Interrupted or
  partially verified rebuild remains unready and safely rerunnable. Writer quiescence is
  proven only by unexpired external attempt/generation/contract-bound evidence, never a
  fixed environment assertion. Legal transitions and abandoned attempts produce strict
  immutable reports. Duplicate rebuild rows fail by default unless an explicit reviewed
  policy artifact authorizes a bounded target count. Python and Go readiness both reject
  any marker/report schema drift or incomplete applicable evidence.
- **AC11 — Safe data cutover:** PostgreSQL-to-MySQL tooling is operator initiated,
  resumable and audited. It preserves IDs and UTC times, adjusts auto increments,
  handles cyclic foreign keys in a documented order, and reconciles row counts,
  constraints, inventory, claims, orders, payments, entitlements and flash-sale state
  before cutover. Every checkpoint is bound to source identity/snapshot and schema, tool
  commit, exact embedded/live target migration provenance, target schema, batch digests
  and second-phase FK status. Execute requires both the approved external signed source
  write-freeze contract and the independently trusted target-writer fence bound to the
  live target identity and deployment generation. The target fence proves zero
  application/background writers, disabled restart and disabled write traffic, is
  reloaded before every target mutation, and is revalidated with the live target before
  success publication. Read-only verify independently authenticates both
  manifest-embedded evidence records and the immutable report using only their separate
  out-of-band keys/key IDs (plus the expected target generation), without accepting
  either external attestation path or creating, replacing or updating artifacts. Legacy
  knowledge uses a separate immutable
  manifest/import/reconciliation gate and cannot be skipped. No application-startup dual
  write or automatic destructive cleanup exists.
- **AC12 — Deployment and security:** Local/CI manifests pin MySQL, LightRAG, Milvus,
  etcd and MinIO versions; credentials are not defaults in production, data ports bind
  only to loopback or private networks, health probes are semantic, all stateful volumes
  are persistent and resource/backup requirements are documented. Production MySQL
  TLS verifies the server certificate/hostname; DSN and connection-session invariants
  hold for every physical connection. The application defaults its LightRAG endpoint
  to HTTPS, rejects HTTP without an explicit `XLH_LIGHTRAG_ALLOW_INSECURE=true` in both
  runtime and importer paths, rejects malformed boolean overrides, and enforces the rule
  again at both client constructors. The override is documented as local/controlled-only;
  production keeps it false so API credentials and request bodies are not cleartext.
  When flash sale is enabled, production also rejects plaintext `redis://` unless the
  local/CI-only Redis override is explicitly true, and rejects missing or partial
  RocketMQ ACL credentials unless the ACL-free local/CI override is explicitly true.
  Disabling flash sale continues to require neither dependency nor either override.
- **AC13 — Compatibility and safety:** Existing REST/SSE, auth, error, timestamp,
  idempotency and async contracts remain compatible. All Agent tools remain read-only;
  Redis/RocketMQ outage and LightRAG/Milvus outage produce explicit bounded degradation
  without unsafe fallback. Public knowledge search remains unauthenticated while a
  bounded global token bucket and in-flight cap reject excess work before LightRAG,
  return `429 capacity_exceeded` plus `Retry-After`, preserve cancellation and maintain
  constant limiter memory independent of caller-controlled headers.
- **AC14 — Verification and delivery:** MySQL unit/integration/race, Redis/RocketMQ,
  official LightRAG/Milvus lifecycle, frontend, eval, architecture, static, image and
  documentation gates pass with no required skip. The final report maps every criterion
  to executed evidence and lists environment-only rollout work separately.

## Assumptions And Open Questions

- This migration targets a fresh install plus a separately rehearsed cutover from an
  existing PostgreSQL instance. Exact production dataset size and downtime budget are
  unknown, so the approved default is a short write freeze and bulk copy, not dual
  write or CDC. If measured downtime is unacceptable, CDC requires a new reviewed plan.
- Render cannot provision this complete production topology. The checked-in app-only
  Blueprint therefore lives at `deploy/render.app-only.example.yaml`, outside the
  auto-discovered repository-root path, and uses placeholders for an external MySQL
  DSN, TLS files and the read-only LightRAG fence mount. Copying/enabling it is a
  separately reviewed rollout action; choosing/buying providers is not an
  implementation assumption.
- Local/CI Milvus uses standalone CPU mode. A managed or distributed Milvus endpoint may
  be substituted later through the same LightRAG environment contract after its own
  backup, auth, TLS and availability review.
- Live vector rebuild and end-to-end ingestion need valid LLM/embedding credentials and
  incur provider cost. Code/static tests may be completed without them, but AC9/AC10
  cannot be marked passed until an authorized live run is captured.
- PostgreSQL source access may remain available to the isolated cutover/import binary
  during the migration window. It is not linked into or configured by the normal
  XiaoLanHe server binary.
- The deployment can prove that the managed LightRAG service is scaled to zero and
  restart-disabled during rebuild, but it cannot detect arbitrary external writers.
  Operator sign-off is therefore part of the writer-fence evidence and no stronger
  global-writer claim is made.

## Clarify Decisions

- 2026-09-07: user explicitly approved the MySQL 8.4 business database plus official
  LightRAG `MilvusVectorDBStorage` architecture and authorized implementation.
- 2026-09-07: LightRAG application transport was hardened to default/recommend HTTPS;
  cleartext HTTP requires an explicit local/controlled-environment opt-in.
- 2026-09-07: Redis plaintext transport and RocketMQ without ACL were restricted to
  explicit local/CI opt-ins; production flash-sale configuration now fails closed.
- 2026-09-07: discovered public-search cost exposure was added to the approved scope;
  the endpoint stays public but receives deterministic process-local rate and
  concurrency admission with no spoofable client identity.
- 2026-09-07: relational execute was bound to an external signed write-freeze
  attestation; verify was fixed as authenticated and strictly read-only; arbitrary
  `--target-migration-commit` input was removed in favor of embedded/live migration
  provenance.
- Production infrastructure purchase/provisioning, destructive source retirement and
  paid live embedding/rebuild remain separate rollout approvals.
