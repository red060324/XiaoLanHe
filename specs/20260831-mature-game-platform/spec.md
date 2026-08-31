# 小蓝盒成熟游戏平台 Spec

- Status: `APPROVED` (2026-08-31)
- Owner: red060324
- Source: current Codex goal and prior architecture discussion
- Branch: `codex/clean-architecture-refactor`
- Mode: `FULL`

This is the authoritative product-delivery spec. The earlier clean-architecture
migration remains historical evidence. The Research Agent draft is retained as
phase-four design input and is not independently active.

## Goal

Build one deployable Go modular monolith that combines:

1. a game catalog and purchase flow;
2. a game community;
3. atomic coupon claims and order discounts;
4. a read-only game assistant that can research knowledge, catalog, and forum
   content;
5. the identity, security, verification, observability, and deployment baseline
   needed for these capabilities to work as one credible product.

The product should be runnable locally and on an open-source-friendly hosting
stack without Java or private organization infrastructure.

## Decisions

1. Keep a Go modular monolith and one PostgreSQL database. Split services only
   from measured scaling, availability, isolation, or ownership needs.
2. Use module-first Clean Architecture. Each module owns Entry/Presenter,
   UseCase, Entity, and Repository adapter code as needed.
3. Deliver vertical slices in dependency order: foundation/account/catalog,
   community, promotion/commerce, assistant integration, then operational
   hardening.
4. Game purchase uses a sandbox payment adapter in this repository. No real
   card or wallet charge is made until a separate provider/security spec exists.
5. Coupon claim and order state changes are ordinary deterministic UseCases.
   The assistant remains read-only in this delivery.
6. Router and Answer are bounded Nodes. Research is the only autonomous Agent,
   with read-only `search_knowledge`, `search_catalog`, `search_forum`, and
   optional `search_web` tools when their backing modules exist.
7. Replace startup execution of one mutable schema file with ordered,
   version-recorded migrations before adding commerce tables.

## In Scope

### Foundation and account

- Register, login, logout, and current-user APIs.
- Password hashing and opaque, hashed, expiring database sessions in secure
  HttpOnly cookies.
- User/admin roles, request identity, authorization middleware, request IDs,
  graceful shutdown, liveness/readiness, and versioned migrations.
- Remove dead diagnostic configuration and distinguish dependency failure from
  successful empty results.

### Catalog

- Browse/search games, game detail, editions, regional prices, and ownership.
- Admin-only catalog writes and deterministic seed data for local/demo use.
- Cursor pagination and stable public response contracts.

### Community

- Create/list/read/update/delete posts owned by authenticated users.
- Comments and one reaction per user/post/reaction type.
- Game-scoped feeds, cursor pagination, soft deletion, and basic moderation
  status. No realtime chat or recommendation feed.

### Promotion and commerce

- Coupon campaigns with time window, eligibility, per-user limit, total stock,
  minimum spend, and fixed/percentage discount rules.
- Atomic idempotent claim under concurrency.
- Create an order from a current edition price snapshot, apply one eligible
  coupon, and compute non-negative totals deterministically.
- Sandbox payment confirmation, order state machine, coupon redemption, and a
  game entitlement issued exactly once.
- Order history and detail visible only to the owner or an admin.

### Assistant

- Preserve REST/SSE chat compatibility.
- Replace the fixed research pipeline with a bounded read-only Research Agent.
- Search knowledge, catalog, and forum content; Web Search remains optional.
- Citations, explicit no-result/partial/all-failed/cancelled semantics, budgets,
  trace fields, and deterministic fake-model/tool tests.

### Frontend and operations

- Responsive navigation for Discover, Community, Deals, Assistant, Orders, and
  Account.
- Authentication state, accessible forms, loading/error/empty states, and
  cancellable chat streaming.
- Local Docker deployment, GitHub Actions, Render-compatible deployment,
  documented environment configuration, backup/rollback expectations, and
  smoke-test evidence.

## Non-goals

- No real-money payment provider, refund settlement, tax/invoice system, seller
  marketplace, inventory keys, realtime messaging, social graph, or mobile app.
- No transactional Agent tools, Multi-Agent system, MCP marketplace, background
  autonomous Agent, recommendation model, Redis, queue, or microservice split.
- No Kubernetes requirement. No fake claim that a sandbox payment is a real
  commercial payment integration.

## Current-State Evidence

See `research.md`. Today only chat, knowledge search/ingestion, optional Web
Search, PostgreSQL persistence, and a local-only chat UI exist. Account tables
are unused; catalog, community, coupons, orders, entitlements, and functional
login UI do not exist.

## Acceptance Criteria

### AC1 Architecture and language

All backend production behavior is Go. Module-first Clean Architecture checks
pass; public Entity/UseCase contracts do not expose Hertz, Eino, pgx, SQL, or
provider DTOs.

### AC2 Identity and authorization

A user can register, login, logout, and fetch their profile. Passwords and
session tokens are never stored in plaintext. Anonymous, user, owner, and admin
permissions are enforced at HTTP and UseCase boundaries. Knowledge and catalog
writes are no longer anonymous.

### AC3 Catalog

Users can search/browse games and view editions/prices with stable pagination.
Admins can create/update catalog entries. Purchased editions appear as owned.

### AC4 Community

Authenticated users can manage their own posts/comments and react once per
type. Other users cannot mutate them. Deleted/hidden content is absent from
public feeds, and pagination is stable under new inserts.

### AC5 Coupon claim

Claim validates campaign state, time, eligibility, per-user limit, and stock in
one transaction. Concurrent requests cannot oversell or create duplicate
claims. Repeating the same idempotency key returns the original result.

### AC6 Order and entitlement

Order totals use a server-side price snapshot and deterministic discount rule.
State transitions reject invalid moves. One successful sandbox payment redeems
the coupon and grants one entitlement exactly once; replay is idempotent.

### AC7 Assistant

Router and Answer remain Nodes; Research is the sole autonomous Agent. The
Agent can iteratively use only allowlisted read-only tools, observes typed tool
results, stays within configured budgets, preserves citations, and cannot claim
coupons, create/pay orders, or mutate community data.
Bounded prior conversation context reaches Router and Answer on every route.

### AC8 Failure and cancellation semantics

Provider timeout/non-2xx/malformed response is not represented as successful
empty evidence. Cancellation reaches HTTP, Agent, database, and outbound calls.
Frontend chat can abort a request and never leaves an unexplained empty reply.

### AC9 Data and migrations

Ordered migrations are recorded and safe on repeated/multi-instance startup.
Foreign keys, uniqueness, checks, and indexes protect identity, content,
promotion, order, payment, and entitlement invariants. No destructive migration
is applied without an explicit migration/rollback plan.

### AC10 Verification and delivery

Every phase has requirement-linked unit, integration, interface, race, frontend,
static, and smoke evidence where applicable. `make ci` passes, no Java exists,
and skipped external checks are reported rather than treated as success.

## Assumptions Requiring Review

1. Registration uses unique username + password; email verification and account
   recovery are deferred.
2. A database session cookie lasts seven days, is rotated at login, and stores
   only a random opaque token client-side; the database stores its SHA-256 hash.
3. Catalog prices use integer minor units plus ISO currency code. The first
   release exposes one active price per edition/region/currency.
4. Sandbox payment is sufficient for this portfolio/demo delivery; a real
   payment provider is explicitly excluded.
5. Coupon percentage is stored in basis points; fixed discounts use minor units.
6. Public catalog/community reads and guest assistant chat remain allowed.
   Writes, coupons, orders, ownership, and history require login.
7. Phase order is foundation/account/catalog, community, promotion/commerce,
   Research Agent integration, and deployment hardening.

## Clarify Decisions

The user approved the seven assumptions, phase ordering, plan, data model, and
test plan on 2026-08-31. On 2026-09-01, the user confirmed that real payment,
refund, tax, and transactional Agent tools are permanent project non-goals,
not follow-up backlog.
