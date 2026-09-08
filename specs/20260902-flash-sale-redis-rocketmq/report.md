# Delivery Report

- Status: `VERIFYING — CURRENT LOCAL FULL CI PASS / REMOTE CI PENDING; PRODUCTION/ROLLOUT NOT READY`
- Execution snapshot: current uncommitted patch 2026-09-09; historical baseline 2026-09-04
- Authoritative spec: ./spec.md

## Outcome

The approved flash-sale slice is implemented across the Go modular monolith,
MySQL migrations 022-027, checked-in Redis Lua scripts, RocketMQ transactional
producer and at-least-once consumer, HTTP contracts, browser flow, opt-in
configuration, local middleware Compose, CI wiring, and deployment guidance. The
feature remains disabled by default with `XLH_FLASH_SALE_ENABLED=false`.

The current uncommitted patch adds admin GET list/detail (including drafts and
management fields), account-page admin create/edit-draft/activate/cancel UI,
public response redaction, and the explicit Reserve precedence of authoritative
MySQL exact replay, activity lookup after a durable miss, Redis replay, durable/Redis
different-key conflict, ownership precheck, then Redis/MQ. Only a pre-durable Redis
`failed/technical_rollback` marker permits a same-key retry, fenced by a strictly
newer reservation timestamp so delayed compensation cannot release the new attempt.
Consumer and Order ownership, uniqueness, and final-stock guards remain in place, as
do normal Redis pending and stale-pending recovery.
Targeted ordinary/race tests and frontend build passed locally. The final
`GOCACHE=/private/tmp/xlh-go-cache PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX=/private/tmp/xlh-pycache make ci BASE_REF=HEAD`
also passed after the last safety fixes. Clean-checkout GitHub Actions is pending.

For the earlier baseline, local deterministic verification was green: the final `make ci
BASE_REF=origin/codex/clean-architecture-refactor` run passed Go vet, all Go tests,
all Go race tests, 80/80
Vitest cases, repository hooks, architecture and spec-drift checks, and the web
production build. `git diff --check` also passed. The Assistant renderer is now
lazy-loaded: the initial entry fell from about 982 kB to 246 kB (about 76 kB
gzip); only optional deferred Streamdown renderer/language chunks retain the
greater-than-500-kB warning.

GitHub run `34259498765` was green at commit `5c854dd`, covering repository,
LightRAG/Milvus bootstrap/live/RBAC, MySQL 8.4, Redis, RocketMQ, repeated seed and
container smoke. That commit predates the current uncommitted patch and is not
current-patch evidence. This local host lacks the configured middleware and Docker,
so those gates were not repeated here. All rollout load/fault/rollback checks remain
not run, and this slice is still **not READY**.

## Acceptance Criteria

| Criterion | Final change | Evidence | Result |
|---|---|---|---|
| AC1 activity lifecycle | admin list/detail plus create/edit-draft/activate/cancel UI; admin sees drafts and management fields while public does not | targeted backend/frontend tests and current local full CI pass | PASS — CURRENT LOCAL |
| AC2 atomic stock admission | Redis-server-time Lua validates state/window/user/stock and atomically decrements one unit | historical Redis live gate passes in run `34259498765`; current patch CI pending | HISTORICAL PASS / CURRENT PATCH CI PENDING |
| AC3 stable idempotency | authoritative MySQL exact replay; Redis replay on durable miss; only pre-durable Redis technical rollback retries with a newer timestamp fence; then durable/Redis different-key conflict before ownership/admission | targeted usecase/MySQL/Lua tests and current local full CI pass; live Redis rerun awaits current GitHub CI | PASS — CURRENT LOCAL / LIVE PENDING |
| AC4 transactional publication | versioned RocketMQ half message, Lua local transaction, exact-marker checker, stale-pending recovery | historical RocketMQ integration passes in run `34259498765`; current patch CI pending | HISTORICAL PASS / CURRENT PATCH CI PENDING |
| AC5 idempotent async order | at-least-once consumer and durable reservation/order replay remain the final duplicate boundary after HTTP prechecks | current local Go/race suites pass; historical RocketMQ integration passes in run `34259498765` | PASS — CURRENT LOCAL / HISTORICAL LIVE |
| AC6 MySQL final guard | consumer/Order ownership, unique request/source and locked final-stock guards are retained | targeted/package race regressions and current local full CI pass | PASS — CURRENT LOCAL |
| AC7 owned request status | authenticated owner/admin lookup for queued, processing, order_ready, failed, expired | current presenter and public/admin Hertz tests pass locally | PASS — CURRENT LOCAL FOCUSED |
| AC8 expiry and release | database-deadline expiry, locked terminal transition, durable leased release job, compare-and-release Lua | historical Redis/MySQL integration passes in run `34259498765`; current patch CI pending | HISTORICAL PASS / CURRENT PATCH CI PENDING |
| AC9 fail closed and compatibility | opt-in wiring, disabled default, fixed error envelope, ordinary regression suite | historical full repository and V45 smoke pass in run `34259498765`; current patch CI pending | HISTORICAL PASS / CURRENT PATCH CI PENDING |
| AC10 config/readiness/operations | strict endpoint/limit/credential validation, enabled-only Redis PING and authenticated RocketMQ publish-route readiness, protected metrics, bounded workers and safe logs | historical live readiness and V45 smoke pass in run `34259498765`; production DLQ/HA inspection remains rollout-only | PARTIAL |
| AC11 browser flow | buyer flow plus admin-only account-page management; non-admin users do not render or request admin management | current frontend 7 files / 90 tests and production build pass in local full CI | PASS — CURRENT LOCAL |
| AC12 complete verification | CI service definitions, live integration tests, Compose and documented rollout/rollback | run `34259498765` passes the historical repository/live/container baseline; current patch CI and rollout gates remain open | BLOCKED |

## Verification

| Gate | Command/environment | Executed evidence | Result |
|---|---|---|---|
| Current uncommitted patch focused checks | AI, Catalog and FlashSale ordinary/race packages; frontend Vitest/build | all pass: 7 files / 90 frontend tests; entry 256,385/512,000 bytes | PASS — CURRENT LOCAL FOCUSED |
| Current uncommitted patch full combined CI | `GOCACHE=/private/tmp/xlh-go-cache PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX=/private/tmp/xlh-pycache make ci BASE_REF=HEAD` after the final safety fixes | vet, all Go/race packages, 8-case eval, 7 files / 90 Vitest tests, hooks, architecture, MySQL/LightRAG static checks, 190 fence tests, spec drift and production build all completed | PASS — CURRENT LOCAL |
| Historical full local PRE_MERGE | earlier pre-MySQL snapshot | vet; Go and race suites; 6 Vitest files / 80 tests; eval; hooks; architecture; spec drift; web build with 246,161/512,000-byte initial-entry gate | PASS — HISTORICAL SNAPSHOT ONLY |
| Current patch hygiene | `git diff --check` | executed after the documentation sync | PASS |
| MySQL/Redis/RocketMQ live | GitHub run `34259498765`, commit `5c854dd` | all three integration gates pass for the historical commit; services are not configured on this host | HISTORICAL PASS / LOCAL HOST NOT RUN |
| Container build and V45 smoke | GitHub run `34259498765`, commit `5c854dd` | build, repeated seed and real-dependency product smoke pass for the historical commit; Docker is unavailable on this host | HISTORICAL PASS / LOCAL HOST NOT RUN |
| Assistant mutation boundary | scan `internal/usecase` and `internal/adapter/eino` for flash-sale mutation references | no matches | PASS |
| Java absence | `rg --files -g '*.java' .` | no Java files | PASS |
| GitHub Actions | clean Linux checkout with MySQL 8.4, Redis and RocketMQ services | run `34259498765` green at commit `5c854dd`; it predates the new revision | HISTORICAL PASS / NEW REVISION PENDING |
| Rollout | isolated persistent target | not authorized or provisioned | NOT RUN |

An exit-zero `go test` command is not counted as a live pass when the named test
reported `SKIP`. Historical run `34259498765` supplies live middleware evidence for
`5c854dd`; it does not validate the current patch.

## Architecture And Implemented Behavior

- `internal/flashsale` owns Entity, UseCase, presenter, HTTP/background entry
  points, and MySQL/Redis/RocketMQ/Order repository adapters. Provider types
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
- MySQL locks the activity row as the final stock boundary, stores frozen
  commercial data, enforces request/activity-user/source-order uniqueness, and
  drives idempotent expiry and Redis release jobs. Database statement time is used
  at activation/payment deadline boundaries.
- Public reads hide drafts, `totalStock`, and `paymentTimeoutSeconds`; admin GET
  list/detail includes them and requires authentication plus admin role without an
  Origin check. Reservation and admin mutations retain same-origin enforcement. A
  202 response means queued admission, not an existing order.
- Reserve derives identity and first returns an authoritative MySQL exact replay.
  After a durable miss it validates activity existence, checks Redis replay, and lets
  only a pre-durable Redis `failed/technical_rollback` marker retry behind a newer
  timestamp fence. It rejects a same-activity/user different key
  from either store as `already_reserved`, and runs ownership precheck only for a
  new request before Redis/MQ. Downstream consumer/Order guards remain authoritative.
- The browser uses one stable idempotency key per attempt, disables accidental
  duplicate submission, performs bounded cancellable polling, and links to an
  order only after `order_ready`. The account page renders flash-sale
  create/edit-draft/activate/cancel management only for admins.
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
- The migration, Lua semantics and RocketMQ client compatibility passed pinned-service
  gates for historical commit `5c854dd`; the current patch still needs exact-revision
  CI. Cross-service production crash boundaries and reconciliation remain rollout
  evidence rather than inferred from the CI smoke.
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
occurred. The current patch has passed local CI, but clean-checkout GitHub and
rollout gates remain pending, so production is not ready. T13-T15 and V24-V26 remain separately gated. After an isolated target is
approved, apply the current MySQL migrations while the feature is disabled, run the
MySQL/Redis/RocketMQ and product smoke gates, create the topic/groups/DLQ,
enable one small activity, then execute the recorded load/fault/reconciliation
matrix before exposing navigation.

Rollback stops intake by cancelling activities or setting
`XLH_FLASH_SALE_ENABLED=false`, continues draining/reconciling accepted requests,
and redeploys the previous application while retaining additive schema, Redis
markers and broker data. Redis keys, RocketMQ storage and database rows must not
be deleted as an application rollback step.

## Requirements To Reach READY

1. Run GitHub Actions on the exact committed revision; current local full CI passed,
   while historical run `34259498765` is not a substitute for the new revision.
2. Reconcile that current-revision evidence into T2, T4-T6, T11-T12 and V3-V6, V8,
   V10-V16, V18, V20, V22-V23 and V27-V28. Only then may PRE_MERGE move to READY. Rollout
   readiness and performance claims still require the separately approved
   V24-V26 exercises.
