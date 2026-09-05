# Data Model

- Status: APPROVED (2026-09-03)
- Migration: planned 006_flash_sale.sql

All business timestamps use timestamptz. Money remains integer minor units with
a three-character uppercase currency. Migration 006 is additive and is exercised
against both a clean database and a database containing migrations 001-005 data.

## flash_sale_activity

| Column | Contract |
|---|---|
| id | bigserial primary key |
| code | stable admin-readable code, case-insensitive unique |
| edition_id | required FK to game_edition |
| region_code | 2-16 uppercase letters/digits/hyphen |
| currency | three uppercase letters |
| sale_price_minor | non-negative and no greater than activation-time catalog price |
| total_stock | positive bigint |
| allocated_stock | bigint, 0 <= allocated_stock <= total_stock |
| status | draft, active, cancelled, or ended |
| starts_at, ends_at | non-empty interval |
| payment_timeout_seconds | bounded positive integer, default 900 |
| version | positive integer copied into Redis metadata |
| created_by | admin FK to user_account |
| timestamps | created_at, updated_at, optional activated_at and cancelled_at |

Indexes support public active-window listing by status, time, and ID plus edition
history. Activation serializes on the edition, region, and currency and rejects an
overlap with another active activity. Commercial fields and version cannot change
after activation through the UseCase.

## flash_sale_reservation

| Column | Contract |
|---|---|
| request_id | stable `fsr_<activity-base36>_<32hex>` public identifier, primary key |
| activity_id | FK to flash_sale_activity |
| user_id | FK to user_account |
| idempotency_digest | fixed-length SHA-256 bytes, never raw key |
| status | reserved, order_ready, failed, or expired |
| order_id | nullable FK to purchase_order, unique when set |
| failure_code | nullable bounded internal enum, no provider text |
| reserved_at | Redis event time validated again on consume |
| payment_expires_at | durable database-time deadline |
| timestamps | created_at, updated_at |

Constraints:

- unique (activity_id,user_id) enforces one person per activity durably;
- unique (activity_id,user_id,idempotency_digest) supports exact replay lookup;
- order_id is required only for order_ready;
- terminal states cannot transition back to reserved;
- status transitions occur through locked, idempotent repository operations.

## flash_sale_release_job

| Column | Contract |
|---|---|
| id | bigserial primary key |
| request_id | unique FK to reservation |
| activity_id, user_id | copied validated identifiers for bounded worker lookup |
| idempotency_digest, reserved_at | copied request identity for compare-and-release, including pre-durable rollback |
| reason | technical_rollback, final_guard, payment_expired, or admin_repair |
| status | pending, leased, or done |
| attempts | non-negative bounded counter |
| next_attempt_at, lease_until | retry and lease timestamps |
| last_error_code | safe bounded code, never provider message or URL |
| timestamps | created_at, updated_at, optional completed_at |

Workers claim due rows with FOR UPDATE SKIP LOCKED. `request_id` is not an FK
because a final-guard/technical rollback may need Redis compensation before a
durable reservation can legally be inserted. Unique request_id makes job
creation and repeated expiry/failure handling idempotent. Exponential retry is
bounded; exhausted jobs remain inspectable and alertable rather than being deleted.
Completed jobs may be pruned after 30 days by an explicit maintenance command.

## purchase_order Additions

| Column | Contract |
|---|---|
| source_type | non-null standard or flash_sale, default/backfill standard |
| source_reference | nullable flash request ID |
| payment_expires_at | nullable deadline; required for flash-sale pending orders |

A partial unique index on (source_type,source_reference) where the reference is
not null makes order creation idempotent. Existing ordinary orders retain null
source reference/deadline and their API shape remains compatible.

## Durable Allocation Saga

For a new MQ request, the FlashSale PostgreSQL adapter:

1. locks the activity row;
2. verifies active version, event time, and allocated_stock < total_stock;
3. resolves the unique activity/user or request replay;
4. inserts reserved and increments allocated_stock; and
5. commits before Order creation is attempted.

An event is eligible when its validated Redis reserved_at is inside the original
activity window and, for a cancelled activity, no later than cancelled_at. This
lets cancellation stop new intake without discarding already accepted requests.

If order creation transiently fails, the durable reservation remains and MQ
redelivery retries it. If it permanently fails, a second idempotent transaction
marks it failed, decrements allocation, and inserts a release job. If Order commits
but reservation marking fails, redelivery resolves the unique source order and
completes the mark. This is an idempotent saga, not a distributed transaction or
an exactly-once claim.

## Redis Representation

Redis is disposable acceleration state but must use persistence and replication
in production. Activity keys share one cluster hash tag. Values contain only
bounded IDs, version, status, epoch milliseconds, integer stock, and digests. The
request hash plus pending sorted set supports transaction checks and stale
republishing.

The activation path calculates remaining Redis stock from total_stock minus
allocated_stock; it never blindly restores total stock over a live activity.
Missing keys fail closed. A repair procedure pauses admission, drains or accounts
for broker backlog, compares PostgreSQL allocations, and only then primes the new
Redis version.

## Retention And Backup

- Activities, reservations, and linked orders are retained with commerce records;
  deletion requires a later legal/product retention decision.
- PostgreSQL backups are the durable recovery source.
- Redis AOF/snapshots reduce admission-state loss but do not replace PostgreSQL.
- RocketMQ broker store and consume offsets use persistent volumes or managed
  retention longer than the maximum activity plus recovery window.
- Backup/restore smoke proves migration, order/reservation links, and that Redis
  is rebuilt only through the fail-closed reconciliation procedure.
