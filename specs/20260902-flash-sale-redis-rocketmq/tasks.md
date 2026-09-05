# Tasks

- Status: `IMPLEMENTED — EXTERNAL PRE_MERGE GATES BLOCKED` (2026-09-04)

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | DONE | Human approves RocketMQ selection, transactional-message design, activity/order semantics, API, data model, deployment, and test plan | all | explicit approval on 2026-09-03 |
| T1 | PRE_MERGE | DONE | Add failing Entity/UseCase/Presenter contract tests for activity lifecycle, stable idempotency, async statuses, final stock, order source, and expiry | AC1, AC3, AC5-AC8 | flashsale entity/usecase/presenter, worker, HTTP, order, Redis and RocketMQ test suites pass in `make ci`; no durable pre-implementation red transcript was retained |
| T2 | PRE_MERGE | DONE | Add migration 006, embedded migration registration, constraints/indexes, and clean/upgrade PostgreSQL integration tests | AC1, AC5, AC6, AC8 | PostgreSQL 17 + pgvector fresh/upgrade/repeat/concurrent integration passed in ordinary and race suites on 2026-09-04 |
| T3 | PRE_MERGE | DONE | Implement flashsale Entity and UseCases with consumer-owned Redis, publisher, store, order, and clock contracts | AC1-AC10 | affected Go suites and `.hooks/check-architecture.sh` pass |
| T4 | PRE_MERGE | BLOCKED | Implement checked-in admission and compare/release Lua scripts plus Redis adapter, script reload, TTL, cluster hash tag, and live Redis concurrency tests | AC2, AC3, AC6, AC8 | scripts/adapter and live tests are implemented; live cases skipped because `XLH_TEST_REDIS_URL` is unset |
| T5 | PRE_MERGE | BLOCKED | Implement RocketMQ transactional producer/listener/checker and bounded at-least-once consumer behind provider-neutral ports | AC4, AC5, AC10 | codec/listener/transport unit tests pass; live 5.3.2 transaction test skipped because broker variables are unset |
| T6 | PRE_MERGE | PARTIAL | Implement durable reservation/final-stock saga and idempotent Order.CreateFromFlashSale, preserving ordinary/coupon/sandbox-payment behavior | AC5, AC6, AC9 | live PostgreSQL duplicate replay, concurrent final guard and expiry pass; combined live Redis/MQ chain remains blocked |
| T7 | PRE_MERGE | DONE | Implement expiration reaper, release-job worker, stale-pending recovery dispatcher, bounded leases/retries, and safe shutdown | AC4, AC5, AC8, AC10 | worker retry/lease/compensation/shutdown tests and full `go test -race` pass |
| T8 | PRE_MERGE | DONE | Implement public/admin HTTP contracts, authorization, same-origin checks, error mapping, request ownership, and bounded status polling API | AC1, AC3, AC7, AC9 | presenter and public/admin Hertz wire tests pass |
| T9 | PRE_MERGE | DONE | Add validated opt-in config, composition, lifecycle, enabled-only readiness, structured safe telemetry and protected low-cardinality Prometheus metrics | AC9, AC10 | config/readiness/startup, log redaction, metrics vocabulary/cardinality and race tests pass; Redis requires URL authentication, RocketMQ readiness verifies an authenticated publish route with a bounded probe, and flash sale requires a 32-512 character metrics token |
| T10 | PRE_MERGE | DONE | Add accessible frontend flash-sale activity/reservation/poll/order flow with cancellation and stale-result protection | AC7, AC11 | focused Commerce/API Vitest passes within 80/80 frontend tests; production build passes, with the heavy Assistant renderer removed from the initial entry |
| T11 | PRE_MERGE | BLOCKED | Add local Compose for pgvector, Redis 7.4, RocketMQ 5.3.2, persistent volumes, deterministic seed/smoke, and update architecture/deployment/env docs | AC9, AC10, AC12 | assets/docs are present; `make middleware-config` and `make docker-build` fail because Docker is unavailable, so health/restart/smoke were not run |
| T12 | PRE_MERGE | BLOCKED | Run focused, full race, frontend, architecture, spec-drift, public-only, Java-absence, migration, container, and full make ci gates; complete report/readiness mapping | AC12 | latest full `make ci` with PostgreSQL 17 + pgvector and 80 Vitest cases exits 0; live Redis/MQ, container and current GitHub Actions gates remain unexecuted |
| T13 | ROLLOUT | TODO | Provision an approved isolated persistent Redis/RocketMQ environment, create topic/groups/DLQ, apply migration disabled, then enable one small activity | AC9, AC10, AC12 | target/config review, readiness and ordinary-flow smoke |
| T14 | ROLLOUT | TODO | Run approved concurrent and fault-injection smoke: stock+1 users, same-user storm, duplicate delivery, process restart, Redis/MQ interruption, expiry/release, DLQ inspection | AC2-AC9, AC12 | versioned report with actual counts, latency, errors, and reconciliation |
| T15 | ROLLOUT | TODO | Observe pending age, retries, DLQ, stock drift, release backlog, and rollback/drain behavior before public navigation | AC5, AC8-AC12 | observation window and rollback evidence |
| T16 | FOLLOW_UP | DONE | Keep Assistant knowledge independent from flash sale and hand it to its own reviewed architecture | excluded | superseded by the approved 20260904 advanced-AI spec using official HKUDS LightRAG native storage |
| T17 | FOLLOW_UP | TODO | Split MQ worker into an independent Go deployment only if measured scaling/isolation/SLO evidence requires it | excluded | trigger: consumer saturation or failure-isolation evidence |
| T18 | FOLLOW_UP | TODO | Evaluate Redis Cluster or managed RocketMQ HA and edge admission after a real multi-instance production target is selected | excluded | trigger: approved horizontal scaling topology |

Statuses: TODO, IN_PROGRESS, DONE, or BLOCKED. Every incomplete PRE_MERGE row
blocks READY. T0 blocks all production-code work. Deployment, shared-environment
writes, and cost-bearing infrastructure require separate rollout approval even
after PRE_MERGE implementation.
