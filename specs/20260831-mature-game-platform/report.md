# Delivery And Readiness Report

- Status: `PRE_MERGE_COMPLETE`
- Date: 2026-08-31
- Branch: `codex/clean-architecture-refactor`
- Verified implementation: branch HEAD; immutable evidence is listed below
- Authoritative spec: `./spec.md`

## Outcome

The approved pre-merge product scope is implemented as one Go modular
monolith. It includes Account, Catalog, Community, Promotion, sandbox Commerce,
and a read-only game Assistant. Router and Answer remain bounded Nodes;
Research is the only Agent and may call only knowledge, catalog, forum, and
optional public Web read tools. The React application and API ship in the same
container with PostgreSQL/pgvector.

No real payment provider, transactional Agent tool, microservice split, queue,
Redis, background Agent, or Java runtime was introduced.

## Acceptance Criteria

| Criterion | Evidence | Result |
|---|---|---|
| AC1 architecture and Go-only backend | architecture hook, import review, Java/Maven/Gradle scan | PASS |
| AC2 identity and authorization | Account, server conversation ownership, and browser account-isolation tests; admin/owner/anonymous cases | PASS |
| AC3 catalog and ownership | Catalog UseCase/HTTP/PostgreSQL and frontend tests | PASS |
| AC4 community | entity/UseCase/HTTP/PostgreSQL and frontend tests | PASS |
| AC5 atomic coupon claim | entity/UseCase/HTTP/PostgreSQL concurrency tests | PASS |
| AC6 order and entitlement | entity/UseCase/HTTP/PostgreSQL/frontend replay, ownership, and duplicate-edition payment tests | PASS |
| AC7 bounded read-only Assistant | deterministic Agent/tool/allowlist/citation tests | PASS |
| AC8 failure and cancellation | provider, Agent budget, deadline, cancellation, SSE tests | PASS |
| AC9 migrations and data invariants | fresh/repeat/concurrent/checksum and migrations 001-005 | PASS |
| AC10 delivery evidence | local `make ci`, public GitHub Actions, contracts and container smoke | PASS |

## Verification

| Gate | Evidence | Result |
|---|---|---|
| Latest local full command | `make ci`: Go tests, full race, frontend tests/build, hooks, architecture and spec drift | PASS |
| Go vet and all tests | local `make ci`; full GitHub Actions `33377050477` at `68dddc5` | PASS |
| Go race | local full repository race; GitHub Actions `33377050477` | PASS |
| Frontend | 4 files, 27 Vitest tests and Vite production build; coupon-claim restoration/reservation, account-isolated chat history, failed-stream and mixed-edition ownership regressions | PASS |
| Architecture/spec/docs | hooks, architecture, spec drift and link checks | PASS |
| PostgreSQL | GitHub Actions `33377050477` passed migrations plus identity, forum, promotion, available-coupon reservation, order and entitlement integration | PASS |
| Seed and container | GitHub Actions `33377050477`: repeated seed, health/readiness/catalog/community/deals/SPA | PASS |
| Assistant | Browser history isolation, UUIDv4/session ownership, fake model/tool iteration, historical-context boundary, failure/budget/cancellation, four-tool allowlist, REST/SSE citations | PASS |
| Request correlation | invalid client IDs replaced once and the validated ID is shared by response and REST/SSE completion logs | PASS |
| Search input boundary | query trim/blank/100-rune checks and knowledge limit 1-10; invalid requests prove zero downstream calls | PASS |
| Web Search contract | SearXNG identity is fixed to the actual adapter; nonexistent cache/provider-selection surfaces are absent | PASS |
| Public-only scan | no private domains/platform names; dependencies resolve from public modules | PASS |
| Java absence | no Java/Maven/Gradle source or build files | PASS |
| Real model/Web and deployed product smoke | rollout tasks T16-T17 | NOT RUN |

## Readiness Checklist

| Item | Status | Evidence |
|---|---|---|
| Human spec/design approval | PASS | T0, approved 2026-08-31 |
| Spec/plan/tasks/test plan/data model/contracts/report | PASS | this spec directory |
| All product PRE_MERGE behavior | PASS | T1-T15 and T20-T33 |
| Final documentation and latest code-bearing clean-checkout CI | PASS | GitHub Actions `33377050477` |
| Migration and application rollback | PASS | additive migrations plus public deployment guide |
| Sensitive-data review | PASS | logs omit prompts/messages/secrets; repository scan clean |
| Observability | PASS | validated request ID, result/latency and Agent route/iteration/tool/stop reason |

## Architecture And Deliberate Limits

- Ordinary modules own their UseCases/entities/repository adapters; composition
  remains in `cmd/xiaolanhe`.
- The Research Agent and ordinary services share one process because every run
  is request-scoped and bounded.
- No ORM, DI container, tool registry framework, event bus, Redis, or extra
  Agent was added.
- Citation links are appended deterministically; only HTTP(S) and local `/api/`
  resource URLs are emitted.
- Knowledge and Web query validation is owned by the shared UseCase boundary,
  so direct HTTP and Research Agent calls cannot bypass it.

## Rollout, Rollback, And Residual Risk

Migrations 001-005 are additive and startup-serialized. Take a database
snapshot or `pg_dump` before rollout; roll back the application image without
editing applied migrations. Use a reviewed forward migration for schema fixes.

| Risk | Disposition |
|---|---|
| Real model and optional Web output are nondeterministic/cost-bearing | T17 rollout smoke; inspect safe traces and citations |
| No shared hosted environment has run authenticated/admin full-product smoke | T16 rollout task |
| Public API abuse and provider-cost admission control are not implemented | governed by draft `../20260831-public-rollout-hardening/`; rollout remains blocked pending approval and delivery |
| Checked-in Render free database expires after 30 days and has no managed backups | demo only; choose durable PostgreSQL and tested backups before persistent rollout |
| Streamdown keeps the initial Vite chunk near 1 MB | accepted for demo; split only when measured load time matters |
| Sandbox payment is not a financial integration | explicit product label; real provider requires a separate spec |

## Decision

- Ready for review: yes.
- Ready for merge: yes; all PRE_MERGE tasks and final clean-checkout CI pass.
- Ready for rollout: no; T16-T17 and abuse protection remain explicit rollout work.
