# Technical Plan

- Status: `IMPLEMENTED — LOCAL CI PASS; PRE_MERGE ENVIRONMENT BLOCKED`
- Authoritative spec: `./spec.md`

## Selected Runtime Design

```text
Browser
  -> XiaoLanHe Go HTTP/SSE
     -> module Entry -> UseCase -> Entity/ports
        -> MySQL repositories -> MySQL 8.4/InnoDB
        -> flash-sale admission -> Redis 7.4 Lua
           -> transactional publish -> RocketMQ 5.3.2
           -> consumer -> MySQL finalization -> Redis completion/release
        -> Assistant orchestration -> official LightRAG HTTP API
           -> JsonKVStorage on persistent WORKING_DIR
           -> NetworkXStorage on persistent WORKING_DIR
           -> JsonDocStatusStorage on persistent WORKING_DIR
           -> MilvusVectorDBStorage -> Milvus 2.6.11
                                      -> etcd 3.5.25
                                      -> MinIO pinned release
```

MySQL and LightRAG are separate systems of record. The Go knowledge adapter knows
LightRAG HTTP contracts only. Milvus credentials, namespaces, collections and schemas
stay behind LightRAG. Redis admission is a fast atomic reservation boundary; durable
business reconciliation remains in MySQL.

## Relational Adapter And Dependency Flow

The UseCase/Entity ports remain provider neutral. New repositories live at:

```text
internal/adapter/mysql              # pool setup helpers and migration runner
internal/account/repository/mysql
internal/catalog/repository/mysql
internal/community/repository/mysql
internal/promotion/repository/mysql
internal/order/repository/mysql
internal/flashsale/repository/mysql
internal/assistant/repository/mysql
```

`cmd/xiaolanhe` is the sole runtime composition root. It opens one `*sql.DB`, applies
the MySQL migration set, validates server/session invariants, injects MySQL stores and
registers `PingContext` readiness. Provider types and SQL stay inside repositories.
Entry, Presenter, UseCase and Entity APIs remain unchanged unless a test exposes a
previously provider-specific leak.

The legacy PostgreSQL-to-LightRAG importer becomes an isolated migration command with
an explicit source DSN and a build/runtime dependency that is not imported by the
server. After its retirement window, pgx can be removed entirely; if retained for the
operator command, dependency/static gates distinguish that tool from the runtime.

## MySQL Connection Contract

The public variable stays `XLH_DATABASE_URL`, now containing a MySQL DSN such as:

```text
xlh:secret@tcp(mysql:3306)/xiaolanhe?parseTime=true&loc=UTC&charset=utf8mb4&timeout=5s&readTimeout=5s&writeTimeout=5s
```

Startup parses rather than string-matches the DSN and rejects:

- absent database name, network address or credentials where required;
- `parseTime` other than true or `loc` other than UTC;
- `multiStatements=true`, `interpolateParams=true`, `clientFoundRows=true` or
  unbounded/missing timeouts;
- an unsupported server version, non-InnoDB defaults, non-UTC session timezone or
  missing strict SQL mode.

The application wraps the MySQL connector so every newly opened physical connection
executes `SET time_zone='+00:00'` and the versioned required session `sql_mode` before
it enters the pool. Startup also reads both values back on a dedicated connection. Pool
size, lifetime and idle settings are bounded through config. Every stored time is
converted to UTC before write and scanned from `DATETIME(6)`.

Production requires the named `tls=xlh-verified` DSN contract and attaches a private
`crypto/tls` configuration with CA roots, minimum TLS version and server-name
verification directly to each connector, avoiding process-global profile collisions.
`tls=false`, omitted TLS and `tls=skip-verify` are accepted only by explicitly local
test configuration. Pool-growth tests open more physical connections than the idle
capacity and verify every connection's timezone/sql-mode/TLS properties. Keeping
`clientFoundRows=false` makes `RowsAffected` mean rows actually changed; repositories
that distinguish insert/update/no-op assert that semantic instead of assuming matched
rows.

## Migration Design

### Independent history

PostgreSQL `migrations/001_initial_schema.sql` through
`007_advanced_ai.sql` stay untouched. MySQL receives a new embedded namespace, for
example `migrations/mysql/*.sql`, beginning at a fresh baseline. The MySQL
`schema_migration` table contains version/name, checksum, dirty flag, started/completed
timestamps and optional failure context. Old PostgreSQL checksums never appear in it.

Each embedded MySQL file contains exactly one driver-executable statement. This avoids
enabling `multiStatements`, prevents delimiter/procedure parsing ambiguity and makes
the point of failure observable. Logical releases may comprise several ordered files.

### Lock and dirty protocol

1. Acquire a dedicated `*sql.Conn`.
2. Call `SELECT GET_LOCK('xiaolanhe:mysql:migrate:v1', timeout)` on that connection and
   require an integer result of `1`; treat `0` as lock timeout and SQL `NULL` as error.
3. Bootstrap and verify the exact `schema_migration` metadata table while holding the
   lock; the bootstrap definition itself is versioned in code and cannot drift silently.
4. Verify that no dirty row exists and all applied checksums match.
5. Insert/update the next migration row as dirty in autocommit mode.
6. Execute its one DDL/DML statement and validate postconditions when required.
7. Mark the row clean with completion time.
8. Repeat, then call `RELEASE_LOCK` in a defer on the same connection and require `1`.
   `0`, `NULL`, connection loss or scan error is reported as an operator-visible
   migration failure even when statements already completed.

If a process dies after implicit DDL commit but before clean marking, the next startup
fails closed. DML-only statements may use an explicit transaction, but DDL is never
presented as rollback-safe. A separate inspect/repair command is dry-run by default,
requires the exact version/checksum plus an explicit acknowledgement, verifies that
statement's information-schema/data postcondition, and emits an audit report before it
may mark that exact version clean or run a versioned compensating statement. It never
blindly reruns DDL and the application never guesses.

### Strict CHECK metadata canonicalization

Repair and postcondition verification read CHECK expressions from
`information_schema.CHECK_CONSTRAINTS.CHECK_CLAUSE`. MySQL 8.4 can serialize a source
literal such as `'active'` as `_utf8mb4\'active\'`. The expression lexer maps both the
ordinary `_utf8mb4'...'` representation and this escaped-delimiter representation to
the same string token used for migration source comparison. The introducer must equal
`_utf8mb4` case-insensitively; prefix matches such as `_utf8mb4evil`, other charsets,
separated introducers, raw quotes inside the escaped form and unterminated literals are
errors. The escaped form follows the exact two-layer MySQL 8.4 emitter grammar: slash
runs before a quote distinguish a closing delimiter from a value apostrophe and any
preceding value backslashes; four slashes encode one value backslash; doubled slashes
plus `0`, `n`, `r` or `Z` encode the control byte escaped by `String::print`. Raw bytes
that the emitter must escape, impossible slash-run remainders and unknown outer escapes
are rejected, so no lossy slash removal can collapse different literal values.

This normalization is local to the CHECK expression grammar. It does not rewrite the
raw metadata string, change SQL mode, modify historical migrations or weaken the
expected constraint. Unsupported syntax remains fail-closed, and the canonical values
of different literal contents must remain different so repair cannot bless tampering.

## MySQL Data Model And SQL Translation

The exact schema is in `data-model.md`. Core mappings are:

| PostgreSQL | MySQL 8.4 strategy |
|---|---|
| `bigserial` | `BIGINT AUTO_INCREMENT` |
| `timestamptz` | UTC `DATETIME(6)` |
| `jsonb` | `JSON` plus Go validation |
| `bytea` SHA-256 | `BINARY(32)` |
| `$n` | `?` |
| `RETURNING` | `sql.Result.LastInsertId` or same-transaction unique-key read |
| `ON CONFLICT` | explicit `ON DUPLICATE KEY UPDATE` or lock/read/insert branch |
| partial unique index | nullable column or stored generated discriminator + UNIQUE |
| `LATERAL`/`ANY` | window/correlated query and bounded generated `IN` list |
| JSONB operators | `JSON_EXTRACT/SET/REMOVE/CONTAINS_PATH` |
| `FILTER`/`bool_or` | `SUM/MAX(CASE WHEN ...)` |
| `UPDATE FROM` | `UPDATE ... JOIN` |
| modifying CTE + returning | locked ID select, update-by-ID, select results |
| pgvector | removed; knowledge goes through LightRAG |

Dynamic `IN` placeholders are generated only from a bounded integer count; values
remain parameters. MySQL 8.4 row-alias upsert syntax is used instead of deprecated
`VALUES(column)`. `INSERT IGNORE` is prohibited in correctness-sensitive paths because
it can mask warnings beyond duplicate keys.

## Transaction And Concurrency Design

### Isolation and retry

A shared helper runs classified business transactions at
`sql.LevelReadCommitted`. It retries the complete closure only for MySQL 1213/1205,
only where the command is idempotent, up to a small fixed attempt cap with bounded
jitter and the original context deadline. Duplicate-key, validation and unknown errors
are not generically retried. Before any retry, the failed transaction is explicitly
rolled back and discarded; a new transaction and all reads are acquired again. Retry
closures may contain database work only and cannot publish MQ events, mutate Redis,
call a payment provider or perform any other external side effect. Commit ambiguity
returns an explicit retry/reconcile outcome and a fresh transaction resolves it by the
operation's durable idempotency key.

### Row-lock replacement

- Coupon claim, order creation, payment and flash-sale order finalization lock
  `user_account.id` first. Every multi-resource path follows a documented lock order:
  user -> activity/scope -> coupon/price -> claim/order/reservation -> entitlement.
- Flash-sale activation obtains or reads one `flash_sale_scope_lock` row keyed by
  `(edition_id, region_code, currency)`. Activity creation performs an idempotent
  `INSERT ... ON DUPLICATE KEY UPDATE` of that row in the same transaction; activation
  locks scope first with `FOR UPDATE`, then locks/checks activity rows. The MySQL
  baseline/cutover backfills all distinct activity scopes before writes are enabled;
  the scope FK is `ON DELETE RESTRICT`.
- Post/comment mutation locks the target published row before dependent writes.
- Profile compare-and-set locks the user's profile row after its stable user row when
  insertion races are possible.
- Payment entitlement creation handles unique-key collision as replay only after a
  fresh locked read proves the existing active entitlement has the same
  `source_order_id`, that order is paid, and the payment's provider reference and
  idempotency identity match. A different order for the same user/edition is a domain
  conflict and the complete transaction rolls back.

### Flash-sale pipeline

The existing flow remains:

1. Redis Lua checks activity version/window, stock, one-user-one-order and request
   digest atomically; it creates a queued marker.
2. RocketMQ transactional publish commits only with the admission outcome.
3. At-least-once consumers idempotently insert/finalize MySQL reservation and order
   records under row locks and unique keys.
4. Completion marks Redis; failures create durable MySQL release jobs and Redis Lua
   restores stock at most once.
5. Release workers claim work by `SELECT id ... FOR UPDATE SKIP LOCKED LIMIT ?`, then
   update and read those IDs inside one transaction.

Tests re-prove no oversell, one-user-one-order, replay compatibility, compensation and
multiple-worker lease ownership under MySQL Read Committed.

## Data Cutover

Application startup never migrates live PostgreSQL data. The operator command has
`inspect`, `copy`, `verify` and `resume` modes, an explicit source PostgreSQL DSN, the
target MySQL DSN and a checkpoint directory/report. Default is dry run.
The target identity is not an operator assertion: the tool hashes its embedded ordered
MySQL migrations, requires the live `schema_migration` history to match them exactly,
and binds that digest plus the inspected target schema into every artifact.

Recommended first production procedure:

1. Build the operator binary with an exact `--tool-commit`; provision an empty MySQL 8.4
   schema, apply the embedded migrations, verify TLS/UTC/strict mode, and create a private
   normalized `0700` checkpoint directory. Back up PostgreSQL and MySQL.
2. Run `--mode inspect` and `--mode copy` without `--execute`. Review the source
   inventory/checks and the target instance, database, embedded/live migration digest and
   schema digest. Neither command creates a manifest or changes source/target data.
3. Disable every business write path, stop/drain HTTP mutators, RocketMQ consumers and
   background workers, drain CDC/outbox work, and disable target write traffic and
   automatic application/worker restart. Independent external controllers then issue
   (a) the short-lived signed `xlh.postgres_write_freeze.v1` artifact bound to the
   observed source database, schema and full canonical snapshot digest and (b) the
   short-lived signed `xlh.mysql_target_writer_fence.v1` artifact bound to the inspected
   target identity/digest and approved deployment generation, proving zero application
   and background writers plus disabled restart and write traffic. `GET_LOCK` is only
   a migration-tool mutex and does not satisfy the target-writer fence.
4. Install the separately trusted base64 Ed25519 public keys either in
   `XLH_SOURCE_FREEZE_PUBLIC_KEY` / `XLH_TARGET_WRITER_FENCE_PUBLIC_KEY` or their
   effective-UID-owned, non-symlink regular key files with mode `0400`/`0600`; never
   provide both sources for one key. Review both key IDs and the target deployment
   generation out of band. Run `--mode copy --execute` with both attestation paths, key
   IDs and key sources. Any resume uses the same valid source attestation identity and
   may use a freshly issued target fence only for the same target identity and generation.
5. Copy tables in foreign-key order while preserving IDs and converting JSON, UTC
   timestamps and SHA-256 digests. Handle the coupon/order cyclic references with the
   documented deferred update phase, without disabling validation globally.
6. Before every copied/deferred batch or auto-increment mutation, reload and authenticate
   the target fence against the live target. Set each `AUTO_INCREMENT` to greater than
   `MAX(id)`. Reinspect the complete frozen source, the live target identity and both
   signed evidence records before publishing reconciliation success or
   `Completed/CutoverReady`.
7. Persist checkpoints only after target commit. Each checkpoint is bound to the
   cutover identity and records table, last source primary key, source row count and
   canonical batch SHA-256, target committed row count/digest, and whether deferred FK
   links are pending or complete. Resume refuses any source snapshot, schema, target
   embedded/live migration, target schema or tool-commit mismatch.
8. Run `--mode verify` with both trusted keys/key IDs and the expected target deployment
   generation, but no external attestation path and never `--execute`. It authenticates
   both manifest-embedded attestations (including the target canonical payload and
   payload/attestation digests), securely
   loads the referenced content-addressed reconciliation report, acquires only existing
   locks, uses read-only source/target snapshots, reruns full reconciliation and performs
   no create, update, rename or delete in the checkpoint directory or either database.
9. Require table count and canonical row-digest reconciliation plus orphan, uniqueness,
   status, inventory and financial checks. Emit one immutable reconciliation digest and
   require the second-phase coupon/order and entitlement FK links to be complete.
10. Only after independent approval of the verify evidence, stop the old process, start
   the exact reviewed build against MySQL with public writes still disabled, run read-only
   smoke, then run explicitly approved bounded write smoke and enable consumers/writes.
11. Observe the approved window and retain PostgreSQL read-only. Do not delete or mutate
   the source automatically.

The cutover identity contains source cluster/database identity, PostgreSQL snapshot
identifier and exported snapshot/LSN, source schema checksum, target MySQL
instance/database identity, embedded/live target migration version/checksum, target schema
digest and tool build commit. There is deliberately no `--target-migration-commit` flag.

Resume consumers/writes only when all required evidence is accepted. No reverse dual
write exists, so rollback after accepting MySQL writes requires stopping writes and an
explicit reconciliation decision.

If rehearsal shows the write freeze exceeds the approved outage window, stop and write
a separate CDC/dual-run spec. Do not improvise dual writes in repositories.

## LightRAG And Milvus Design

### Pinned storage configuration

```text
LIGHTRAG_KV_STORAGE=JsonKVStorage
LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage
LIGHTRAG_GRAPH_STORAGE=NetworkXStorage
LIGHTRAG_DOC_STATUS_STORAGE=JsonDocStatusStorage
WORKSPACE=xiaolanhe_v1
WORKING_DIR=/app/data/rag_storage
# lightrag-bootstrap and steady-state lightrag
MILVUS_URI=http://milvus:19530/lightrag
MILVUS_DB_NAME=lightrag
MILVUS_INDEX_TYPE=AUTOINDEX
MILVUS_METRIC_TYPE=COSINE
EMBEDDING_MODEL=text-embedding-v4
EMBEDDING_DIM=1024
EMBEDDING_SEND_DIM=false
EMBEDDING_ASYMMETRIC=false
# EMBEDDING_DOCUMENT_PREFIX and EMBEDDING_QUERY_PREFIX are deliberately unset
```

`MILVUS_WORKSPACE` stays unset. A deployment init job connects with the short-lived root
identity and `MILVUS_URI=http://milvus:19530`, lists/creates database `lightrag`,
provisions the runtime identity, then exits. Both `lightrag-bootstrap` and steady-state
`lightrag` connect with `MILVUS_URI=http://milvus:19530/lightrag` and
`MILVUS_DB_NAME=lightrag`. With the selected PyMilvus 3.0.0 this URI path makes the first
client context the target database instead of `default`; the explicit database variable
remains part of LightRAG configuration and validation.

The runtime role keeps exactly `DatabaseAdmin` and `CollectionReadWrite` scoped to
`lightrag`, plus cluster-scoped `ListDatabases` and `RenameCollection`. Role inspection
queries all database scopes so named-database grants are not omitted. No runtime grant
is added to `default`; create-database, create-user and RBAC-management probes remain
denied. This avoids relying on the adapter's otherwise valid auto-create path without
forking the official LightRAG adapter. Authentication variables are supplied only when
the target supports them. Production configuration must not use example MinIO or
database passwords. LightRAG keeps one service replica; its supported Gunicorn workers
coordinate only within that service instance.

The embedding contract hash includes binding/provider, normalized endpoint identity,
model, dimension, `EMBEDDING_SEND_DIM`, asymmetric mode, document/query prefix values,
LightRAG version/API, workspace, all four storage classes, Milvus database, index type
and metric. This release explicitly uses symmetric embeddings with no prefixes. Any
contract change makes the fence stale and requires all entity, relationship and chunk
vectors to be regenerated; a new collection suffix alone does not populate data.

### Standalone topology and persistence

The local/CI deployment contains `milvus`, `milvus-etcd` and `milvus-minio`. All use
pinned images, private DNS names, persistent named volumes, resource bounds and
semantic health probes. The LightRAG service waits for healthy Milvus. Host access to
19530/9091, etcd and MinIO is absent unless an isolated test explicitly binds loopback.

The consistency unit for backup/restore is:

- complete LightRAG `WORKING_DIR`;
- Milvus data volume;
- etcd data volume;
- MinIO data volume;
- versioned LightRAG/Milvus and embedding configuration.

After knowledge mutations drain, LightRAG is stopped first, then Milvus is stopped
cleanly, then etcd and MinIO are stopped before cold volume snapshots are taken. The
archive contains a manifest with component/image/config identities, volume members,
byte sizes and SHA-256 for every artifact; publication is atomic only after all checksums
are durable. Restore starts etcd and MinIO, then Milvus, and keeps LightRAG stopped while
the restored fence is first forced to `stale`. A restore into empty volumes must validate
manifest checksums, document/status/source counts, graph/KV source digests, Milvus
collection schemas and expected ID sets, then retrieval/citations. Only a new
`restore_verify` report for a new deployment generation can publish `verified` and start
LightRAG. Restoring only Milvus or only the LightRAG directory is rejected.

The manifest is canonical JSON with an outer manifest digest. Its inner payload binds
the source generation, complete fence contract/configuration digest and the external
writer-evidence digest. Each of the four named components has exactly one archive entry
with a byte length, archive SHA-256, regular-file count, aggregate file-list digest and
the sorted regular-file list (`path`, `size_bytes`, `sha256`). Restore re-opens each tar,
rejects absolute/parent/symlink/device members, and recomputes every member digest before
extracting to newly created project-scoped volumes. Static/unit negative fixtures cover
missing components, truncated members and a separately valid manifest from another
generation; the isolated lifecycle repeats those pre-extraction failures.

### NanoVectorDB to Milvus rebuild and fence

The normative state/file/report contract is in `rebuild-fence.md`. The guarded workflow:

1. Select the attempt ID before evidence collection, acquire the deployment rebuild
   lock, quiesce knowledge mutations, and create a canonical external writer-evidence
   artifact only after proving the managed LightRAG service is stopped/absent and its
   automatic restart is disabled. The artifact and command-line digest bind attempt,
   generation, operation, contract, issue/expiry, pipeline idle, zero replicas and a
   bounded operator attestation that no external writer remains. A fixed container
   environment value is never evidence.
2. Wait for pipeline idle before shutdown, back up the complete old workspace/config,
   start only healthy Milvus dependencies, and verify the exact contract hash. Preserve
   JsonKV, NetworkX and source graph/KV; change only vector storage/connectivity.
3. Atomically publish state `rebuilding` before any target can be dropped. Invoke the
   pinned v1.5.7 library functions `rebuild_entities_vdb`,
   `rebuild_relationships_vdb`, then `rebuild_chunks_vdb`; do not automate the
   interactive menu or infer success from exit status.
4. For each structured stats object require `errors=[]`, `failed_batches=0`,
   `staged=0`, `rebuilt=prepared`, and
   `prepared + skipped + duplicates = source_total`. Production normally requires
   `skipped=0` and, by default, `duplicates=0`. Any nonzero duplicate count requires a
   separate canonical reviewed-policy artifact bound to the exact attempt, generation,
   operation, contract and source digest, with named reviewer, expiration, artifact
   digest and per-target maximum; an explanatory report field alone cannot waive it.
5. Prove pre/post source node, edge and `text_chunks` digests are unchanged. Independently
   derive expected normalized IDs using pinned upstream helpers and require exact set
   equality and count for all three live Milvus collections. Require database `lightrag`;
   exact final namespaces `xiaolanhe_v1_{entities,relationships,chunks}_text_embedding_v4_1024d`;
   exact common and target-specific field sets, types, VARCHAR lengths, nullability,
   `id` primary-key identity, dynamic-field setting; FLOAT_VECTOR(1024), `AUTOINDEX`,
   `COSINE`, upstream graph consistency and retrieval/citation fixtures.
6. Durably write the immutable attempt report and atomically publish `verified` for the
   current deployment generation only after all storage finalizers and fresh verifier
   clients close successfully. Any exception, signal, source mutation, malformed stats,
   cleanup or verification failure publishes `failed`; the retained source permits an
   explicit rerun.
7. Start LightRAG, then verify authenticated `/health`, no pipeline recovery, matching
   fence/report digest and live collection schema/index. Routine readiness does not
   compare mutable live counts with historic rebuild counts because later valid
   ingestion changes them.

Every command maps to one immutable operation (`rebuild` to `migration_rebuild`,
`bootstrap` to `bootstrap_empty`, `restore-verify` to `restore_verify`, and `revalidate`
to `revalidate`); callers cannot relabel it. Before a new attempt, the controller
validates the current state against an allowlisted transition table. If it acquires the
exclusive lock and finds `rebuilding`, the prior owner is gone: it first publishes a
schema-complete immutable `failed/abandoned_rebuilding` report and marker for that exact
prior attempt, using the exact immutable attempt journal written and fsynced before the
old `rebuilding` marker. Recovery rejects a missing/mismatched journal and idempotently
reuses only a matching already-durable failed report; it never reconstructs the old
attempt from current runtime configuration or overwrites a report. It may then transition
the new attempt from `failed`. Verification-only
commands remain unready in `stale` until terminal publication. Python and Go share the
same exact marker/report schema and reject unknown keys, missing applicable values,
operation/reason mismatch, noncanonical files and malformed timestamps/digests.

Fresh bootstrap may publish `verified` without paid embedding calls only when graph,
`text_chunks` and all three target collections are independently proven empty and their
schemas/indexes are valid. This repository's deployment guard owns the fence; it does
not claim that upstream LightRAG writes it.

### Legacy PostgreSQL knowledge import and retirement

Before relational source retirement, freeze the legacy knowledge tables and create an
immutable manifest ordered by `knowledge_document.id`. Each row records the deterministic
`xlh-legacy-<id>.txt` source key, SHA-256 and rune/byte length of the exact
`XiaoLanHe-Knowledge-v1` canonical envelope, legacy chunk count, and an explicit
disposition for metadata/published/created/updated timestamps. The manifest has a
top-level digest and no document content. `knowledge_chunk` text/metadata/1536d vectors
are derived inputs and are not copied; LightRAG re-chunks, extracts and embeds.

The importer remains operator-only, reads a bound PostgreSQL snapshot and sends the
canonical envelope through the official authenticated LightRAG API. A batch checkpoint
advances only through the contiguous prefix of successfully processed or manifest-
confirmed replayed IDs; failures are recorded separately so resume cannot skip them.
Track reconciliation requires the expected track ID, exactly one document, expected
source key and terminal `PROCESSED`. A 409 key match is not content proof: the frozen
source manifest and immutable prior success report supply the digest guarantee.

Retirement requires an unbounded/paginated verifier (not the current 4,000-document
client cap) to prove exact source-key set equality, unique IDs/keys, all documents
`PROCESSED`, zero nonterminal/failed records, pipeline idle/no recovery, and compatible
content lengths. It records old and new chunk totals but never requires equality because
chunking changed. All four retrieval modes and managed citations, full backup/restore,
and static/dynamic absence of SQL knowledge fallback must pass. The source tables then
remain read-only through the rollback window; physical deletion is separately approved.

## Knowledge Runtime Cutover

The former `internal/adapter/postgres/knowledge.go` disappears. Both baseline and
advanced Assistant composition receive the official LightRAG repository:

- baseline: bounded direct/research flow using LightRAG evidence;
- advanced: Query Planner + Game Copilot/Research/Planning using the same LightRAG
  evidence boundary;
- public knowledge search/admin: existing direct LightRAG facade.

When LightRAG or Milvus is unavailable, knowledge-dependent requests return explicit
unavailable/partial outcomes. Catalog/forum evidence may still be used when the Skill
allows it. No SQL substring search, pgvector fallback or provider relabeling exists.

### Public search admission

`GET /api/knowledge/search` remains a public read-only endpoint. Its Entry owns a
process-local admission guard because the protected resource is the downstream
LightRAG query, not a business identity. `NewHTTP` installs conservative defaults: a
single global token bucket refilling at one token per second with capacity five, plus a
four-slot semaphore for in-flight searches. The Entry rejects malformed public query
parameters before admission; the UseCase retains independent defensive validation.
Admission therefore happens before any LightRAG request without allowing malformed
traffic to drain valid search capacity. Tokens refill lazily from a monotonic clock;
no goroutine, ticker or caller map is retained, so memory use is constant and there is
no cleanup lifecycle.

Rate exhaustion and a full concurrency semaphore fail fast with
`429 capacity_exceeded` and an integer `Retry-After` of at least one second. The rate
case derives the delay from the next token; the concurrency case uses one second as a
bounded retry hint. A canceled context is checked before admission, cancellation flows
to LightRAG after admission, and a deferred release returns every acquired slot. Tests
use an injected deterministic clock/guard rather than sleeping.

The limiter is deliberately global and never reads `X-Forwarded-For`, `X-Real-IP` or
another caller-controlled identity. This prevents bypass by rotating spoofed headers
and avoids unbounded per-client state. If product requirements later need client-level
fairness, only the socket remote address may be used unless an explicit trusted-proxy
chain is configured and reviewed. This process-local guard bounds one service process;
a future multi-replica deployment must add a shared edge/distributed quota and size
per-process concurrency against aggregate LightRAG capacity.

## Configuration, Deployment And Compatibility

- Application configuration defaults `XLH_LIGHTRAG_BASE_URL` to
  `https://127.0.0.1:9621` and strictly parses `XLH_LIGHTRAG_ALLOW_INSECURE`, whose
  default is `false`. HTTPS is accepted without an override; HTTP fails configuration
  loading unless the override is explicitly `true`. Local Compose/CI may opt in for a
  loopback or controlled private endpoint, while production must use HTTPS and keep the
  override false. The runtime and importer loaders pass the decision explicitly to the
  repository client and migration verifier, which independently reject unapproved HTTP.
  These gates run before any client can send the API key or query/request body.
- When flash sale is enabled, configuration defaults
  `XLH_REDIS_ALLOW_INSECURE=false` and requires an authenticated `rediss://` URL.
  Plaintext `redis://` is accepted only with an explicit local/CI override. It also
  defaults `XLH_ROCKETMQ_ALLOW_INSECURE=false` and requires a non-empty valid
  RocketMQ access/secret pair; the explicit local/CI override permits both credentials
  to be absent but never permits a partial pair. Both strict boolean flags and all
  dependency values are ignored when flash sale is disabled.
- `deploy/docker-compose.middleware.yml` replaces PostgreSQL with MySQL 8.4 while
  retaining Redis and RocketMQ.
- `deploy/docker-compose.lightrag.yml` adds the full Milvus standalone stack and moves
  LightRAG from NanoVectorDB to Milvus. Static guards assert image pins, one LightRAG
  replica, private ports, volumes and exact store configuration.
- GitHub Actions uses a MySQL 8.4 service with a health check, UTC and strict mode. A
  separate lifecycle job runs official LightRAG + Milvus when credentials/local test
  providers are available. Required live gates cannot be relabeled as unit passes.
- `deploy/render.app-only.example.yaml` is deliberately not auto-discovered. It
  accepts external MySQL, TLS-file and read-only fence placeholders without pretending
  Render supplies managed MySQL or stateful Milvus. An operator may copy it to
  `render.yaml` only after those mounts and services exist.
- Public REST/SSE and frontend contracts remain stable; no data-store name is exposed.

## Failure, Cancellation And Backpressure

- Database operations use request contexts and bounded query/transaction deadlines.
- MySQL pool exhaustion, migration dirty state, timezone/sql-mode mismatch and failed
  readiness are explicit. Retry does not outlive request cancellation.
- Flash-sale Redis/RocketMQ/MySQL failure outcomes retain existing compensation and
  reconciliation state; no in-memory queue becomes authoritative.
- Milvus 9091 health failure, LightRAG storage mismatch, vector-dimension mismatch or
  any fence other than `verified` for the required generation/contract makes knowledge
  readiness fail closed.
- Ingestion backpressure continues to use LightRAG's bounded pending-document limit.
  Milvus timeouts are surfaced through LightRAG; Go does not independently retry vector
  writes.
- Public knowledge search rejects exhausted rate or concurrency capacity before any
  LightRAG call with `429` and `Retry-After`. The guard does not queue unbounded work,
  does not start background refill workers and releases in-flight capacity on success,
  dependency error, panic unwind or cancellation.

## Security And Trust Boundaries

- MySQL, Redis, RocketMQ, Milvus, etcd, MinIO and LightRAG use private networking.
- Private networking does not replace transport encryption. Go-to-LightRAG traffic uses
  HTTPS by default and in production; an explicit insecure override is limited to local
  or otherwise controlled environments because HTTP exposes the API credential and
  knowledge query/request content to the transport path.
- Production Redis traffic uses authenticated TLS (`rediss://`), and production
  RocketMQ clients use an access/secret ACL pair. Plaintext Redis and an ACL-free
  RocketMQ broker are permitted only through separate explicit local/CI overrides;
  neither override weakens validation of credentials that are present.
- Secrets are environment/secret-store values, excluded from logs, metrics, reports and
  checked-in Compose defaults intended for production.
- Migration tools require explicit source/target confirmation and default to dry run.
- Retrieved LightRAG content remains untrusted bounded evidence. Agent model output
  cannot select SQL, a database endpoint, Milvus collection, LightRAG URL or write tool.
- Backup artifacts inherit encryption, least privilege and retention policies.

## Observability

Low-cardinality metrics add MySQL operation outcome/latency, pool saturation,
transaction retry decision events, migration state and reconciliation totals; no query
text, DSN, idempotency key or user ID is a label. LightRAG metrics expose the expected
storage backend, authenticated service health, deployment-generation match, fence
state and rebuild outcome/count categories. The Go runtime observes only the official
LightRAG health/storage contract; direct Milvus health belongs to Compose/orchestrator
checks and is never inferred by a Go-to-Milvus connection.

Host/orchestrator monitoring owns MySQL storage/replication/backup signals and all four
LightRAG/Milvus volumes, process memory, disk free space and restarts. Alerts cover dirty
migrations, repeated deadlocks/timeouts, reconciliation mismatch, Redis/MySQL stock
drift, RocketMQ backlog, LightRAG pipeline recovery, Milvus unhealthy state and vector
rebuild mismatch. Runtime tracing remains optional and must not contain content/secrets.

## Rollout And Rollback

Implementation rollout order is MySQL schema/adapter tests, module repositories,
knowledge unification, deployment manifests, data-copy rehearsal, isolated full stack,
then an approved production cutover. Redis/RocketMQ remain running but consumers are
drained during relational cutover.

Before production vector migration, retain a complete NanoVectorDB-era LightRAG
workspace backup. If Milvus rebuild or validation fails, keep LightRAG stopped, restore
the old workspace/config, invalidate the Milvus fence, and start the pinned
NanoVectorDB configuration only as the documented migration rollback. Once new
knowledge writes resume against Milvus, rolling back requires another writer freeze and
full consistency decision.

Before MySQL cutover, retain PostgreSQL backup/read-only source. If smoke fails before
new MySQL writes are accepted, point the application back to PostgreSQL using the old
build. If writes have been accepted, automatic rollback is forbidden because no reverse
dual write exists; stop writes, reconcile and make an explicit operator decision.

## Architecture Traceability

| Planned change | Owner/module | Requirement |
|---|---|---|
| MySQL pool, migrations and runtime wiring | `adapter/mysql`, `cmd` | AC1-AC3 |
| MySQL repositories and schema invariants | business modules | AC2, AC4, AC5 |
| Redis/RocketMQ config + MySQL finalization | `config`, `flashsale`, `order` | AC5, AC6, AC12 |
| Remove local pgvector knowledge | `knowledge`, `assistant`, composition | AC1, AC7 |
| Milvus LightRAG storage and readiness | `knowledge/repository/lightrag`, deploy | AC8 |
| Public knowledge rate/concurrency admission | `knowledge/entry` | AC13 |
| Rebuild, persistence and restore | deploy/operator scripts | AC9, AC10 |
| PostgreSQL-to-MySQL cutover | migration command/docs | AC11 |
| Compatibility and permanent read-only Agent boundary | entry/presenter/assistant | AC13 |
| CI, security and final report | repository-wide | AC12, AC14 |

## Rejected Alternatives

- **Use LightRAG as the business database:** rejected because LightRAG's storage ports
  model knowledge retrieval, not relational transactions, foreign keys, money, orders
  or flash-sale durability.
- **Keep PostgreSQL for business data:** rejected by the corrected requirement.
- **Store vectors in MySQL:** rejected because the requirement selects official
  LightRAG Milvus support and MySQL 8.4 is not the existing pgvector equivalent.
- **Point LightRAG at Milvus without rebuild:** rejected because existing vector data
  remains in NanoVectorDB and LightRAG has no automatic cross-backend migration.
- **Grant the runtime role access to `default` or fork the Milvus adapter:** rejected
  because the PyMilvus 3.0.0 URI path selects `lightrag` before the first client RPC while
  preserving the upstream adapter and the existing least-privilege grant set.
- **Move all four LightRAG stores to MySQL:** rejected because the requirement is
  business MySQL plus Milvus vector storage; it would conflate ownership and is not an
  official LightRAG all-store MySQL configuration.
- **Use MySQL named locks for business operations:** rejected due to connection-scoped
  lifetime and pool leak risk; stable row locks express the business resource directly.
- **Rewrite old migration files in place:** rejected because applied checksums and SQL
  dialect histories cannot be shared safely.
- **Continuous dual write:** rejected for this scope because it multiplies inventory and
  idempotency failure modes; a CDC design requires measured need and separate review.
