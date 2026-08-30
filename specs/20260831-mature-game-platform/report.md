# Phase 1 Delivery Report

- Status: `IMPLEMENTED_LOCAL_DB_PENDING`
- Date: 2026-08-31
- Branch: `codex/clean-architecture-refactor` (working tree)
- Authoritative spec: `./spec.md`

## Outcome

Phase-one application code is implemented: ordered embedded migrations,
request identity/readiness, Account, Catalog, admin authorization, deterministic
demo seed, Web Search failure semantics, frontend auth/catalog navigation, and
cancellable chat. Local static, unit, interface, race, frontend, architecture,
and dependency gates pass.

The host has no Docker, `psql`, or reachable test PostgreSQL, so fresh/repeat/
concurrent migration execution and repository SQL behavior remain unverified.
Phase one is ready for code review, not merge or rollout.

## Acceptance Criteria

| Criterion | Change | Evidence | Result |
|---|---|---|---|
| AC1 | Go modular Account/Catalog modules and boundary hook | `make ci`, architecture hook | PASS |
| AC2 | bcrypt accounts, hashed rotating sessions, roles, Origin check, knowledge admin gate | UseCase + HTTP tests | PASS without real DB |
| AC3 | public catalog, regional detail price, admin aggregate writes, demo seed | UseCase + HTTP tests | PASS without real DB |
| AC8 | typed Web failures and frontend stream abort | Go and Vitest regression tests | PASS |
| AC9 | advisory-locked checksum migrations and DB constraints | code/static review only | BLOCKED: PostgreSQL unavailable |
| AC10 | canonical CI, no Java runtime, report | `make ci BASE_REF=origin/master` | PASS locally |

## Verification

| Gate | Command/environment | Result |
|---|---|---|
| Go targeted | Account/Catalog/httpauth/WebSearch named tests | PASS |
| HTTP contract | Account, Catalog, knowledge admin tests | PASS |
| Frontend | `npm test` (2 files, 6 tests) | PASS |
| Dependency audit | `npm audit fix` final audit | PASS, 0 vulnerabilities |
| Full local CI | `make ci BASE_REF=origin/master` | PASS |
| Java absence | repository file scan | PASS |
| PostgreSQL integration | Docker/psql probe | BLOCKED: tools unavailable |
| Docker image | `make docker-build` | NOT RUN: Docker unavailable |
| Rollout/model/Web smoke | shared deployment | NOT RUN: rollout-only |

## Architecture And Debt

- The deployed shape remains one Go modular monolith and one PostgreSQL.
- Account and Catalog own UseCases/entities/repository adapters; composition
  stays in `cmd/xiaolanhe`.
- No ORM, DI container, router framework, queue, Redis, microservice, or
  additional Agent was introduced.
- The existing Streamdown dependency still produces large Vite chunks. This is
  a performance follow-up, not a phase-one correctness blocker.

## Rollout And Rollback

Run migrations and seed only in an isolated environment first. Additive
migrations permit application rollback to the previous revision; do not edit
an applied migration. The seed command changes the selected admin password and
must not be run against an unknown production account.

## Required Next Evidence

1. Start isolated PostgreSQL with pgvector and run fresh, repeated, checksum,
   concurrent-runner, session, catalog price, and seed idempotency cases.
2. Build the Docker image and smoke `/healthz`, `/readyz`, auth, catalog, static
   SPA fallback, and admin knowledge write.
3. Record revision/environment/rollback in this report before merge or rollout.
