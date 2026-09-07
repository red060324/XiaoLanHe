# Public Deployment

XiaoLanHe requires a Go web service, an external MySQL 8.4/InnoDB business
database and an official LightRAG 1.5.7 knowledge service. LightRAG persists KV,
graph and document status as files in its complete `WORKING_DIR` and persists
chunk/entity/relation vectors through `MilvusVectorDBStorage` in Milvus 2.6.11.
The Go service never connects to Milvus directly.

Redis 7.4 and RocketMQ 5.3.2 are required only when the opt-in flash-sale
feature is enabled. They retain their existing roles: Redis Lua admits requests
atomically, RocketMQ delivers at least once, and MySQL is the final durable
inventory/idempotency boundary.

This repository contains deployment examples and validation tools. It does not
purchase, provision, migrate, cut over, or delete production cloud resources.
Production infrastructure, credentials, destructive retirement, paid
embedding/rebuild calls and traffic enablement require separate approval.

## Storage Ownership

| Data | System of record | Notes |
|---|---|---|
| accounts, sessions, profiles and conversations | MySQL 8.4/InnoDB | UTC, strict mode, `utf8mb4`; verified TLS in production |
| catalog, community, promotions, orders and payments | MySQL 8.4/InnoDB | foreign keys, checks and durable idempotency |
| flash-sale admission and recovery markers | Redis + Lua | time-bounded fast path, not the durable order record |
| flash-sale reservation events | RocketMQ | at-least-once transport; consumers are idempotent in MySQL |
| knowledge documents, chunks and caches | LightRAG `JsonKVStorage` | persistent `WORKING_DIR` files |
| knowledge graph | LightRAG `NetworkXStorage` | persistent `WORKING_DIR` files |
| ingestion/document status | LightRAG `JsonDocStatusStorage` | persistent `WORKING_DIR` files |
| chunk/entity/relation vectors | LightRAG `MilvusVectorDBStorage` | derived Milvus projections; no direct Go access |

LightRAG is the knowledge boundary in both basic and advanced Assistant modes.
`XLH_ADVANCED_AI_ENABLED=false` disables advanced orchestration, not LightRAG.
There is no MySQL, PostgreSQL, pgvector or NanoVectorDB runtime fallback.
The Go-to-LightRAG transport defaults to HTTPS. Plain HTTP is rejected unless an
operator explicitly sets `XLH_LIGHTRAG_ALLOW_INSECURE=true`; that exception is only
for loopback or otherwise controlled local environments because HTTP can expose the
LightRAG API key and query/request bodies in transit. Production must use an HTTPS
`XLH_LIGHTRAG_BASE_URL` and keep the override `false`.

## Render Blueprint

The repository deliberately has no root `render.yaml`: Render would auto-discover it
even though a Blueprint cannot provide this complete production topology or the
required TLS/fence files. `deploy/render.app-only.example.yaml` is a non-automatic
app-only reference with no `databases:` resource and no `fromDatabase` binding. Copy it
to `render.yaml` only after the external services and file mounts below have been
provisioned and reviewed.

Before deploying the Blueprint, an operator must separately provide:

1. an external MySQL 8.4/InnoDB database with private connectivity, verified TLS,
   backups and restore evidence;
2. an official LightRAG 1.5.7 service backed by a persistent `WORKING_DIR`, Milvus
   2.6.11, etcd 3.5.25 and the pinned MinIO release;
3. a verified rebuild-fence marker mounted read-only into the Go service at
   `XLH_LIGHTRAG_REBUILD_FENCE_DIR`; and
4. all required values in Render's secret store.

At minimum, configure the external/secret placeholders for `XLH_DATABASE_URL`,
`XLH_AI_API_KEY`, `XLH_LIGHTRAG_BASE_URL`, `XLH_LIGHTRAG_API_KEY`,
`XLH_LIGHTRAG_REBUILD_FENCE_DIR`, `XLH_LIGHTRAG_DEPLOYMENT_GENERATION` and
`XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256`. Keep
`XLH_DATABASE_ALLOW_INSECURE=false` and `XLH_LIGHTRAG_ALLOW_INSECURE=false`, and
configure `XLH_LIGHTRAG_BASE_URL` with `https://`. A production DSN must include the fixed
`tls=xlh-verified` profile. `XLH_DATABASE_TLS_CA_FILE` and
`XLH_DATABASE_TLS_SERVER_NAME` are required so the profile verifies the certificate
chain and hostname; optional `XLH_DATABASE_TLS_CERT_FILE` and
`XLH_DATABASE_TLS_KEY_FILE` enable mutual TLS and must be configured together. The DSN
must also retain
`parseTime=true`, `loc=UTC`, bounded dial/read/write timeouts,
`multiStatements=false` and `clientFoundRows=false`.

For example, the production DSN query must include
`parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s&multiStatements=false&clientFoundRows=false&tls=xlh-verified`.
The CA, client certificate and client key must be delivered as secret files at the
configured paths; setting a path variable does not create or upload its file.

The example keeps flash sale and advanced orchestration disabled. These flags
avoid creating optional runtime dependencies; they do not make the external
MySQL and LightRAG services optional. If enabled, the Blueprint creates only the Go
web service, so applying it does not buy MySQL, Milvus, etcd, MinIO, Redis,
RocketMQ, disks or backup storage.

Render supplies an `onrender.com` subdomain. Buying a custom domain is optional;
it changes DNS/TLS and `XLH_PUBLIC_ORIGIN`, not application code. `/readyz` is
the routing health check and remains unready until the external database,
LightRAG contract and rebuild fence are valid.

## Local Integration Topology

The checked-in middleware Compose stack starts MySQL 8.4, password-protected
Redis 7.4 and a persistent RocketMQ 5.3.2 NameServer/Broker pair:

```bash
cp .env.example .env
# Replace all local placeholders; never reuse them in production.
make middleware-config
make middleware-up
```

The Go process runs on the host and connects to loopback addresses. Named
volumes retain MySQL, Redis AOF, RocketMQ logs and broker state across ordinary
restarts. `make middleware-down` stops containers without deleting volumes.
Removing volumes is a destructive reset and is not part of that target.

The local stack is integration tooling, not a high-availability topology. Do not
expose MySQL, Redis, RocketMQ NameServer or Broker publicly. For production, use
private addressing, least-privilege identities, authentication, encryption,
persistent storage, alerts and tested recovery.
The checked-in Redis endpoint is plaintext and the checked-in RocketMQ broker does
not enable ACL, so a host-run application must explicitly set
`XLH_REDIS_ALLOW_INSECURE=true` and `XLH_ROCKETMQ_ALLOW_INSECURE=true` when enabling
flash sale against this local stack. These opt-ins are not production settings.

## Official LightRAG And Milvus Stack

`deploy/docker-compose.lightrag.yml` pins:

- official LightRAG 1.5.7/API 0344 at an immutable image digest;
- Milvus standalone 2.6.11;
- etcd 3.5.25; and
- MinIO `RELEASE.2025-09-07T16-13-09Z`.

The supported LightRAG storage contract is exactly:

```text
LIGHTRAG_KV_STORAGE=JsonKVStorage
LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage
LIGHTRAG_GRAPH_STORAGE=NetworkXStorage
LIGHTRAG_DOC_STATUS_STORAGE=JsonDocStatusStorage
WORKSPACE=xiaolanhe_v1
WORKING_DIR=/app/data/rag_storage
MILVUS_DB_NAME=lightrag
MILVUS_INDEX_TYPE=AUTOINDEX
MILVUS_METRIC_TYPE=COSINE
EMBEDDING_MODEL=text-embedding-v4
EMBEDDING_DIM=1024
EMBEDDING_SEND_DIM=false
EMBEDDING_ASYMMETRIC=false
```

`MILVUS_WORKSPACE` and the document/query embedding prefixes remain unset. A
change to any embedding semantic, dimension, workspace, collection/index
contract or storage class invalidates the fence and requires a reviewed full
rebuild or re-ingestion.

All four state volumes are persistent: the LightRAG workspace plus Milvus, etcd
and MinIO. Milvus data ports remain private to the Compose network; only the
local LightRAG endpoint is loopback-bound. The deployment bootstrap identity may
create the `lightrag` database, while the steady-state identity must have only the
permissions needed for that database and its collections.
The local loopback endpoint currently uses HTTP, so the host-run Go process must
explicitly set `XLH_LIGHTRAG_ALLOW_INSECURE=true`. This local override is not a
production pattern and does not make cleartext safe on an untrusted network.

Because KV, graph and status remain local files, exactly one LightRAG service
replica may mount the read-write workspace. Milvus standalone and a single
LightRAG replica are not a distributed or HA deployment. A managed/distributed
replacement needs a separate TLS, authentication, backup, restore and
availability review.

For a fresh empty local stack, set unique credentials and a deployment
generation, then run the non-paid empty bootstrap before starting LightRAG:

```bash
export XLH_LIGHTRAG_API_KEY='a-distinct-private-key-at-least-32-chars'
export XLH_LIGHTRAG_LLM_API_KEY='...'
export XLH_LIGHTRAG_EMBEDDING_API_KEY='...'
export XLH_MILVUS_MINIO_USER='replace-with-a-local-user'
export XLH_MILVUS_MINIO_PASSWORD='replace-with-a-local-password'
export XLH_MILVUS_ROOT_PASSWORD='replace-with-a-distinct-root-password'
export XLH_MILVUS_TOKEN='xlh_lightrag:replace-with-a-distinct-runtime-password'
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION='local-empty-v1'
export XLH_LIGHTRAG_ATTEMPT_ID="bootstrap-$(date +%Y%m%d%H%M%S)"
export XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR="$(pwd -P)/.state/lightrag-fence"
export XLH_LIGHTRAG_REBUILD_FENCE_DIR="$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR"
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR="$(pwd -P)/.state/lightrag-writer-evidence"
export XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers
make lightrag-static
bootstrap_output=$(make --no-print-directory lightrag-bootstrap-empty)
printf '%s\n' "$bootstrap_output"
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_DEPLOYMENT_GENERATION=//p' | tail -n 1
)
export XLH_LIGHTRAG_ATTEMPT_ID=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_ATTEMPT_ID=//p' | tail -n 1
)
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=//p' | tail -n 1
)
export XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=//p' | tail -n 1
)
test -n "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION"
test -n "$XLH_LIGHTRAG_ATTEMPT_ID"
test -n "$XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256"
test -n "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"
make lightrag-up
```

Persist the printed generation, attempt/evidence digest and contract digest as
deployment evidence; the snippet feeds the contract digest back into the current
shell before the canonical steady-state start. Empty bootstrap refuses nonempty
authoritative sources or orphaned targets; it cannot be used to certify an
existing NanoVectorDB workspace. See
[`../../deploy/lightrag/README.md`](../../deploy/lightrag/README.md) for the
guarded rebuild/fence interface.

Do not use bare `docker compose up`. `make lightrag-up` first verifies the canonical
absolute fence host path and exact generation/contract, then starts only etcd, MinIO,
Milvus and LightRAG. The LightRAG container takes a shared `serving.lock` lease before
rechecking the fence and retains it while supervising the complete Gunicorn process
group. Controllers take the same lease exclusively before `rebuild.lock`, so a fence
transition cannot race a running steady writer. `milvus-init` and `lightrag-bootstrap`
are isolated behind the `bootstrap`
profile and are never part of steady-state restart.

## Configuration

| Variable | Required | Purpose |
|---|---:|---|
| `XLH_DATABASE_URL` | yes | MySQL DSN for the business database |
| `XLH_DATABASE_ALLOW_INSECURE` | local only | permits a plaintext local-test DSN; must be `false` in production |
| `XLH_DATABASE_TLS_CA_FILE` | production | CA bundle used by the fixed `xlh-verified` TLS profile |
| `XLH_DATABASE_TLS_SERVER_NAME` | production | hostname verified by the fixed `xlh-verified` TLS profile |
| `XLH_DATABASE_TLS_CERT_FILE` / `XLH_DATABASE_TLS_KEY_FILE` | optional pair | client certificate and key for mutual TLS |
| `XLH_DATABASE_*CONNECTION*` | no | bounded pool lifetime/idle settings |
| `XLH_DATABASE_MIGRATION_LOCK_TIMEOUT` | no | bounded MySQL migration-lock wait |
| `XLH_AI_API_KEY` | yes | OpenAI-compatible model credential |
| `XLH_AI_BASE_URL` / `XLH_AI_CHAT_MODEL` | no | model endpoint and model |
| `XLH_PUBLIC_ORIGIN` | deployed browser app | same-origin mutation policy |
| `XLH_COOKIE_SECURE` | deployed HTTPS | must remain `true` outside local HTTP |
| `XLH_SEARCH_ENABLED` / `SEARXNG_BASE_URL` | optional | public Web Search switch and endpoint |
| `XLH_LIGHTRAG_BASE_URL` | yes | official LightRAG API URL; defaults to `https://127.0.0.1:9621` and must use HTTPS unless the local override is explicit |
| `XLH_LIGHTRAG_ALLOW_INSECURE` | local/controlled only | permits an HTTP LightRAG URL when exactly parseable as boolean `true`; default and production value is `false` |
| `XLH_LIGHTRAG_API_KEY` | yes | distinct 32-512 character server-to-server key |
| `XLH_LIGHTRAG_WORKSPACE` | no | pinned workspace, default `xiaolanhe_v1` |
| `XLH_LIGHTRAG_WORKING_DIR` | no | expected path, default `/app/data/rag_storage` |
| `XLH_LIGHTRAG_REBUILD_FENCE_DIR` | yes | read-only deployment fence directory visible to the Go service |
| `XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR` | LightRAG Compose | canonical absolute host directory bind-mounted read-only for steady state |
| `XLH_LIGHTRAG_DEPLOYMENT_GENERATION` | yes | versioned deployment/restore generation |
| `XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256` | yes | exact verified storage/embedding contract digest |
| `XLH_LIGHTRAG_LLM_*` | LightRAG service | LLM endpoint, key and model |
| `XLH_LIGHTRAG_EMBEDDING_*` | LightRAG service | embedding endpoint, key, model and dimension |
| `XLH_MILVUS_MINIO_USER` / `XLH_MILVUS_MINIO_PASSWORD` | local Milvus stack | MinIO credentials; do not use example values in production |
| `XLH_MILVUS_ROOT_PASSWORD` | Milvus server + one-time init | nonempty server initialization password used as a root token only by the init job; never passed to steady-state LightRAG |
| `XLH_MILVUS_TOKEN` | Milvus init + LightRAG | distinct `username:password`; init assigns only pinned runtime privileges and verifies it cannot create databases/users |
| `XLH_LIGHTRAG_ATTEMPT_ID` / `XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR` | empty bootstrap only | unique attempt and canonical absolute evidence path; not required by steady-state start |
| `XLH_ADVANCED_AI_ENABLED` | no | selects advanced orchestration only; default `false` |
| `XLH_METRICS_TOKEN` | advanced AI or flash sale | distinct operator bearer token for `GET /metrics` |
| `XLH_FLASH_SALE_ENABLED` | no | opts into Redis + RocketMQ; default `false` |
| `XLH_REDIS_URL` | flash sale | authenticated `rediss://` URL; plaintext `redis://` is local/CI only |
| `XLH_REDIS_ALLOW_INSECURE` | local/CI only | permits plaintext `redis://` when exactly parseable as boolean `true`; default and production value is `false` |
| `XLH_ROCKETMQ_NAMESERVERS` | flash sale | comma-separated private NameServer addresses |
| `XLH_ROCKETMQ_ACCESS_KEY` / `XLH_ROCKETMQ_SECRET_KEY` | production flash sale | non-empty RocketMQ ACL pair; partial pairs are always rejected |
| `XLH_ROCKETMQ_ALLOW_INSECURE` | local/CI only | permits both RocketMQ ACL values to be absent when exactly parseable as boolean `true`; default and production value is `false` |

Use the deployment platform's secret store. Do not log or persist DSNs, database
passwords, LightRAG/Milvus credentials, model keys or backup credentials in
markers, reports or checked-in files. `.env.example` is a local template only.
Private networking is not a substitute for transport security: without TLS, an
intermediary can observe LightRAG credentials/content or authenticated Redis traffic.
For a production flash-sale deployment, keep both insecure overrides `false`, use an
authenticated `rediss://` URL, and configure both RocketMQ ACL values through the
secret store. If flash sale remains disabled, Redis, RocketMQ and these override
variables are not required.

## Metrics And Host Monitoring

When `XLH_METRICS_TOKEN` is configured, scrape the application endpoint with a
dedicated operator credential over a private network or operator-only gateway:

```bash
curl --fail \
  -H "Authorization: Bearer $XLH_METRICS_TOKEN" \
  https://<private-service>/metrics
```

The route is not registered when the token is empty. Advanced orchestration and
flash-sale mode each require a valid token; do not reuse a user-session secret,
LightRAG key, model key or database credential. Application metrics use bounded
labels for Agent/model, MySQL, LightRAG pipeline/fence/document and flash-sale
outcomes. They never use run, session, user or source identifiers as labels and
do not expose prompts, content, SQL, DSNs or keys.

Collect process memory, filesystem capacity, volume bytes, container restarts and
Milvus/etcd/MinIO resource saturation at the host or orchestrator. Those values
cannot be inferred safely from the LightRAG API, and this release does not claim
to install an OpenTelemetry exporter or emit spans.

## Migrations, Seed And Legacy Boundaries

Application startup applies the independent one-statement-per-file MySQL
baseline in `migrations/mysql/`. It obtains a fixed-connection MySQL migration
lock, records dirty state before DDL and fails closed on dirty/checksum/postcondition
errors. It does not copy live PostgreSQL data.

Existing PostgreSQL migrations under `migrations/` are immutable historical
migration input. Keep them in the repository; never rewrite their checksums or
run them against MySQL. PostgreSQL/pgx may be used only by the isolated,
operator-invoked legacy knowledge importer and relational cutover tool. Neither
path may be imported by the normal server, seed or evaluation binaries.

Seed demo data explicitly, not at every startup:

```bash
XLH_SEED_ADMIN_PASSWORD='replace-with-a-strong-password' go run ./cmd/seed
```

The seed command updates the configured demo admin password. Do not run it
against an account whose ownership is unknown.

The legacy knowledge importer is dry-run by default:

```bash
go run ./cmd/import-knowledge --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json
go run ./cmd/import-knowledge --execute --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json
go run ./cmd/import-knowledge --reconcile --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json \
  --reconciliation-report ./artifacts/legacy-knowledge-reconciliation.json
```

The first command scans the frozen source and writes the immutable manifest plus
initial checkpoint. Repeating the same `--execute` command resumes from the
checkpoint's continuous-success watermark; the removed `--after-id` flag must not be
used. A completed import still requires `--reconcile` and retention of its immutable
report. The isolated importer applies the same HTTPS-by-default rule as the server;
HTTP requires `XLH_LIGHTRAG_ALLOW_INSECURE=true` and is only for a controlled local
environment because its credential and canonical document bodies are sensitive.
No legacy chunk or pgvector value is copied: LightRAG re-chunks and re-embeds the
canonical document through its API. PostgreSQL knowledge remains read-only through the
rollback window; physical deletion requires separate approval.

## Backup And Restore

Backup/restore evidence is a rollout prerequisite, not an application feature.
Do not accept irreplaceable data until an isolated restore has been executed and
its RPO/RTO recorded. Encrypt backups, restrict access and define retention.

### MySQL

Prefer provider snapshots plus a verified point-in-time recovery policy. For a
logical backup, use a dedicated least-privilege client configuration file rather
than putting the password on the command line. Since all application tables are
InnoDB, a representative flow is:

```bash
mysqldump --defaults-extra-file=/secure/mysql-client.cnf \
  --single-transaction --routines --triggers --events \
  --set-gtid-purged=OFF xiaolanhe > xiaolanhe.sql

mysql --defaults-extra-file=/secure/mysql-restore-client.cnf \
  xiaolanhe_restore < xiaolanhe.sql
```

Restore into an empty isolated MySQL 8.4 target. Verify server version, InnoDB,
UTC/strict session settings, migration checksums, constraints, row counts,
inventory, claims, orders, payments, entitlements and bounded read/write smoke
before declaring the backup usable. Never test a restore over the live database.

### LightRAG And Milvus

The knowledge consistency unit is indivisible:

- the complete LightRAG `WORKING_DIR`;
- the Milvus data volume;
- the etcd data volume;
- the MinIO data volume; and
- pinned LightRAG/Milvus/embedding/workspace configuration.

For raw-volume backup, stop public knowledge mutations, wait for the document
pipeline to become idle, stop every LightRAG writer, then cleanly stop Milvus,
etcd and MinIO before archiving all four state volumes. A LightRAG-only or
Milvus-only snapshot is incomplete and must be rejected.

Restore every component into empty volumes. A restored `verified` fence is never
trusted: use a new deployment generation, publish `stale`, start etcd/MinIO/Milvus
while LightRAG writers remain stopped, and run fresh source/status, collection
schema/index, exact ID-set and retrieval/citation verification. Only a new
`restore_verify` report may publish `verified` and allow LightRAG to start.

The isolated lifecycle gate exercises ingestion, all retrieval modes, restart,
whole-unit backup/restore and exact deletion. It creates and destroys only its
unique disposable Compose project, calls real providers and may incur cost:

```bash
export XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test
make lightrag-lifecycle
```

Run it only after separate credential, cost and destructive-test approval.

### Flash-Sale State

When flash sale is enabled, configure and rehearse MySQL backup together with
Redis AOF/snapshot and RocketMQ broker recovery. Monitor consumer lag, retries,
DLQ and Redis/MySQL reconciliation. Do not delete Redis markers or RocketMQ
topics during an application rollback: accepted requests may still need repair.
Production requires private, authenticated persistent Redis over TLS (`rediss://`)
and a persistent RocketMQ deployment with an access/secret ACL pair and tested broker
recovery. Keep `XLH_REDIS_ALLOW_INSECURE=false` and
`XLH_ROCKETMQ_ALLOW_INSECURE=false`; startup fails closed otherwise. The
local single-broker Compose stack is integration tooling, not HA. `/readyz` checks
Redis PING and an authenticated RocketMQ topic-route lookup with a publishable
queue; that does not replace lag, DLQ, durability or end-to-end publish monitoring.
The pinned admin API does not cancel its underlying route call when the readiness
context expires. XiaoLanHe bounds public response time and admits at most one such
residual probe; repeated readiness polls cannot create unbounded SDK work.
The pinned RocketMQ Go SDK has no cancellable total `Consumer.Start` deadline. XiaoLanHe
keeps that call synchronous and uses a 10-second process fail-stop watchdog after the
bounded route preflight; the supervisor must restart a process that exits with
`outcome=startup_timeout`. This avoids reporting startup failure while an orphaned SDK
client continues mutating lifecycle state. Repeated exits require operator investigation
of NameServer reachability, topic routes and broker heartbeat latency.

## Rollout And Rollback

No command in the default application startup performs a PostgreSQL-to-MySQL
copy, NanoVectorDB-to-Milvus rebuild, production cutover or destructive cleanup.
The following are separately approved rollout operations.

### Relational cutover

1. Build `cmd/migrate-postgres-to-mysql` with an exact tool commit. Provision and
   validate an empty MySQL 8.4/InnoDB target with private networking, verified TLS,
   strict SQL mode and UTC; apply the exact embedded migrations and back up both
   databases. Create a normalized effective-UID-owned `0700` checkpoint directory.
2. Set `XLH_LEGACY_POSTGRES_URL` and `XLH_DATABASE_URL`, then run `--mode inspect` and
   `--mode copy` without `--execute`. Review source inventory/checks and target identity.
   The tool derives target provenance from the embedded migration filenames/names/SHA-256
   values, requires an exact live `schema_migration` match and binds the inspected target
   schema; there is no `--target-migration-commit` input.
3. Freeze all application mutation endpoints, stop/drain RocketMQ consumers and
   background workers, drain CDC/outbox work, and disable target write traffic and
   automatic application/worker restart. Independent approved controllers must issue
   (a) a short-lived Ed25519-signed `xlh.postgres_write_freeze.v1` attestation bound to
   the observed source identity/snapshot and (b) a short-lived Ed25519-signed
   `xlh.mysql_target_writer_fence.v1` attestation bound to the inspected target
   instance/database/migration/schema identity and approved deployment generation. The
   target artifact must prove zero application/background writers plus disabled restart
   and write traffic. `GET_LOCK` only serializes migration tools and is not this proof.
4. Trust the two key IDs and target deployment generation out of band. Supply each raw
   base64 public key through exactly one of `XLH_SOURCE_FREEZE_PUBLIC_KEY` /
   `XLH_TARGET_WRITER_FENCE_PUBLIC_KEY` or the corresponding public-key-file flag. Each
   file must be a non-symlink regular file owned by the effective UID with mode `0400`
   or `0600`; the command validates and reads one no-follow descriptor.
5. Run `--mode copy --execute` with both attestation paths, both key IDs, both trusted
   keys and `--target-deployment-generation`. If interrupted, use `--mode resume
   --execute` with the same source attestation identity and either the existing target
   fence or a freshly issued fence for the same target identity/generation. The tool
   reloads and authenticates the target fence against the live target before every
   copied/deferred batch and auto-increment mutation, then revalidates both evidence
   records and the live target before successful reconciliation or
   `Completed/CutoverReady` publication.
6. Preserve IDs/UTC timestamps, complete deferred foreign keys, advance auto increments,
   and reconcile all tables, constraints, inventory, claims, orders, payments,
   entitlements, flash-sale reservations and the separate legacy knowledge import.
7. Run `--mode verify` without `--execute`, `--source-freeze-attestation` or
   `--target-writer-fence-attestation`, but with both trusted public keys/key IDs and
   `--target-deployment-generation`. Verify must authenticate both manifest-embedded
   attestations, the target canonical payload/digests and referenced immutable
   reconciliation report, use
   read-only database snapshots and existing locks only, and create/replace/update/delete
   no checkpoint artifact or database data. Preserve before/after evidence proving this.
8. After independent approval, stop the old process, start the reviewed build against
   MySQL while writes remain disabled, run read-only smoke, then explicitly approve the
   bounded write smoke and enable consumers/writes. Observe the approved window.
9. Keep PostgreSQL read-only through the rollback window. Do not delete it automatically.

If smoke fails before MySQL accepts new writes, restore the previous build and
read-only PostgreSQL source. Once MySQL accepts writes, automatic reversal is
forbidden because there is no reverse dual write: freeze writes, reconcile and
make an explicit operator decision.

The real PostgreSQL-to-MySQL 8.4 copy/resume and authenticated verify rehearsals are
required readiness evidence. Until both V27 and V28 run successfully, relational cutover
and overall readiness remain `BLOCKED/TODO`; unit or static checks are not substitutes.

### Vector cutover

1. Freeze knowledge mutations, drain the pipeline, stop all LightRAG writers and
   disable automatic restart.
2. Back up the complete NanoVectorDB-era workspace and pinned configuration.
3. Obtain explicit destructive-operation and paid-embedding approval.
4. Start healthy Milvus dependencies and invoke all three pinned official rebuild
   functions for entities, relationships and chunks.
5. Verify structured rebuild stats, unchanged graph/KV source digests, exact target
   ID sets, 1024-dimensional `AUTOINDEX`/`COSINE` schemas, status, retrieval and
   citations.
6. Publish the immutable report and `verified` fence for the exact generation and
   contract, then start LightRAG and observe.

If rebuild or validation fails, keep LightRAG stopped and the fence ineligible.
Before new Milvus-era writes, rollback may restore the complete prior workspace
and pinned NanoVectorDB configuration. After new writes, rollback requires another
writer freeze and explicit consistency/reconciliation decision. It is never an
automatic fallback.

## Verification And Smoke

Static checks do not require provider credentials:

```bash
make mysql-static
make milvus-static
make fence-static
make architecture
```

Live checks require the named local services and fail rather than silently skip
when their required configuration is absent:

```bash
make mysql-live
make milvus-live
make fence-live
make lightrag-live
```

After an approved deployment:

```bash
curl --fail https://<service>/healthz
curl --fail https://<service>/readyz
curl --fail https://<service>/api/games
curl --fail https://<service>/api/community/posts
curl --fail https://<service>/api/deals
```

For an isolated target where creating demo data is acceptable, run the full
authenticated product smoke only after explicit approval:

```bash
XLH_SMOKE_BASE_URL=https://<service> \
XLH_SMOKE_ADMIN_PASSWORD='<seeded-admin-password>' \
bash scripts/smoke-product.sh
```

The smoke creates a temporary user, community content, a coupon claim, a
sandbox-paid order and an entitlement. Hosted-environment writes, production
restore, relational/vector cutover, real-model evaluation and paid provider
calls remain rollout-only evidence. A skipped live check is `ENVIRONMENT BLOCKED`
or `SKIPPED WITH RISK`, never a static pass.
