# Technical Plan

- Status: `APPROVED` (2026-08-31)
- Authoritative spec: `./spec.md`

## Selected Architecture

Use a module-first Clean Architecture modular monolith:

```text
HTTP/SSE
  -> account | catalog | community | promotion | order | assistant
  -> each module's Presenter -> UseCase -> Entity
  -> consumer-owned ports -> PostgreSQL/model/search/payment adapters
```

The executable composition root constructs repositories and UseCases. Modules
may call another module's public application capability, never its repository.
No event bus is added until a real asynchronous requirement exists.

## Target Packages

Create only packages needed by the current vertical slice:

```text
cmd/xiaolanhe/
internal/app/
internal/platform/http/
internal/platform/postgres/
internal/account/{presenter,usecase,entity,repository/postgres}/
internal/catalog/{presenter,usecase,entity,repository/postgres}/
internal/community/{presenter,usecase,entity,repository/postgres}/
internal/promotion/{presenter,usecase,entity,repository/postgres}/
internal/order/{presenter,usecase,entity,repository/postgres,payment}/
internal/assistant/{presenter,usecase,entity,agent/research,repository/...}/
```

The current horizontal packages migrate incrementally. There is no directory
shuffle without accompanying behavior or boundary tests.

## Phase 1: Foundation, Account, Catalog

### Runtime flow

```text
register/login -> Account UseCase -> credential/session repositories
request cookie -> Auth middleware -> trusted Principal
catalog request -> Catalog Presenter -> Catalog UseCase -> Catalog repository
admin catalog write -> Principal role check -> Catalog UseCase
```

### Contracts

Exact phase-one request, response, cookie, error, and pagination contracts are
defined in `contracts/phase1-http.md`.

- `POST /api/auth/register`
- `POST /api/auth/login`
- `POST /api/auth/logout`
- `GET /api/me`
- `GET /api/games?query=&cursor=&limit=`
- `GET /api/games/{slug}`
- `POST /api/admin/games`
- `PUT /api/admin/games/{id}`

Registration/login return public account data and set/rotate an HttpOnly cookie.
Authentication failures use one generic message. Public account responses never
include credential/session fields.

### Foundation fixes included

- version table + ordered migration runner + advisory lock;
- request ID middleware, readiness, graceful shutdown;
- admin authorization for knowledge writes;
- typed Web Search failures;
- remove dead agentMode/minio configuration;
- frontend authentication and catalog navigation.

## Phase 2: Community

Create posts, comments, and reactions scoped to optional game IDs. Use keyset
pagination `(created_at,id)`, ownership checks in UseCases, soft deletion, and
admin hidden status. Public feeds exclude non-published rows. Cross-module game
validation uses a narrow Catalog capability. Exact request, response,
moderation, reaction, and pagination behavior is defined in
`contracts/phase2-http.md`.

## Phase 3: Promotion And Commerce

Claim coupon in one PostgreSQL transaction:

1. lock active campaign/coupon row;
2. validate time, status, eligibility, stock, and per-user limit;
3. resolve prior idempotency result;
4. insert claim and increment claimed stock atomically.

Create order in one transaction from a server-read active price. Store item and
discount snapshots. Sandbox payment confirmation locks the order, validates the
transition, marks the claim redeemed, records payment, and inserts entitlement
under unique constraints. Replays return the existing terminal result.

Promotion exposes a discount quote capability to Order. Order never reads the
Promotion repository directly.

## Phase 4: Assistant Integration

```text
Chat UseCase
  -> Router Node
       DIRECT/CLARIFY -> Answer Node
       RESEARCH -> bounded Research Agent
                    -> search_knowledge
                    -> search_catalog
                    -> search_forum
                    -> search_web (optional)
                  -> Answer Node with typed ResearchResult
```

Research defaults: 30-second total budget, six model iterations, eight tool
calls, per-tool timeout, and request cancellation. Tools accept query/filter
arguments only; trusted identity and provider endpoints come from request
context/composition. No mutation tool is registered.
The bounded historical conversation context is passed to both Router and
Answer for every route; Chat excludes the current message before loading it.

## Data And Migration

See `data-model.md`. Money uses `bigint` minor units and three-character
currency. All timestamps are `timestamptz`. IDs remain internal bigint; public
resources expose stable slug or opaque ID strings where enumeration is a risk.

Migrations are immutable after merge. A migration runner records versions and
checksums, serializes execution with a PostgreSQL advisory lock, and refuses a
checksum mismatch. Rollback favors forward fixes; destructive changes need a
separate approved plan.

## Errors And HTTP Mapping

Entities/UseCases return small typed/sentinel errors for invalid input,
unauthenticated, forbidden, not found, conflict, exhausted stock, invalid state,
dependency unavailable, deadline, and cancellation. Presenters map them to one
consistent JSON error envelope with request ID. SQL/provider details stay in
logs, not client responses.

## Security

- bcrypt password hashes with a bounded password length before hashing;
- 32-byte random session token, SHA-256 hash at rest, rotation and expiry;
- Secure cookie in deployed HTTPS, HttpOnly, SameSite=Lax, scoped path;
- CSRF protection for cookie-authenticated mutation requests by SameSite plus a
  required same-origin/CSRF header policy before public cross-origin hosting;
- rate limits deferred until deployment topology is selected, but login/claim
  abuse is recorded as a rollout blocker;
- admin/user/owner checks in UseCases, not only route middleware;
- no secrets, passwords, tokens, full prompts, or full messages in logs.

## Observability

Structured logs carry request_id, module, operation, result, latency_ms, and
safe resource IDs. Commerce additionally records idempotency outcome and state
transition. Agent traces include route, iteration/tool counts, stop reason, and
provider without prompt/message content. `/healthz` is process liveness;
`/readyz` verifies required dependencies with a short timeout.

## Frontend

Keep React/Vite. Add a small router only when multiple screens land; reuse
browser APIs and existing dependencies where possible. API calls share error,
credentials, and cancellation handling. Add Vitest for non-trivial state/API
logic. Heavy assistant rendering is lazy-loaded after page routing exists.

## Rollout And Rollback

- Run ordered migrations against an isolated PostgreSQL + pgvector instance.
- Seed only deterministic demo catalog/admin data through an explicit command;
  never on every production startup.
- Deploy backward-compatible API/schema changes first, then frontend.
- Roll back application revision without reversing additive migrations.
- Real provider/model/payment smoke requires explicit target and credentials;
  sandbox payment never contacts a financial provider.

## Rejected Alternatives

- Microservices now: adds distributed transactions and operations without a
  scaling requirement.
- Agent-driven coupon/order execution: unnecessary security and correctness
  risk before ordinary UseCases are proven.
- Big-bang package move: high churn without product value.
- ORM/DI/event-bus framework: existing pgx and explicit composition are enough.
