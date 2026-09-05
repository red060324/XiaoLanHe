# Delivery Report

- Status: `VERIFYING — LOCAL CODE/POSTGRES GATES PASS; EXTERNAL REDIS/MQ GATES BLOCKED`
- Execution snapshot: 2026-09-04
- Authoritative spec: ./spec.md

## Outcome

The approved flash-sale slice is implemented across the Go modular monolith,
PostgreSQL migration 006, checked-in Redis Lua scripts, RocketMQ transactional
producer and at-least-once consumer, HTTP contracts, browser flow, opt-in
configuration, local middleware Compose, CI wiring, and deployment guidance. The
feature remains disabled by default with `XLH_FLASH_SALE_ENABLED=false`.

Local deterministic verification is green: the final `make ci
BASE_REF=origin/codex/clean-architecture-refactor` run passed Go vet, all Go tests,
all Go race tests, 80/80
Vitest cases, repository hooks, architecture and spec-drift checks, and the web
production build. `git diff --check` also passed. The Assistant renderer is now
lazy-loaded: the initial entry fell from about 982 kB to 246 kB (about 76 kB
gzip); only optional deferred Streamdown renderer/language chunks retain the
greater-than-500-kB warning.

PostgreSQL 17 + pgvector integration now runs and passes in both ordinary and race
suites. This slice is still **not READY** because the required live Redis 7.4 and
RocketMQ 5.3.2 tests cannot run without those services. Docker is not installed in
the execution environment, so
Compose validation, dependency health/persistence, image build, and product smoke
were not run. GitHub Actions and all rollout load/fault/rollback checks were also
not run.

## Acceptance Criteria

| Criterion | Final change | Evidence | Result |
|---|---|---|---|
| AC1 activity lifecycle | activity entity/usecase, catalog identity validation, draft versioning, interval-safe PostgreSQL activation, admin HTTP | unit/admin wire plus PostgreSQL activation concurrency tests pass | PASS |
| AC2 atomic stock admission | Redis-server-time Lua validates state/window/user/stock and atomically decrements one unit | scripts and adapter compile; high-contention/exact-end live tests skipped without Redis | ENVIRONMENT BLOCKED |
| AC3 stable idempotency | SHA-256 digest-bound request ID, same-key replay, different-key one-user conflict, no raw key persistence | usecase/parser tests pass; same-user live Lua race skipped without Redis | PARTIAL |
| AC4 transactional publication | versioned RocketMQ half message, Lua local transaction, exact-marker checker, stale-pending recovery | commit/rollback/unknown, codec, recovery and concurrent correlation unit/race tests pass; live broker test skipped | PARTIAL |
| AC5 idempotent async order | at-least-once consumer, durable reservation/order replay, retry mapping and broker DLQ policy | consumer/order/replay and PostgreSQL duplicate-delivery tests pass; live broker still blocked | PARTIAL |
| AC6 PostgreSQL final guard | locked activity allocation, unique identities, frozen snapshot, idempotent compensation job | live PostgreSQL final-stock concurrency and replay pass | PASS |
| AC7 owned request status | authenticated owner/admin lookup for queued, processing, order_ready, failed, expired | presenter and public/admin Hertz wire tests pass | PASS |
| AC8 expiry and release | database-deadline expiry, locked terminal transition, durable leased release job, compare-and-release Lua | concurrent PostgreSQL expiry and worker tests pass; live Redis release is blocked | PARTIAL |
| AC9 fail closed and compatibility | opt-in wiring, disabled default, fixed error envelope, ordinary regression suite | full local unit/race/HTTP suite passes; enabled live dependency outage/product smoke not run | PARTIAL |
| AC10 config/readiness/operations | strict endpoint/limit/credential validation, enabled-only Redis PING and authenticated RocketMQ publish-route readiness, protected metrics, bounded workers and safe logs | config/readiness/lifecycle/log/cardinality race tests pass, including empty routes and concurrent probes; real service readiness and broker DLQ inspection not run | PARTIAL |
| AC11 browser flow | activity UI, stable retry key, bounded abortable polling, terminal states and order navigation | focused Commerce/API tests pass within 80/80 Vitest; production build passes | PASS |
| AC12 complete verification | CI service definitions, live integration tests, Compose and documented rollout/rollback | local `make ci`, diff, architecture, Assistant read-only and Java-absence gates pass; live/container/GitHub/rollout gates remain open | BLOCKED |

## Verification

| Gate | Command/environment | Executed evidence | Result |
|---|---|---|---|
| Full local PRE_MERGE | `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 URL> make ci BASE_REF=origin/codex/clean-architecture-refactor` | vet; Go and race suites; 6 Vitest files / 80 tests; eval; hooks; architecture; LightRAG static; spec drift; web build with 246,161/512,000-byte initial-entry gate | PASS (exit 0) |
| Patch hygiene | `git diff --check` | exit 0 | PASS |
| PostgreSQL live | `TestProductPostgres` through the full ordinary and race gates | PostgreSQL 17.11 + pgvector 0.8.6, including migration 006 and flash-sale concurrency | PASS |
| Redis live | `go test -v ./internal/flashsale/repository/redis -run '^TestRedis' -count=1` | all nine top-level lifecycle, contention, time boundary, NOSCRIPT, cancellation and recovery tests discovered; live cases skipped with `XLH_TEST_REDIS_URL is not set` | ENVIRONMENT BLOCKED |
| RocketMQ live | `go test -v ./internal/flashsale/repository/rocketmq -run 'Integration$' -count=1` | transaction commit/rollback test discovered; skipped with NameServer/broker variables unset | ENVIRONMENT BLOCKED |
| Compose validation | `make middleware-config` | `make: docker: No such file or directory` | ENVIRONMENT BLOCKED |
| Container build | `make docker-build` | `make: docker: No such file or directory` | ENVIRONMENT BLOCKED |
| Assistant mutation boundary | scan `internal/usecase` and `internal/adapter/eino` for flash-sale mutation references | no matches | PASS |
| Java absence | `rg --files -g '*.java' .` | no Java files | PASS |
| GitHub Actions | clean Linux checkout with PostgreSQL, Redis and RocketMQ services | workflow updated but not executed in this task | NOT RUN |
| Rollout | isolated persistent target | not authorized or provisioned | NOT RUN |

An exit-zero `go test` command is not counted as a live pass when the named test
reported `SKIP`. The full local CI result therefore proves compilation and
deterministic behavior, not live middleware compatibility.

## Architecture And Implemented Behavior

- `internal/flashsale` owns Entity, UseCase, presenter, HTTP/background entry
  points, and PostgreSQL/Redis/RocketMQ/Order repository adapters. Provider types
  remain at adapter boundaries and the architecture gate passes.
- Redis keys share an activity hash tag. Lua uses Redis server time and maintains
  activity metadata, remaining stock, one-user markers, request markers, and a
  leased stale-pending set. Exact replay is bound to activity, user, digest and
  reservation timestamp; only technical rollback removes the buyer marker.
- RocketMQ uses versioned JSON, transactional publication and a marker-backed
  transaction checker. Delivery is explicitly at-least-once. The consumer ACKs
  only after fulfilment and pending completion; broker retry limits provide the
  configured DLQ path. Enabled readiness uses configured ACL credentials to query
  the topic route and requires a publishable queue, with one in-flight probe to
  bound admin-client creation. No exactly-once claim is made.
- PostgreSQL locks the activity row as the final stock boundary, stores frozen
  commercial data, enforces request/activity-user/source-order uniqueness, and
  drives idempotent expiry and Redis release jobs. Database statement time is used
  at activation/payment deadline boundaries.
- Public/admin APIs implement list/detail/reserve/status and
  create/update/activate/cancel with authentication, ownership, admin and
  same-origin enforcement. A 202 response means queued/replayed admission, not an
  existing order.
- The browser uses one stable idempotency key per attempt, disables accidental
  duplicate submission, performs bounded cancellable polling, and links to an
  order only after `order_ready`.
- A protected process-local Prometheus registry records fixed-enum Lua admission/
  release, RocketMQ transaction/check/consume, final-guard, recovery, expiry and
  release outcomes, processed counts and pending-age histograms. IDs and content
  are never labels; enabling flash sale requires a distinct 32-512 character
  `XLH_METRICS_TOKEN`.
- The existing Assistant has no flash-sale or order mutation capability. Ordinary
  commerce and sandbox payment remain the only payment behavior; real payment is
  not implemented.

## Retained Debt And Residual Risk

- Structured safe logs and protected bounded application metrics are implemented.
  Broker DLQ depth, release-job backlog and database/Redis stock drift remain
  deployment probes/alerts because they require cross-system state or broker admin
  telemetry and are not fabricated from one process event.
- Poison messages return retry and rely on RocketMQ's configured max reconsume
  count to enter the broker DLQ. Actual DLQ creation, routing, inspection and alert
  delivery require a live broker exercise.
- The migration, Lua semantics and RocketMQ client compatibility are covered by
  executable live tests but remain unproven until those tests run against the
  pinned services. Cross-service crash boundaries and reconciliation remain
  partially proven by deterministic fakes only.
- The local Compose broker is single-node integration infrastructure, not a
  production HA topology. Redis replication/TLS/backup, RocketMQ ACL/TLS/HA and
  persistent private networking depend on the selected deployment provider.
- No throughput, p95 latency, capacity, HA, production safety or exactly-once
  result is claimed. The initial frontend entry is code-split; optional deferred
  Mermaid/Shiki renderer chunks still carry a chunk-size warning.
- The worktree contains pre-existing unrelated uncommitted changes. They were
  preserved; no reset, checkout, commit or push was performed.

## Rollout And Rollback

No infrastructure was purchased, provisioned or mutated and no public rollout
occurred. T13-T15 and V24-V26 remain separately gated. After an isolated target is
approved, apply migration 006 while the feature is disabled, run the live
PostgreSQL/Redis/RocketMQ and product smoke gates, create the topic/groups/DLQ,
enable one small activity, then execute the recorded load/fault/reconciliation
matrix before exposing navigation.

Rollback stops intake by cancelling activities or setting
`XLH_FLASH_SALE_ENABLED=false`, continues draining/reconciling accepted requests,
and redeploys the previous application while retaining additive schema, Redis
markers and broker data. Redis keys, RocketMQ storage and database rows must not
be deleted as an application rollback step.

## Requirements To Reach READY

1. Provide isolated PostgreSQL/pgvector, Redis 7.4 and RocketMQ 5.3.2 endpoints,
   set the four `XLH_TEST_*` variables, and run all three live test commands above
   with no skips.
2. Run `make middleware-config`, `make middleware-up`, dependency health and
   persistence restart checks, `make docker-build`, and the seeded product smoke
   on a Docker-capable host.
3. Run the updated GitHub Actions workflow on a clean Linux checkout and retain
   the successful run link/ID.
4. Reconcile the resulting evidence into T2, T4-T6, T11-T12 and V3-V6, V8,
   V10-V16, V18, V20 and V23. Only then may PRE_MERGE move to READY. Rollout
   readiness and performance claims still require the separately approved
   V24-V26 exercises.
