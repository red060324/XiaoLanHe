# Test Plan

- Status: `LOCAL CODE/POSTGRES GATES PASS — EXTERNAL REDIS/MQ GATES BLOCKED` (2026-09-04)

## Scope And Environments

Unit tests use deterministic fakes for Redis-script results, MQ publication and
redelivery, clock, background leases, and Order capability. Integration uses
isolated PostgreSQL plus pgvector, Redis 7.4, and RocketMQ 5.3.2 with no production
credentials or data. Frontend uses Vitest. Rollout load/fault checks run only
after the target and side effects are approved.

## Cases

| ID | Class | Layer | Scenario | Expected result | Command/evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | entity/unit | draft/active/cancelled/ended transitions, interval, price, stock, immutability | deterministic valid transitions; invalid state rejected | `TestActivityValidateDraft`, `TestActivityLifecycle`, `TestActivityAcceptsReservationTime` | PASS |
| V2 | PRE_MERGE | usecase/unit | unauthenticated/user/admin boundaries and server-resolved activity/price | no client-controlled identity/price/stock; unauthorized calls do zero downstream work | flashsale UseCase plus HTTP auth/origin tests in `make ci` | PASS |
| V3 | PRE_MERGE | Redis/Lua integration | before start, active, exact end, cancelled, exhausted, malformed/missing state | server-time boundary is exact; missing/invalid state fails closed | live tests discovered but skipped: `XLH_TEST_REDIS_URL is not set` | ENVIRONMENT BLOCKED |
| V4 | PRE_MERGE | Redis/concurrency | stock N, more than N distinct users race | exactly N new admissions and stock zero; never negative | high-contention live test implemented; skipped without Redis | ENVIRONMENT BLOCKED |
| V5 | PRE_MERGE | Redis/concurrency | one user races same and different idempotency keys | one decrement; same digest replays stable ID; different digest conflicts | same/different-key live subtests implemented; both skipped without Redis | ENVIRONMENT BLOCKED |
| V6 | PRE_MERGE | Redis/failure | NOSCRIPT, timeout, cancellation, unavailable Redis, compare/release replay | one bounded reload; cancellation propagates; dependency fails closed; release increments once | script parsing/outcome unit tests pass; NOSCRIPT/cancel/release live cases skipped | PARTIAL |
| V7 | PRE_MERGE | MQ/unit | half-message send, Lua commit/reject/error, transaction check present/absent/uncertain | correct COMMIT/ROLLBACK/UNKNOWN mapping with bounded time and safe payload | producer/listener/checker/codec tests pass, including concurrent correlation under race | PASS |
| V8 | PRE_MERGE | MQ integration | live broker commit and rollback | only committed valid event reaches configured consumer group | live test skipped: RocketMQ NameServer/broker variables are unset | ENVIRONMENT BLOCKED |
| V9 | PRE_MERGE | consumer/unit | malformed/version-unknown/oversized message, transient error, permanent error | poison/retry/ack mapping follows contract; no unbounded goroutine | codec and consumer retry/lifecycle unit tests pass | PASS |
| V10 | PRE_MERGE | PostgreSQL integration | new event, exact replay, duplicate concurrent delivery, different request same user | one allocation/reservation/order; stable result; no oversell | `TestProductPostgres` passed against PostgreSQL 17 + pgvector in ordinary and race suites | PASS |
| V11 | PRE_MERGE | defense in depth | durable concurrent allocation receives more events than stock | PostgreSQL final guard caps allocation and queues idempotent compensation | live PostgreSQL concurrency/final-guard test PASS; combined deliberately over-primed Redis exercise remains rollout evidence | PARTIAL |
| V12 | PRE_MERGE | crash/recovery | failure after allocation, after order commit, before MQ ACK, transaction commit uncertainty | redelivery/recovery completes once without duplicate order or allocation | deterministic replay/recovery/checker tests pass; live combined failure path not run | PARTIAL |
| V13 | PRE_MERGE | expiry | database time crosses deadline while pending; paid race; multiple reapers | pending order expires/releases once; paid wins or expiry wins under lock, never both | concurrent PostgreSQL expiry and worker batch tests PASS | PASS |
| V14 | PRE_MERGE | release/recovery | Redis unavailable during release, worker restart, stale pending duplicate publication | durable job retries; stock restores once; duplicate events replay safely | worker retry/lease/idempotency tests pass; Redis completion integration skipped | PARTIAL |
| V15 | PRE_MERGE | migration | clean 001-006, upgrade populated 001-005, rerun/checksum/multi-start | additive compatibility, constraints/indexes and migration lock pass | isolated PostgreSQL 17 + pgvector integration PASS | PASS |
| V16 | PRE_MERGE | order regression | ordinary order with/without coupon, sandbox payment replay, ownership/entitlement | existing contracts unchanged; flash order source/deadline enforced | unit/HTTP and PostgreSQL-backed regression suites PASS | PASS |
| V17 | PRE_MERGE | HTTP | create/list/detail/reserve/status/admin activate/cancel and mapped errors | documented status/envelope/ownership/origin/idempotency behavior | presenter and public/admin Hertz wire tests pass | PASS |
| V18 | PRE_MERGE | cancellation | client cancels reservation/status; app shuts down with in-flight consumer/workers | request work cancels; consumer drains boundedly; no leaked goroutine/lease | frontend abort, worker shutdown, producer/consumer lifecycle and race suites pass; no live in-flight broker shutdown | PARTIAL |
| V19 | PRE_MERGE | config/readiness | feature disabled, enabled valid, missing/malformed secrets/endpoints/limits, dependency outage, missing/empty RocketMQ publish route, concurrent probes | strict startup validation; clients absent when disabled; Redis authentication is mandatory; enabled readiness reflects dependencies and bounds admin clients | config validation plus Redis PING and authenticated RocketMQ publish-route readiness success/fail-closed/race tests pass | PASS |
| V20 | PRE_MERGE | observability/security | admission/transaction/check/consume/final-guard/recovery/expiry/release results, processed counts and pending age | protected Prometheus series use fixed labels; safe logs retain correlation without raw key, URL, credential, cookie or payload | registry cardinality, transaction/worker/release redaction and race tests PASS; broker DLQ depth and cross-store drift remain external probes | PASS |
| V21 | PRE_MERGE | frontend | activity states, reserve once, retry, bounded polling, cancellation, order-ready/exhausted/error/expired | accessible UI and no stale result or accidental duplicate mutation | focused Commerce/API cases pass within 80/80 Vitest; production build and 500 KiB initial-entry budget pass | PASS |
| V22 | PRE_MERGE | static | format, vet, race, frontend, hooks, architecture, spec drift, public-only and Java absence | all pass with actual tests executed | latest `make ci BASE_REF=origin/codex/clean-architecture-refactor`, `git diff --check`, no Assistant mutation references, no Java files | PASS |
| V23 | PRE_MERGE | deployment | Compose config, dependency health, persistent restart, app readiness, seed and product smoke | reproducible local stack; secure production guidance | Compose/docs exist; both Compose validation and image build fail because Docker is absent | ENVIRONMENT BLOCKED |
| V24 | ROLLOUT | load | stock=N, at least N+1 distinct authenticated users plus same-user replay storm | exactly N accepted/order-ready maximum, one per user, actual throughput/p95/error counts recorded | requires approved isolated target | NOT RUN |
| V25 | ROLLOUT | fault | interrupt app, Redis, broker, consumer and PostgreSQL at named boundaries | fail closed or retry/recover per contract; DLQ/backlog/drift observable; no oversell | requires approved isolated target and fault side effects | NOT RUN |
| V26 | ROLLOUT | rollback | cancel intake, drain/inspect backlog, disable feature, roll back app, retain additive schema | ordinary product remains available and no accepted request is silently lost | requires approved deployed target | NOT RUN |

## Concurrency Invariants

Every repeatable concurrent test records configured stock, unique users, total
requests, accepted new requests, replays, conflicts, durable reservations, orders,
allocated stock, Redis remaining stock, release jobs, and DLQ count. A test fails
on any negative stock, duplicate activity/user, duplicate source order, allocation
above stock, missing accepted request after recovery, or unexplained drift.

Tests use synchronization barriers to overlap operations and run repeatedly. A
single green non-overlapping run is not accepted as concurrency evidence. Race
detector coverage includes producer callback state, consumer shutdown, leases, and
frontend request cancellation where applicable.

## Failure Matrix

| Failure point | Required behavior |
|---|---|
| half-message send fails before Lua | no Redis decrement; 503 |
| Lua business rejection | transaction rollback; mapped 409; no consumer event |
| Lua dependency failure | transaction rollback/unknown per proven state; fail closed |
| Lua accepts, commit response is lost | checker or stale-pending recovery commits or republishes same request |
| duplicate MQ delivery | durable reservation and order replay; ACK after success |
| PostgreSQL unavailable | MQ retry; Redis pending remains observable |
| durable final stock rejects | terminal request and compensation; no order |
| process dies after order commit | redelivery finds source order and completes reservation |
| Redis unavailable on expiry release | durable release job remains pending and retries |
| poison message exhausts retries | visible DLQ and alert; never silently acknowledged as success |

## Not Applicable

- Agent fake-model/eval and real-model smoke are not applicable: this slice does
  not change Router, Research Agent, Answer Node, tools, prompts, or model calls.
- Real payment provider testing is not applicable: payment remains the explicit
  sandbox adapter and must not be represented as a financial integration.
- Kubernetes and multi-region failover are not acceptance gates for the approved
  single-deployment modular monolith.

## Exit Criteria

All PRE_MERGE cases execute and pass; migration 006 is proven on clean and upgraded
databases; Redis and RocketMQ live integration is not replaced by mocks; full CI
passes; the final diff maps to AC1-AC12; skipped checks have explicit accepted
risk. ROLLOUT remains separately gated by target credentials, external writes,
and fault/load side effects. No performance or production-readiness claim is made
until V24-V26 are recorded.
