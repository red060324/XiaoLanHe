# Delivery Report

- Status: `IMPLEMENTATION DELIVERED — PRODUCTION READINESS BLOCKED`
- Authoritative spec: `./spec.md`
- Evidence date: 2026-09-08

## Outcome

The approved implementation is present in the current branch. Normal business runtime
uses MySQL 8.4/InnoDB; Redis Lua and RocketMQ retain the flash-sale admission and
asynchronous-order path; official LightRAG 1.5.7 is the only knowledge boundary and uses
Milvus 2.6.11 for its vector projections. PostgreSQL remains only in explicit
operator-run migration/import packages, and the Go application does not access Milvus
directly.

All repository-local PRE_MERGE gates available on this host passed, including the full Go
suite and race detector, deterministic AI evaluation, frontend tests/build, architecture
and spec checks, MySQL static tests, and the LightRAG/Milvus/fence static suites. This is
not production-ready evidence: this host has no Docker and no configured real MySQL,
Redis or RocketMQ targets, and no authorized provider-backed LightRAG lifecycle or real
PostgreSQL-to-MySQL cutover rehearsal was run.

## Delivered Architecture

- MySQL repositories, independent one-statement-per-file migrations, strict DSN/TLS and
  per-connection session initialization, migration dirty/checksum/lock handling,
  readiness, and operator-only PostgreSQL copy/resume/verify tooling.
- Redis Lua atomic stock reservation and one-user-one-order, RocketMQ transactional
  delivery and recovery, MySQL final guards/idempotency/release jobs, bounded dependency
  readiness, and a process hard-stop for the SDK's non-cancellable consumer start.
- One official LightRAG service replica with two Gunicorn workers, native JsonKV,
  NetworkX and JsonDocStatus persistence, and MilvusVectorDBStorage for chunk/entity/
  relationship vectors only.
- Authenticated Milvus with separate bootstrap root and steady-state runtime identities.
  The runtime role is pinned to `DatabaseAdmin` and `CollectionReadWrite` in database
  `lightrag`, plus `ListDatabases` and `RenameCollection`; broad cluster/admin groups are
  rejected and runtime create-database/create-user probes must be denied.
- Guarded LightRAG empty bootstrap, steady-state startup, five-state rebuild fence,
  immutable attempt/report evidence, exact schema/ID checks, crash recovery, bounded
  signal cleanup, and complete four-component backup/restore contracts. Host bind roots
  are validated component-by-component without following links before creation or
  privileged mutation; the bounded initializer validates both full trees before freezing
  them. Fence readers retain no-follow directory descriptors and use a persisted shared
  GID without changing the Go distroless container's primary UID.
- Baseline and advanced Assistant retrieval both use LightRAG. Advanced mode adds the
  Game Copilot, Research and Planning Agent orchestration; all Assistant tools remain
  read-only. Public knowledge search remains unauthenticated but has a constant-memory
  global rate and in-flight guard.
- Legacy PostgreSQL migrations `001` through `007` remain immutable. No automatic
  production copy, vector rebuild, destructive source retirement, or traffic switch is
  performed by normal startup.

## Acceptance Criteria

| Criterion | Delivered evidence | Result |
|---|---|---|
| AC1 — MySQL-only runtime | composition/import scan and all normal Go packages pass; pgx is isolated to operator migration/import | PASS — LOCAL |
| AC2 — MySQL schema | 25 MySQL migrations and schema/repository tests pass | PARTIAL — REAL MYSQL BLOCKED |
| AC3 — migration history | dirty/checksum/GET_LOCK/repair implementation and deterministic tests pass | PARTIAL — REAL MYSQL BLOCKED |
| AC4 — relational compatibility | account/chat/catalog/community/promotion/order/memory unit, HTTP and race tests pass | PARTIAL — REAL MYSQL BLOCKED |
| AC5 — transactional invariants | lock order, replay, commit ambiguity, state CHECK and retry tests pass | PARTIAL — LIVE CONTENTION BLOCKED |
| AC6 — flash-sale integrity | Redis Lua/RocketMQ/MySQL paths and unit/race tests pass; timestamp precision is normalized to the Redis millisecond contract | PARTIAL — LIVE MIDDLEWARE BLOCKED |
| AC7 — LightRAG-only knowledge | architecture and client/importer tests prove no SQL knowledge fallback or direct application Milvus path | PASS — LOCAL |
| AC8 — official Milvus backend | pinned Compose, exact configuration, RBAC initializer and schema/fence static tests pass | PARTIAL — DOCKER/LIVE BLOCKED |
| AC9 — Milvus lifecycle | lifecycle/backup/restore runner and fault contracts are implemented | BLOCKED — LIVE LIFECYCLE NOT RUN |
| AC10 — controlled vector migration | all three pinned rebuild calls, exact evidence validation and 189 host/preparer/controller/guard metadata and fault tests pass | PARTIAL — PAID LIVE REBUILD NOT RUN |
| AC11 — safe data cutover | copy/resume/authenticated read-only verify implementation and adversarial tests pass | BLOCKED — V27/V28 NOT RUN |
| AC12 — deployment/security | fail-closed TLS/HTTPS/ACL/config tests and static deployment checks pass | PARTIAL — REAL INFRASTRUCTURE BLOCKED |
| AC13 — compatibility/safety | full local Go/race/HTTP/eval suite and public-search capacity tests pass | PARTIAL — DEPLOYMENT SMOKE BLOCKED |
| AC14 — verification/delivery | local `make ci` passes and evidence is recorded below | BLOCKED — LIVE/CUTOVER/CLEAN REMOTE CI REQUIRED |

## Executed Verification

| Gate | Executed command/evidence | Result |
|---|---|---|
| Complete local PRE_MERGE | `GOCACHE=/private/tmp/xlh-go-cache PYTHONPYCACHEPREFIX=/private/tmp/xlh-pycache make ci BASE_REF=HEAD^` | PASS |
| Go unit packages | `go test -count=1 ./...` through `make ci` | PASS |
| Go race packages | `go test -race -count=1 ./...` through `make ci`; importer completed in about 231 seconds | PASS |
| Go static/style | `go vet ./...`, `fmt-check`, hooks, architecture and spec-drift through `make ci` | PASS |
| Deterministic Agent eval | `go run ./cmd/eval-assistant`; `passed: true`, exact Milvus/embedding metadata | PASS |
| Frontend | Vitest: 6 files / 80 tests; production build; entry 246161/512000 bytes | PASS |
| MySQL static | repository, transaction and schema packages through `make ci` | PASS |
| LightRAG/Milvus static | Compose Go checker, Bash contract checks and 3 Milvus init tests through `make ci` | PASS |
| Rebuild fence static | 189 Python metadata/static tests: 64 full-tree ownership-preparer, 29 host path, 85 controller and 11 guarded-start cases, including zero-mutation rejection, symlink/hardlink/alias/device defense, stable lock-inode and shared-`flock`-domain checks, sealed final-publish failure injection, post-operation staging faults, real SIGKILL publish boundaries and host recovery recognition, exact ownership/modes, report-to-marker recovery, serving-lease exclusion, fixed-directory reads, lock-error cleanup, signal forwarding and process-group cleanup; this count does not simulate real UID 1000/65532 container reads, which remain a separate blocked lifecycle gate | PASS — LOCAL STATIC |
| Milvus bootstrap static | 3 Python RBAC/bootstrap tests plus Go Compose/workflow contract tests, including mandatory guarded bootstrap/steady commands and explicit shared-GID propagation | PASS |
| Flash-sale precision regression | unit and race tests for entity, RocketMQ, MySQL repository and usecase packages | PASS |
| Working-tree hygiene | `git diff --check` | PASS |

The complete local command above was run from scratch after the final V30 observability,
failed-report vocabulary, serving-lease, Compose-contract and documentation patches. It
completed with exit code zero; no earlier partial run is used as final evidence.

## Environment-Blocked Verification

| Gate | Observed blocker | Result |
|---|---|---|
| `make mysql-live` | `XLH_MYSQL_TEST_DSN is required` | ENVIRONMENT BLOCKED — NOT RUN |
| `make redis-live` | `XLH_TEST_REDIS_URL is required` | ENVIRONMENT BLOCKED — NOT RUN |
| `make rocketmq-live` | `XLH_TEST_ROCKETMQ_NAMESERVERS is required` | ENVIRONMENT BLOCKED — NOT RUN |
| `make fence-live` | real fence directory/generation/contract were not provided | ENVIRONMENT BLOCKED — NOT RUN |
| `make milvus-live` / `make lightrag-live` | `docker: command not found` | ENVIRONMENT BLOCKED — NOT RUN |
| `make docker-build` | Docker is unavailable | ENVIRONMENT BLOCKED — NOT RUN |
| Isolated LightRAG lifecycle | requires Docker plus separate destructive-test/provider-cost authorization | BLOCKED — NOT RUN |
| V27 relational copy/resume | no real PostgreSQL source and isolated MySQL 8.4 target were supplied | BLOCKED — NOT RUN |
| V28 authenticated read-only verify | V27 artifacts and before/after filesystem/database evidence do not exist | BLOCKED — NOT RUN |
| V45 real dependency smoke | no Docker/real middleware stack is available | ENVIRONMENT BLOCKED — NOT RUN |
| Production restore/cutover | requires separately approved infrastructure, credentials and rollout window | ROLLOUT — NOT RUN |

## Residual Risk And Required Rollout Evidence

The branch is implementation-complete for the approved architecture but is not ready for
production traffic. Before a production decision, operators must still:

1. Run the pinned MySQL 8.4 schema, connection-replacement, collation, uniqueness,
   affected-row, lock-plan and concurrent transaction suites.
2. Run real authenticated Redis and RocketMQ integration, recovery, retry and DLQ
   exercises, then the V45 black-box container smoke.
3. Run the isolated official LightRAG/Milvus lifecycle, including ingestion/query, clean
   restart, whole consistency-unit backup/restore, schema/ID verification and negative
   restore cases. Paid rebuild calls require separate approval.
4. Complete V27 copy/resume and V28 strictly read-only verification against disposable
   real PostgreSQL and MySQL 8.4 systems, preserving signed immutable evidence.
5. Provision private production services, rotate distinct credentials, configure verified
   TLS/ACLs, monitoring and backups, rehearse restore, and approve the write freeze,
   traffic switch and observation/rollback window.

PostgreSQL source data and any NanoVectorDB-era workspace must remain read-only and
recoverable through the approved rollback window. They are not deleted by this delivery.

The bootstrap operator UID and Docker control plane are trusted deployment principals.
Host preflight descriptors cannot be handed through Docker's later bind-source pathname
resolution; the privileged initializer independently validates the objects actually
mounted, but the system does not claim protection from an untrusted same-UID process that
can replace an otherwise allowlist-compatible leaf in that interval. Production hosts
must not share that identity or Docker authority with untrusted principals.
