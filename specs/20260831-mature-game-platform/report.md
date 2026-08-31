# Phase 2 Delivery Report

- Status: `PHASE_2_COMPLETE`
- Date: 2026-08-31
- Branch: `codex/clean-architecture-refactor` (working tree)
- Authoritative spec: `./spec.md`

## Outcome

The Community vertical slice and Phase 3 atomic coupon claim are implemented on
top of the Phase 1 foundation.
Users can browse stable game-scoped feeds, create and manage owned posts and
comments, and add idempotent reactions. Admin moderation, soft deletion,
cross-module game validation, React navigation, detail/error states, and the
third ordered migration are included. Users can also browse current deals and
claim a coupon without overselling stock or duplicating an idempotent request.
The fourth ordered migration, deterministic demo coupon, and claim HTTP contract
are included. The pushed revisions passed public CI and are ready for review,
not rollout.

## Acceptance Criteria

| Criterion | Change | Evidence | Result |
|---|---|---|---|
| AC1 | Module-first Community Entity/UseCase/Presenter/repository boundaries | architecture hook + Go tests | PASS |
| AC4 | owned posts/comments, moderation, soft deletion, reactions, stable cursor feed | UseCase + HTTP + PostgreSQL + frontend tests | PASS |
| AC5 | campaign window, stock, per-user limit, exact idempotency replay, concurrent final stock | Entity + UseCase + HTTP + PostgreSQL concurrency tests | PASS |
| AC9 | additive migrations 003/004, checks, uniqueness, indexes | migration/integration test; pushed CI is authoritative | PASS |
| AC10 | contract, tests, static gates, no Java production code | local split gates + GitHub Actions | PASS |

## Verification

| Gate | Command/environment | Result |
|---|---|---|
| Go targeted | Community Entity, UseCase, Entry, Catalog capability | PASS |
| HTTP contract | public feed, malformed ID, anonymous write, owner denial | PASS |
| Frontend | `npm test` (3 files, 13 tests) | PASS |
| Affected race | Community, Catalog, PostgreSQL adapter packages | PASS |
| Static/build | vet, hooks, architecture, spec drift, Vite production build | PASS |
| Full local CI | `make ci BASE_REF=origin/master` | ENVIRONMENT BLOCKED: desktop sandbox forbids legacy `httptest` port binding; equivalent split gates pass |
| Java absence | repository file scan | PASS |
| PostgreSQL integration | GitHub Actions run `33350379345`: migration, pagination, comments, concurrent reaction | PASS |
| Container smoke | GitHub Actions run `33350379345`: health/readiness/catalog/community/SPA | PASS |
| Coupon claim | GitHub Actions run `33352631674`: migration 004, concurrent replay/final stock, seed idempotency, deals smoke | PASS |
| Rollout/model/Web smoke | shared deployment | NOT RUN: rollout-only |

## Architecture And Debt

- The deployed shape remains one Go modular monolith and one PostgreSQL.
- Account, Catalog, Community, and Promotion own UseCases/entities/repository adapters;
  composition stays in `cmd/xiaolanhe`.
- No ORM, DI container, router framework, queue, Redis, microservice, or
  additional Agent was introduced.
- The existing Streamdown dependency still produces large Vite chunks. This is
  a performance follow-up, not a Phase 2 correctness blocker.

## Rollout And Rollback

Run migrations 003 and 004 in an isolated environment first. They are additive, so the
application can roll back while the new tables remain; never edit the applied
migrations. Public feeds ignore hidden/deleted rows. Coupon stock is protected
by database row/advisory locks, but rollout should still smoke owner/admin
mutations and final-stock contention.

## Required Next Evidence

1. Implement T11 order, sandbox payment, coupon redemption, and entitlement on
   top of coupon revision `7a23c45`.
2. Keep authenticated/admin rollout smoke and real-model/Web smoke rollout-only.
