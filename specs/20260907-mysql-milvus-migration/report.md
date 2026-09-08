# Delivery Report

- Status: `IMPLEMENTATION DELIVERED — HISTORICAL CI/LIVE/CONTAINER PASS; FULL LIFECYCLE/CUTOVER/ROLLOUT BLOCKED`
- Authoritative spec: `./spec.md`
- Evidence date: 2026-09-09

## Outcome

The approved implementation is present in the current branch. Normal business runtime
uses MySQL 8.4/InnoDB; Redis Lua and RocketMQ retain the flash-sale admission and
asynchronous-order path; official LightRAG 1.5.7 is the only knowledge boundary and uses
Milvus 2.6.11 for its vector projections. PostgreSQL remains only in explicit
operator-run migration/import packages, and the Go application does not access Milvus
directly.

GitHub Actions run `34259498765` passed at commit `5c854dd`: repository gates,
official LightRAG/Milvus empty bootstrap, steady-state live and RBAC checks, pinned MySQL
8.4, Redis, RocketMQ, repeated seed and V45 container smoke were all green. This is
historical clean-checkout evidence for that commit only. The current patch passes
full local `make ci BASE_REF=HEAD`; remote CI for its exact commit is pending.
It does not prove a paid provider-backed rebuild, complete restart/backup/restore
lifecycle, real PostgreSQL-to-MySQL V27/V28 cutover rehearsal, or production resources,
credentials and rollout. This delivery is not production ready.

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
  `milvus-init` keeps the bare server URI so root can list/create `lightrag`;
  `lightrag-bootstrap` and steady-state `lightrag` use `/lightrag` plus
  `MILVUS_DB_NAME=lightrag`, making the first PyMilvus 3.0.0 client context the target DB.
  The runtime role retains `DatabaseAdmin` and `CollectionReadWrite` in `lightrag`, plus
  cluster-scoped `ListDatabases` and `RenameCollection`; it receives no database-scoped
  grant in `default`, and create-database/create-user and broad cluster/admin access
  remain denied. The official LightRAG adapter is not forked.
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
| AC2 — MySQL schema | 27 MySQL migrations and schema/repository tests pass | PASS — GITHUB MYSQL 8.4 |
| AC3 — migration history | dirty/checksum/GET_LOCK/repair implementation and deterministic plus live migration tests pass | PARTIAL — DESTRUCTIVE CRASH/REPAIR REHEARSAL NOT RUN |
| AC4 — relational compatibility | repository, HTTP, race, MySQL 8.4 and container-smoke gates pass | PASS — GITHUB MYSQL/CONTAINER |
| AC5 — transactional invariants | lock order, replay, commit ambiguity, state CHECK, retry and bounded release-claim regressions pass | PASS — GITHUB MYSQL 8.4 |
| AC6 — flash-sale integrity | MySQL claim, Redis admission/recovery including explicit-close precedence, RocketMQ integration and V45 flash-sale smoke pass | PASS — GITHUB RUN 34259498765 |
| AC7 — LightRAG-only knowledge | architecture and client/importer tests prove no SQL knowledge fallback or direct application Milvus path | PASS — LOCAL |
| AC8 — official Milvus backend | two-stage URI, empty bootstrap, live first connection, schema and RBAC allow/deny contracts pass | PASS — GITHUB LIGHTRAG/MILVUS |
| AC9 — Milvus lifecycle | lifecycle/backup/restore runner and fault contracts are implemented | BLOCKED — LIVE LIFECYCLE NOT RUN |
| AC10 — controlled vector migration | all three pinned rebuild calls, exact evidence validation and 190 host/preparer/controller/guard metadata and fault tests pass | PARTIAL — PAID LIVE REBUILD NOT RUN |
| AC11 — safe data cutover | copy/resume/authenticated read-only verify implementation and adversarial tests pass | BLOCKED — V27/V28 NOT RUN |
| AC12 — deployment/security | fail-closed TLS/HTTPS/ACL/config tests and static deployment checks pass | PARTIAL — REAL INFRASTRUCTURE BLOCKED |
| AC13 — compatibility/safety | full Go/race/HTTP/eval suite, public-search capacity and V45 container smoke pass | PASS — GITHUB CONTAINER SMOKE |
| AC14 — verification/delivery | standard repository/live middleware/knowledge/seed/container gates pass for `5c854dd`; full lifecycle and V27/V28 do not | BLOCKED — CUTOVER/LIFECYCLE REQUIRED |

## Executed Verification

Unless a row explicitly says current, the PASS evidence below predates the current
uncommitted patch; run `34259498765` is bound to commit `5c854dd`.

| Gate | Executed command/evidence | Result |
|---|---|---|
| Historical local PRE_MERGE | `GOCACHE=/private/tmp/xlh-go-cache PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX=/private/tmp/xlh-pycache make ci BASE_REF=HEAD^` | PASS — PRE-CURRENT-PATCH SNAPSHOT |
| Historical MySQL metadata regression | `GOCACHE=/private/tmp/xlh-go-cache-mysql go test -count=1 ./internal/adapter/mysql -run 'TestCanonicalSQLExpression|TestRepairCreateTableAcceptsMySQL84|TestRepairCreateTableRejects'` | PASS — PRE-CURRENT-PATCH SNAPSHOT |
| Linux GitHub Actions | run `34259498765`, commit `5c854dd` | PASS — REPOSITORY, LIGHTRAG/MILVUS BOOTSTRAP/LIVE/RBAC, MYSQL 8.4, REDIS, ROCKETMQ, SEED AND CONTAINER SMOKE |
| Current patch full local CI | `GOCACHE=/private/tmp/xlh-go-cache PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX=/private/tmp/xlh-pycache make ci BASE_REF=HEAD` | PASS — all repository gates after final safety fixes |
| Go unit packages | `go test -count=1 ./...` through current `make ci` | PASS — CURRENT LOCAL |
| Go race packages | `go test -race -count=1 ./...` through current `make ci`; importer completed in about 212 seconds | PASS — CURRENT LOCAL |
| Go static/style | `go vet ./...`, `fmt-check`, hooks, architecture and spec-drift through current `make ci` | PASS — CURRENT LOCAL |
| Deterministic Agent eval | `go run ./cmd/eval-assistant`; `passed: true`, exact Milvus/embedding metadata | PASS — CURRENT LOCAL |
| Frontend | 7 files / 90 tests; production build; entry 256385/512000 bytes | PASS — CURRENT LOCAL |
| MySQL static | repository, transaction and schema packages through current `make ci` | PASS — CURRENT LOCAL |
| LightRAG/Milvus static | Compose Go checker, Bash contract checks and 3 Milvus init tests through current `make ci` | PASS — CURRENT LOCAL |
| Rebuild fence static | 190 Python metadata/static tests: 64 full-tree ownership-preparer, 29 host path, 86 controller and 11 guarded-start cases, including zero-mutation rejection, symlink/hardlink/alias/device defense, stable lock-inode and shared-`flock`-domain checks, sealed final-publish failure injection, post-operation staging faults, real SIGKILL publish boundaries and host recovery recognition, exact ownership/modes, report-to-marker recovery, serving-lease exclusion, fixed-directory reads, deferred-LightRAG argv isolation, lock-error cleanup, signal forwarding and process-group cleanup; this count does not simulate real UID 1000/65532 container reads, which remain a separate blocked lifecycle gate | PASS — LOCAL STATIC |
| Milvus bootstrap static | 3 Python RBAC/bootstrap tests plus Go Compose/workflow contract tests, including mandatory guarded bootstrap/steady commands and explicit shared-GID propagation | PASS |
| Two-stage Milvus URI checks | one bare `milvus-init` URI, two `/lightrag` LightRAG URIs, shared `MILVUS_DB_NAME=lightrag` and wrong-URI rejection through focused tests and full `make ci` | PASS — LOCAL STATIC/UNIT |
| Flash-sale precision regression | unit and race tests for entity, RocketMQ, MySQL repository and usecase packages | PASS |
| Working-tree hygiene | `git diff --check` after this documentation sync | PASS |

The historical local command above ran after the strict MySQL 8.4 metadata state machine,
bounded release-claim path, tests and specification changes and completed with exit code zero. It covered the full
Go and race suites, deterministic Agent evaluation, 6 frontend files / 80 tests and
production build, architecture/spec hooks, MySQL/deployment static gates and all 190
LightRAG fence metadata/static tests. The current final local CI repeats these
standard gates after the feature patch. `git diff --check` and spec drift are rerun
after the final evidence sync before commit.

## Current Local Host And Remaining Blockers

| Gate | Observed blocker | Result |
|---|---|---|
| MySQL/Redis/RocketMQ live commands | services and connection variables are not provisioned on this host | LOCAL HOST NOT RUN — GITHUB RUN 34259498765 PASS |
| LightRAG/Milvus/fence live commands | Docker stack is not provisioned on this host | LOCAL HOST NOT RUN — GITHUB BOOTSTRAP/LIVE/RBAC PASS |
| Docker build and V45 smoke | Docker is unavailable on this host | LOCAL HOST NOT RUN — GITHUB CONTAINER SMOKE PASS |
| Isolated LightRAG lifecycle | requires Docker plus separate destructive-test/provider-cost authorization | BLOCKED — NOT RUN |
| Paid three-target vector rebuild | requires separate provider-cost and mutation authorization | BLOCKED — NOT RUN |
| V27 relational copy/resume | no real PostgreSQL source and isolated MySQL 8.4 target were supplied | BLOCKED — NOT RUN |
| V28 authenticated read-only verify | V27 artifacts and before/after filesystem/database evidence do not exist | BLOCKED — NOT RUN |
| Production restore/cutover | requires separately approved infrastructure, credentials and rollout window | ROLLOUT — NOT RUN |

## Residual Risk And Required Rollout Evidence

The branch is implementation-complete for the approved architecture but is not ready for
production traffic. Before a production decision, operators must still:

1. Run GitHub Actions on the exact committed patch; local full CI passed while run
   `34259498765` applies only to `5c854dd`.
2. Run the isolated official LightRAG/Milvus lifecycle, including PyMilvus 3.0.0 version
   and first-client database selection, no target collections or database-scoped grants
   in `default`, exact runtime RBAC allow/deny probes, ingestion/query, clean restart,
   whole consistency-unit backup/restore, schema/ID verification and negative restore
   cases. Paid rebuild calls require separate approval.
3. Complete V27 copy/resume and V28 strictly read-only verification against disposable
   real PostgreSQL and MySQL 8.4 systems, preserving signed immutable evidence.
4. Provision private production services, rotate distinct credentials, configure verified
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
