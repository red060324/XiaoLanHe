# Phase 1 Delivery Report

- Status: `PHASE_1_COMPLETE`
- Date: 2026-08-31
- Branch: `codex/clean-architecture-refactor` (working tree)
- Authoritative spec: `./spec.md`

## Outcome

Phase-one application code is implemented: ordered embedded migrations,
request identity/readiness, Account, Catalog, admin authorization, deterministic
demo seed, Web Search failure semantics, frontend auth/catalog navigation, and
cancellable chat. Local static, unit, interface, race, frontend, architecture,
and dependency gates pass. GitHub CI additionally proves migrations and
repository behavior against PostgreSQL with pgvector, runs the seed twice, and
smokes the built container. Phase one is ready for review and merge, not rollout.

## Acceptance Criteria

| Criterion | Change | Evidence | Result |
|---|---|---|---|
| AC1 | Go modular Account/Catalog modules and boundary hook | `make ci`, architecture hook | PASS |
| AC2 | bcrypt accounts, hashed rotating sessions, roles, Origin check, knowledge admin gate | UseCase + HTTP + PostgreSQL tests | PASS |
| AC3 | public catalog, regional detail price, admin aggregate writes, demo seed | UseCase + HTTP + PostgreSQL/seed tests | PASS |
| AC8 | typed Web failures and frontend stream abort | Go and Vitest regression tests | PASS |
| AC9 | advisory-locked checksum migrations and DB constraints | PostgreSQL fresh/repeat/concurrent/checksum CI | PASS |
| AC10 | canonical CI, no Java runtime, report | local and GitHub CI | PASS |

## Verification

| Gate | Command/environment | Result |
|---|---|---|
| Go targeted | Account/Catalog/httpauth/WebSearch named tests | PASS |
| HTTP contract | Account, Catalog, knowledge admin tests | PASS |
| Frontend | `npm test` (2 files, 6 tests) | PASS |
| Dependency audit | `npm audit fix` final audit | PASS, 0 vulnerabilities |
| Full local CI | `make ci BASE_REF=origin/master` | PASS |
| Java absence | repository file scan | PASS |
| PostgreSQL integration | GitHub Actions pgvector service | PASS |
| Seed idempotency | two consecutive `go run ./cmd/seed` executions | PASS |
| Docker image and local-process smoke | GitHub Actions build/run; health/readiness/catalog/SPA | PASS |
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

1. Deploy the reviewed revision to an isolated shared environment.
2. Run approved real-model/Web Search smoke plus authenticated/admin HTTP smoke.
3. Record revision, environment, commands, results, and rollback revision before rollout.
