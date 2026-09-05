# Redis + RocketMQ 秒杀订单 Spec

- Status: APPROVED (2026-09-03)
- Owner: red060324
- Source: 2026-09-02 user request to align the implemented product with the
  resume's Redis Lua and asynchronous MQ purchase claims
- Branch: codex/clean-architecture-refactor
- Mode: FULL

This is the authoritative spec for the flash-sale slice. It supersedes only the
Redis/queue exclusion and synchronous-only commerce assumption in
../20260831-mature-game-platform/. Ordinary orders, coupon claims, sandbox
payment, the Go modular monolith, and the read-only Assistant remain unchanged.
The independent LightRAG graph and Multi-Agent work is not part of this spec.

## Goal

Add a real, demonstrable limited-stock purchase path in which Redis executes one
Lua script to atomically validate the activity window, reserve stock, and enforce
one reservation per user; RocketMQ absorbs accepted traffic and creates orders
asynchronously; PostgreSQL remains the durable business record and final
correctness guard.

The implementation must support an honest resume statement: the system uses
Redis Lua atomic admission and RocketMQ asynchronous order creation, proves that
concurrent requests neither oversell nor create more than one order per user, and
documents the failure and recovery boundaries instead of claiming exactly-once
delivery.

## Decisions

1. Add a flashsale business module. A flash-sale activity binds one active
   game_edition to one region/currency, an immutable sale price, a fixed stock,
   a start/end window, and a payment deadline. Ordinary catalog purchase remains
   available and does not use Redis or RocketMQ.
2. Use Redis 7.4 and one checked-in Lua script for the admission critical
   section. Redis checks server time, active metadata, remaining stock, and the
   per-activity user marker before atomically decrementing stock and recording a
   request. Quantity is fixed at one.
3. Use Apache RocketMQ 5.3.2 and rocketmq-client-go/v2. The producer sends a
   transactional message; its local transaction executes the Lua admission. A
   transaction check resolves from the same Redis request marker. Provider DTOs
   stay inside the RocketMQ adapter.
4. Treat RocketMQ delivery as at-least-once. The consumer is idempotent by
   request_id; PostgreSQL unique constraints and a locked stock counter are the
   final guard against duplicate orders or a stale/mis-seeded Redis key. No
   exactly-once claim is made.
5. Run the RocketMQ consumer, transaction checker, recovery dispatcher, and
   expiry reaper as bounded background components in the existing Go executable.
   This adds infrastructure, not a Java application or a new business
   microservice.
6. Return 202 Accepted with a stable request ID. The browser polls an
   authenticated request-status API until an order is ready or the request
   reaches a terminal failure. It never treats queue acceptance as an order.
7. Keep existing sandbox payment only. Flash-sale orders expire after the
   activity-configured payment deadline; a durable release job restores Redis
   stock after the PostgreSQL state transition. Real payment remains excluded.
8. Make the feature opt-in with XLH_FLASH_SALE_ENABLED=false by default. When
   enabled, PostgreSQL, Redis, and RocketMQ are required readiness dependencies;
   admission fails closed if either external service is unavailable or Redis
   activity state is missing.
9. Restore local deployment assets for PostgreSQL/pgvector, Redis, RocketMQ
   NameServer, and RocketMQ Broker. The current Render free Blueprint remains a
   non-flash-sale demo unless managed Redis and RocketMQ endpoints are supplied.
10. Deliver graph-enhanced LightRAG as a separate reviewed spec after this slice.

## Scope

### In scope

- Admin create, inspect, activate, and cancel flash-sale activities. Activated
  price, edition, region/currency, stock, and time window are immutable.
- Public authenticated activity list/detail and a countdown-ready response.
- Authenticated Idempotency-Key reservation endpoint with quantity fixed to one.
- Atomic Redis Lua admission with same-key replay, different-key one-user
  rejection, stock exhaustion, activity-time checks, and bounded key retention.
- RocketMQ transactional publishing, broker transaction checks, consumer group,
  retry/DLQ policy, and an idempotent asynchronous order consumer.
- PostgreSQL flash-sale activities and reservations, additive order source and
  expiry fields, final stock guard, request status, payment compatibility,
  expiration, durable Redis release jobs, and stale-message recovery.
- Structured safe logs and counters for admission outcome, transaction state,
  publish/consume retry, lag age, order result, compensation, and stock drift.
- A user-facing flash-sale page that reserves, polls, links to the created order,
  and shows loading, exhausted, duplicate, failure, and expiry states.
- Local Docker Compose, CI integration services, deployment/configuration, backup,
  broker persistence, reconciliation, rollout, and rollback documentation.

### Non-goals

- No real-money payment, refund, tax/invoice, cart with multiple quantities,
  coupon stacking on flash-sale prices, seller marketplace, or inventory keys.
- No Kafka, Redis Streams, NATS, additional queue abstraction framework, Java
  application, Kubernetes requirement, or service split in this delivery.
- No claim of globally linearizable stock across an unavailable Redis cluster.
  Admission fails closed; recovery follows the reviewed reconciliation procedure.
- No assistant mutation tool, transactional Agent, or connection between the
  Assistant and flash-sale writes.
- No LightRAG graph, Planning Agent, Supervisor/Worker Multi-Agent, or Skills
  implementation in this spec. Those require separate approval and evals.
- No replacement of the existing PostgreSQL coupon locking or ordinary order
  path.

## Current-State Evidence

- Reusable: internal/order already owns server-side price snapshots,
  idempotent order creation, sandbox payment transitions, and entitlements.
- Reusable: internal/catalog exposes a narrow purchase-offer capability; auth
  supplies a trusted Principal; HTTP mutations enforce same-origin policy.
- Reusable: the migration runner serializes immutable additive migrations and
  the repository has PostgreSQL concurrency/integration tests.
- Migration debt: current order creation is synchronous and has no source or
  payment-expiry contract. There is no activity/request status API.
- Missing: the current tree contains no Redis dependency, Lua script, RocketMQ
  client, broker deployment, consumer, flash-sale schema, or async request UI.
- Historical evidence: commit 3bcb366 contained Redis 7.4 and RocketMQ 5.3.2
  Compose scaffolding, but only a Java chat-audit publisher and Redis search cache;
  it did not implement the requested purchase path and must not be restored as
  production behavior.
- Approved deviation: ../20260831-mature-game-platform/spec.md excluded Redis
  and queues. The user's explicit 2026-09-02 route-A choice authorizes drafting
  this replacement design, but production implementation still waits for review
  of this full spec set.
- Worktree note: unrelated completed-but-uncommitted remediation changes already
  exist and must be preserved. This feature must not rewrite or discard them.

## Acceptance Criteria

- AC1: An admin can create and activate a valid future/current flash sale for an
  active edition and price. Invalid windows, non-positive stock, price/currency
  mismatch, or mutation of activated commercial fields is rejected.
- AC2: One Redis Lua execution uses Redis server time to atomically validate the
  activity, reserve exactly one stock unit, and create one user/request marker.
  Under concurrent load it admits no more than configured stock and no more than
  one request per activity/user.
- AC3: Reusing the same activity/user/idempotency key returns the stable original
  request without another decrement or message. A different key for the same user
  is rejected as already reserved. Raw idempotency keys are not stored in Redis,
  PostgreSQL, MQ messages, or logs.
- AC4: An accepted request is published as a versioned RocketMQ transactional
  message. Redis rejection rolls the half message back; an uncertain producer
  outcome is resolved by a bounded transaction checker and recoverable pending
  marker. The HTTP response distinguishes rejection, accepted, and dependency
  uncertainty without claiming an order exists.
- AC5: Repeated, delayed, and concurrently delivered messages create at most one
  reservation and one purchase_order for a request and activity/user. The
  consumer acknowledges only after durable handling; transient failures retry and
  poison messages reach the configured DLQ/alert path.
- AC6: PostgreSQL locks the activity row and conditionally increments allocated
  stock before order creation. Even with stale or deliberately over-provisioned
  Redis stock, PostgreSQL never records more active allocations than total stock.
  Final-guard rejection schedules an idempotent Redis compensation.
- AC7: The request owner can poll queued, processing, order-ready, failed, and
  expired states and receives the created order reference only after it exists.
  Other users cannot inspect the request; admins can inspect safe operational
  state without seeing secrets.
- AC8: A pending flash-sale order that passes its database-time payment deadline
  becomes expired exactly once, cannot be paid, releases its durable allocation,
  and eventually restores Redis stock through an idempotent durable release job.
  A paid order is never released and retains the existing one-time entitlement.
- AC9: If Redis, RocketMQ, or its local activity metadata is unavailable, new
  flash-sale admission fails closed with the standard error envelope. Existing
  ordinary catalog, community, coupon, order-history, payment, and Assistant
  behavior remains compatible when the feature is disabled.
- AC10: Configuration validates endpoints, credentials, timeouts, consumer
  concurrency, retry/recovery limits, and key prefix without logging secrets.
  Readiness includes Redis and RocketMQ only when the feature is enabled.
- AC11: The browser exposes the flash-sale flow accessibly, prevents accidental
  duplicate submission while preserving idempotent retry, polls with cancellation
  and a bound, and renders accepted, exhausted, duplicate, failure, order-ready,
  and expired states.
- AC12: Focused unit, Lua/Redis integration, PostgreSQL integration, RocketMQ
  interface and live-broker integration, HTTP, race, frontend, migration, Docker,
  fault/recovery, concurrency, architecture, public-only, Java-absence, and full
  CI gates pass. Performance numbers appear in documentation only after a recorded
  run; no unexecuted load result is presented as evidence.

## Assumptions And Open Questions

1. RocketMQ is selected from the user's “Kafka or RocketMQ” choice because the
   repository previously carried RocketMQ 5.3.2 deployment assets. Approval of
   this spec approves RocketMQ rather than Kafka.
2. Each activity is one edition, one region/currency, one fixed sale price, and
   quantity one. Coupons do not apply to this price.
3. Default payment timeout is 15 minutes, consumer concurrency is 16, producer
   send timeout is 3 seconds, and recovery considers a Redis pending record stale
   after 30 seconds. All are positive bounded configuration.
4. “One person one order” means one accepted attempt for an activity. Business
   expiry does not allow that user to reserve again; technical rollback before a
   durable reservation removes the marker and permits retry.
5. Redis production deployment uses authentication, TLS where supported, AOF or
   managed persistence, and replication. RocketMQ uses persistent broker storage
   and production ACL/TLS where the selected provider supports them.
6. The first implementation runs producer and consumer in the same Go executable.
   Independent worker deployment is a follow-up only if measured load or failure
   isolation requires it.
7. The public-rollout hardening draft remains independently unapproved. Its
   process-local general API limits do not replace flash-sale atomic admission.

## Clarify Decisions

On 2026-09-02 the user selected route A, then selected: deliver the purchase
slice first; create a dedicated flash-sale activity entity; and use the Kafka or
RocketMQ family rather than Redis Streams or NATS. This draft concretizes that
choice as RocketMQ 5.3.2. On 2026-09-03 the user explicitly approved this spec,
plan, tasks, data model, HTTP contract, and test plan and authorized implementation.
