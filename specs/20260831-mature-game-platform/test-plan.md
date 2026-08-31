# Test Plan

- Status: `APPROVED` (2026-08-31)
- Authoritative spec: `./spec.md`

## General Gates

Every behavior change starts with the narrowest test and ends with `make ci`.
Go tests use deterministic fakes and `httptest`; PostgreSQL invariants are also
proved against an isolated pgvector database. Frontend behavior uses Vitest
when introduced. Compilation or an empty test selection is not evidence.

## Phase 1: Foundation, Account, Catalog

| ID | Class | Layer | Scenario | Expected result |
|---|---|---|---|---|
| V1 | PRE_MERGE | entity/usecase | username/password/session validation | boundary values and generic auth failure |
| V2 | PRE_MERGE | security | register/login/logout/expiry/revocation/rotation | hashes only at rest; cookies have required flags |
| V3 | PRE_MERGE | authorization | anonymous/user/owner/admin matrix | forbidden writes never reach repository |
| V4 | PRE_MERGE | migration | fresh DB, repeat run, concurrent runner, checksum mismatch | exactly-once ordered versions; mismatch fails |
| V5 | PRE_MERGE | catalog | search, detail, cursor limits, inactive rows, regional price | stable contract and pagination |
| V6 | PRE_MERGE | HTTP | malformed/unknown JSON, request size, error envelope, request ID | consistent status/body; no internal detail |
| V7 | PRE_MERGE | regression | anonymous knowledge write | 401; admin succeeds |
| V8 | PRE_MERGE | provider | Web Search timeout/non-2xx/malformed/empty | failures distinct from empty result |
| V9 | PRE_MERGE | lifecycle | liveness, readiness failure, shutdown cancellation | observable and bounded |
| V10 | PRE_MERGE | frontend | auth state, catalog states, aborted/error chat stream | no no-op affordance or empty assistant message |

Phase-one HTTP assertions use `contracts/phase1-http.md` as the wire source of
truth.

## Phase 2: Community

- Post/comment validation, ownership, admin hide, soft delete, missing game.
- Feed/game-feed keyset pagination under concurrent inserts.
- Reaction create/replay/delete and uniqueness under concurrency.
- HTTP contracts and frontend create/feed/detail/error flows.

## Phase 3: Promotion And Commerce

- Discount entity boundary table: fixed, percentage basis points, minimum spend,
  currency mismatch, cap at subtotal, and zero total.
- Coupon inactive/not-started/expired/ineligible/exhausted/per-user-limit paths.
- Concurrent final-stock claims prove no oversell; same idempotency key returns
  one claim, different keys still respect per-user limits.
- Order uses server price, rejects client price/currency, stores snapshots, and
  rejects invalid transitions.
- Payment replay creates one payment, one redemption, and one entitlement.
- Cross-user order access is forbidden; history pagination remains stable.
- Frontend Deals/checkout/orders/owned states and error recovery.

## Phase 4: Assistant

- Router structured direct/clarify/research decisions and deterministic fallback.
- Research one/multiple/refined tool calls, no result, partial failure, all
  failure, max iterations, max tool calls, deadline, and cancellation.
- Deterministic Knowledge/Web/Catalog/Forum input errors produce `invalid`
  observations without being counted as provider failures.
- Registry contains only read-only knowledge/catalog/forum/optional Web tools;
  mutation-like or unknown tool request is rejected.
- Knowledge/Web queries are trimmed and reject blank or over-100-rune input
  before storage, embedding, or provider calls; malformed knowledge limits
  return 400 without downstream work.
- Answer direct/evidence/degraded/clarify/stream cases with citations; an empty
  model stream emits a fallback reply before EOF.
- Chat persistence order and no partial assistant persistence on failure.
- Logs/traces exclude full messages, prompts, tokens, cookies, and passwords.
- Real model/Web calls remain ROLLOUT and never substitute for deterministic
  PRE_MERGE tests.

## Static And Build Gates

- `gofmt`, `go vet ./...`, all Go tests, relevant race tests.
- Architecture, spec-drift, link, placeholder, and diff checks.
- Frontend tests and production build.
- Docker image build after lifecycle/deployment changes.
- Search for Java/Maven runtime files; expected none.

## Rollout

- Fresh and upgrade migration against isolated PostgreSQL + pgvector.
- Register/login, catalog, post/comment, coupon claim, order/payment/ownership,
  assistant direct/research/SSE smoke.
- Concurrency smoke for final coupon stock.
- Inspect readiness, request/error logs, commerce transition logs, Agent stop
  reasons, and absence of sensitive data.
- Record exact revision, environment, commands, results, rollback revision, and
  every skipped external check.

## Exit Criteria

- All PRE_MERGE rows for the delivered phase have actual PASS evidence.
- Requirement-to-test traceability is complete.
- No unresolved critical/high defect in the delivered slice.
- Rollout-only checks remain explicit and do not block code review unless the
  deployment environment is part of that phase's acceptance.
