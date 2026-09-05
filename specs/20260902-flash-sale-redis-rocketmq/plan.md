# Technical Plan

- Status: APPROVED (2026-09-03)
- Authoritative spec: ./spec.md

## Selected Architecture

Keep one Go modular monolith and add one flashsale module plus Redis and
RocketMQ adapters. PostgreSQL remains the durable source of business truth.
Redis owns fast admission and pending-delivery markers; RocketMQ owns durable
asynchronous transport, not business state.

~~~text
POST reservation
  -> FlashSale Entry / Presenter / Reserve UseCase
  -> RocketMQ transactional producer sends half message
       -> local transaction: Redis EVALSHA reserve.lua
            time + state + stock + one-user + idempotency
       -> COMMIT / ROLLBACK / broker transaction check
  <- 202 requestId (order does not exist yet)

RocketMQ consumer (at-least-once)
  -> FlashSale Fulfil UseCase
       -> lock PostgreSQL activity + final stock allocation
       -> Order.CreateFromFlashSale (idempotent public capability)
       -> mark reservation order_ready
  -> ACK only after durable success

GET request status
  -> PostgreSQL reservation/order when durable
  -> Redis pending marker only while the event has not reached PostgreSQL

Expiry reaper
  -> lock expired pending order with SKIP LOCKED
  -> expire order + release PostgreSQL allocation + enqueue release job
  -> Redis compensation worker executes compare-and-release Lua
~~~

## Module And Dependency Ownership

~~~text
internal/flashsale/entity/                 activity/request invariants
internal/flashsale/usecase/                reserve, fulfil, status, admin, expiry
internal/flashsale/entry/                  HTTP and background lifecycle entry
internal/flashsale/presenter/              request/response mapping
internal/flashsale/repository/postgres/    activity, reservation, release job
internal/flashsale/repository/redis/       Lua execution and Redis state
internal/flashsale/repository/rocketmq/    producer/consumer provider boundary
internal/order/usecase/                    narrow CreateFromFlashSale capability
internal/order/repository/postgres/        idempotent sourced order write
cmd/xiaolanhe/                             concrete composition and lifecycle
~~~

Compile-time dependencies point toward entity and consumer-owned UseCase ports.
Redis and RocketMQ types do not enter Entity, UseCase public contracts, HTTP
responses, or the Order module. flashsale may call the Order module's narrow
public application capability; it never reads Order repository tables directly.

## Admission And Transaction Semantics

The server derives a stable request ID and SHA-256 idempotency digest from the
validated activity ID, trusted user ID, and raw Idempotency-Key. The raw key is
discarded before repository calls. The transaction message contains only a
version, request ID, activity ID, trusted user ID, SHA-256 idempotency digest, and
reservation timestamp. The digest is safe for equality checks but cannot recover
the raw client key.

The Lua script receives namespaced keys and bounded scalar arguments. It calls
Redis TIME; client time cannot open or extend an activity. In one evaluation it:

1. validates the activity metadata version and active marker;
2. validates starts_at <= redis_time < ends_at;
3. checks the existing user marker;
4. returns replay for the same digest/request, or one-user conflict otherwise;
5. checks and decrements integer remaining stock;
6. writes the user marker and request detail; and
7. adds the request to a time-ordered pending-delivery set and applies an
   end-of-activity plus recovery-grace expiry to every key.

The adapter maps integer script outcomes to domain results. It does not parse
English Redis errors as business decisions. A checked-in SHA is loaded with
SCRIPT LOAD; NOSCRIPT triggers one bounded reload and retry. Other Redis errors
are dependency failures.

For a new accepted reservation the RocketMQ local transaction returns COMMIT.
Business rejection returns ROLLBACK. If the producer loses the commit response,
the broker transaction checker reads the request marker and returns COMMIT only
for an exact request/activity/user match, ROLLBACK for a proven absent/rejected
request, and UNKNOWN for dependency uncertainty. Checks have bounded timeouts and
never invent acceptance. Same-key HTTP replay does not decrement again; duplicate
messages remain safe at the consumer.

## Consumer, Retry, And Backpressure

- Subscribe one fixed consumer group to one versioned topic/tag. Reject unknown
  versions and invalid bounded payloads as poison messages with safe logging.
- Cap handler concurrency and database time. Do not start unbounded goroutines.
- Persist/resolve the flash-sale reservation first under an activity row lock. A
  conditional allocation protects allocated_stock <= total_stock.
- Invoke Order.CreateFromFlashSale with a typed immutable price snapshot loaded
  from the active activity. The Order repository uses unique source reference and
  current ownership checks, so redelivery returns the original order.
- Mark the reservation order_ready after the Order capability succeeds. A crash
  between those operations redelivers: allocation and order both replay, then the
  status transition completes.
- Return retry for timeout, cancellation, PostgreSQL unavailability, or other
  transient errors. Permanent validation/final-stock/ownership failures mark a
  terminal status, enqueue Redis compensation where appropriate, and ACK.
- Configure broker retries and DLQ. A DLQ event is not silently consumed; safe
  logs and a pending-age signal identify the request for manual replay.
- A recovery dispatcher scans only bounded stale entries from the Redis pending
  set. It republishes their versioned message with the same request ID, using a
  short lease so concurrent app instances cannot create an unbounded storm.
  Consumer idempotency makes duplicate recovery publications harmless.

## Activity Lifecycle

An admin creates a draft activity from a current active catalog price and may
edit it while draft. Activation validates database time and then performs a
fail-closed publish:

1. write Redis metadata and stock with admission disabled;
2. mark the PostgreSQL activity active;
3. atomically enable the Redis marker.

Failure after step 2 leaves PostgreSQL active but Redis disabled, so reservation
returns dependency-unavailable rather than bypassing stock control. Retrying
activation repairs the same version. Cancellation first atomically closes Redis
admission and records its Redis-time cutoff, then stores that cutoff and cancelled
state in PostgreSQL. If the durable write fails, the activity remains unavailable
and retry repairs it; there is no fail-open intake window. Every reservation also
validates the durable activity version before transactional send. The consumer
still fulfils an accepted message whose Redis reservation time is no later than
the recorded cancellation cutoff, so cancellation stops new intake without
revoking accepted work. Activated commercial fields are immutable.

## Order And Expiry Semantics

Add an order source (standard or flash_sale), unique source reference, and
nullable payment deadline. Ordinary order behavior is unchanged. Flash-sale
orders use the activity price and exclude coupons. Existing sandbox payment locks
the order and compares PostgreSQL statement_timestamp() with payment_expires_at;
it cannot pay an expired order.

A bounded reaper selects overdue pending_payment flash-sale orders using
FOR UPDATE SKIP LOCKED. In one database transaction it marks the order expired,
marks/releases the reservation, decrements activity allocation, and inserts a
unique pending Redis release job. A worker executes a compare-and-release Lua
script: it increments Redis stock only if the marker still belongs to that exact
request and has not already been released. It then marks the job done. Retries are
safe; a paid order never matches the reaper.

Business expiry retains the one-user marker through the activity so the user
cannot cycle reservations. Technical rollback before PostgreSQL has accepted the
reservation removes the marker and restores stock.

## Storage And Migration

See data-model.md. Migration 006 is additive. It creates activity, reservation,
and release-job tables and adds nullable/defaulted source and expiry columns to
orders. Existing rows backfill to standard; existing endpoints and indexes stay
compatible. Migrations remain immutable after merge.

Redis keys include an environment-specific validated prefix and hash tag so a
future Redis Cluster evaluates all script keys in one slot:

~~~text
<prefix>:fs:{<activity-id>}:meta
<prefix>:fs:{<activity-id>}:stock
<prefix>:fs:{<activity-id>}:buyers
<prefix>:fs:{<activity-id>}:requests
<prefix>:fs:{<activity-id>}:pending
~~~

No raw username, idempotency key, cookie, credential, or request body is a key or
value. Key TTL is activity end plus a bounded recovery grace. PostgreSQL records
are retained with orders; completed release jobs may be pruned after 30 days.

## HTTP And Serialization Boundaries

Exact contracts are in contracts/flash-sale-http.md. Entry owns authentication,
origin enforcement, bounded decoding, and cancellation. Presenter owns string ID,
timestamp, enum, and error-envelope mapping. The UseCase accepts trusted numeric
user identity and provider-neutral commands/results.

The reservation endpoint returns 202 for accepted/queued work and never embeds a
fabricated order. Status polling is capped client-side and cancellable. A stable
request remains queryable after the Redis pending marker is removed because the
PostgreSQL reservation becomes authoritative.

## Configuration And Required Dependencies

When XLH_FLASH_SALE_ENABLED=true, validate at startup:

- XLH_REDIS_URL, as a `redis://` or `rediss://` URL with an explicit non-empty
  password supplied through the provider secret store; fragments and CR/LF are
  rejected;
- XLH_REDIS_KEY_PREFIX with a conservative character/length bound;
- XLH_ROCKETMQ_NAMESERVERS, optional access/secret keys, topic, producer group,
  consumer group, send/consume timeouts, consumer concurrency, and retry limit;
- recovery batch/lease/stale durations and expiry-reaper interval/batch size.

No credential appears in .env.example, logs, readiness responses, or frontend
assets. When disabled, clients are not constructed, background components do not
start, routes return a documented unavailable response or are hidden from
navigation, and current /readyz semantics remain PostgreSQL-only. When enabled,
readiness checks PostgreSQL, Redis PING, and a bounded authenticated RocketMQ admin
lookup for the configured topic with at least one publishable queue. One in-flight
gate bounds admin-client creation under concurrent probes. Missing credentials, an
unreachable dependency, an inaccessible or missing topic, or an empty publish route
makes the process unready.

## Security And Trust Boundaries

- User identity comes only from authentication middleware, never JSON, headers,
  Redis values, or MQ message fields supplied by a client. The producer constructs
  the internal event after authentication.
- Admin checks exist at both Entry and UseCase boundaries. Activity price is
  server-resolved; clients cannot submit quantity, order status, or trusted user.
- Lua uses only KEYS supplied by the adapter and fixed commands. IDs and digests
  are validated bounded scalars; no dynamic Lua source is built.
- MQ payload size/version and every field are validated before database work.
- Redis and RocketMQ are private dependencies. Production docs forbid their
  unauthenticated public exposure.
- The Assistant remains read-only and receives no FlashSale mutation capability.

## Observability

Structured logs carry request ID, flash request ID, activity ID, operation, safe
outcome, duration, transaction state, retry count, and consumer lag bucket. They
omit raw idempotency keys, Redis URLs, MQ credentials, cookies, and payload bodies.

Record counters for Lua outcome, producer commit/rollback/unknown, consume
success/retry/retry-exhausted, durable final-guard rejection, release-job retry,
and stale pending age. These are emitted through the protected process-local
Prometheus registry with fixed labels; request/user/activity IDs remain log-only.
Broker DLQ depth and database/Redis reconciliation drift require external probes
because neither can be truthfully derived from a single in-process event. Alertable
conditions are growing pending age,
non-empty DLQ, repeated transaction UNKNOWN, release backlog, and any stock drift.

## Rollout And Rollback

1. Apply the additive migration with the feature disabled. Deploy the compatible
   app and verify ordinary product smoke.
2. Provision private persistent Redis and RocketMQ, create topic/groups/DLQ, and
   verify authentication, persistence, retention, and network access.
3. Enable the feature in an isolated environment, create one small activity, run
   concurrent and fault-injection smoke, inspect pending/DLQ/release signals, then
   enable public navigation.
4. To stop intake, cancel activities or disable the feature. Continue consumers
   until queued requests reach terminal status. Do not delete Redis keys, broker
   storage, or database rows during rollback.
5. Roll back the application revision after consumers are drained. Additive tables
   and columns remain. Resume only after Redis is reconciled from PostgreSQL and
   broker backlog has been inspected.

## Rejected Alternatives

- Kafka: valid but not selected; RocketMQ matches the user's choice and the
  repository's historical deployment direction, and supports transaction checks.
- Redis Streams as MQ: fewer services, but the user explicitly selected the
  Kafka/RocketMQ option and wants a distinct asynchronous MQ chain.
- Plain publish after Lua: creates an untracked crash window between reservation
  and message publication. Transaction messages plus a pending recovery ledger
  make that uncertainty observable and recoverable.
- PostgreSQL-only locking: correct for the current coupon flow but does not
  implement the requested Redis Lua admission or queue buffering. It remains the
  final durable guard.
- Distributed lock around read/decrement/write: slower and less precise than one
  atomic Lua evaluation; lock expiry introduces extra correctness states.
- Separate worker service now: independent deployment adds topology before a
  measured need. The consumer remains a bounded component in the monolith.
- Assistant/knowledge architecture is independent from this delivery. The later
  approved advanced-AI spec uses official HKUDS LightRAG with native stores; no
  LightRAG-like graph implementation belongs in the flash-sale module.

## Traceability

| Planned change | Owner/module | Requirement |
|---|---|---|
| activity invariants/admin lifecycle | flashsale | AC1 |
| atomic admission Lua and Redis adapter | flashsale/repository/redis | AC2, AC3 |
| transactional producer/checker | flashsale/repository/rocketmq | AC4 |
| idempotent consumer and order capability | flashsale + order | AC5, AC6 |
| request/status HTTP contracts | flashsale Entry/Presenter | AC3, AC7 |
| order expiry and release jobs | order + flashsale | AC8 |
| optional wiring and readiness | config + cmd | AC9, AC10 |
| accessible asynchronous UI | frontend | AC11 |
| Compose, CI, docs, fault/load proof | repository harness | AC12 |
