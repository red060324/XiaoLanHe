# Delivery And Readiness Report

- Status: `VERIFYING`
- Date: 2026-09-01
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
| AC3 catalog and ownership | Catalog UseCase/HTTP/PostgreSQL and frontend summary-to-purchase-detail tests | PASS |
| AC4 community | entity/UseCase/HTTP/PostgreSQL and frontend tests | PASS |
| AC5 atomic coupon claim | entity/UseCase/HTTP/PostgreSQL concurrency tests | PASS |
| AC6 order and entitlement | entity/UseCase/HTTP/PostgreSQL/frontend replay, ownership, and duplicate-edition payment tests | PASS |
| AC7 bounded read-only Assistant | deterministic Agent/tool/allowlist/citation tests | PASS |
| AC8 failure and cancellation | provider, Agent budget, shared HTTP 504 deadline contract, cancellation, SSE tests | PASS |
| AC9 migrations and data invariants | fresh/repeat/concurrent/checksum and migrations 001-005 | PASS |
| AC10 delivery evidence | local `make ci`, public GitHub Actions, contracts and container smoke | PASS |

## Verification

| Gate | Evidence | Result |
|---|---|---|
| Latest local full command | T65 `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 database URL> make ci BASE_REF=origin/master`: Go vet/tests/race against PostgreSQL 17 + pgvector, 50 frontend tests/build, hooks, architecture and spec drift | PASS |
| Go vet and all tests | latest local `make ci`; full GitHub Actions `33378359262` at `c2ba3fd` | PASS |
| Go race | latest local full repository race; GitHub Actions `33378359262` | PASS |
| Frontend | 4 files, 50 Vitest tests and Vite production build; real-contract catalog-summary purchase hydration, latest-request authentication/catalog/feed/detail/comment-page/reaction/Commerce/chat-stream state, abandoned game-detail/post-create/post-edit/post-delete navigation, cross-view and Commerce-tab error cleanup, abandoned order-history failure isolation, stale-reaction failure isolation, cross-post comment submission/edit/deletion isolation, duplicate-comment prevention, coupon-claim restoration/reservation, post-claim game-filter and mid-checkout coupon-selection preservation, account-isolated chat history, failed-stream and mixed-edition ownership regressions | PASS |
| Architecture/spec/docs | hooks, architecture, spec drift and link checks | PASS |
| PostgreSQL | Isolated local PostgreSQL 17 + pgvector 0.8.6 and GitHub Actions `33377050477` passed migrations plus identity, literal Catalog/knowledge search, forum, hidden-post final comment/reaction rejection, promotion, available-coupon reservation, final campaign/price order revalidation, and entitlement integration | PASS |
| Stale-campaign order regression | `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 database URL> go test -run '^TestProductPostgres$' -count=1 -v ./internal/adapter/postgres` | PASS; paused campaign rejected at final order transaction |
| Seed and container | GitHub Actions `33378359262`: repeated seed, health/readiness/SPA, account, admin catalog, community post/comment/reaction, coupon claim/replay/reservation, order create/replay, sandbox payment/replay, entitlement/ownership and logout | PASS |
| Assistant | Browser history isolation, UUIDv4/session ownership, bounded context retained across direct/clarify/evidence answers, fake model/tool iteration, historical-context boundary, failure/budget/cancellation, four-tool allowlist, REST/SSE citations | PASS |
| Knowledge ingestion boundary | short paragraphs retain their grouping; a single long paragraph is split by Unicode rune so every embedding/retrieval chunk stays within 800 runes without content loss | PASS |
| Request correlation | invalid client IDs replaced once and the validated ID is shared by response and REST/SSE completion logs | PASS |
| Search input boundary | query trim/blank/100-rune checks, literal Catalog and knowledge `%`/`_` semantics, and knowledge limit 1-10; invalid requests prove zero downstream calls | PASS |
| HTTP deadline contract | request-scoped `context.DeadlineExceeded` is mapped once to 504 `deadline_exceeded` and reused by Account, auth, Catalog, Community, Promotion, Order, Knowledge, Web Search, and Assistant entries | PASS |
| Knowledge cancellation | embedding cancellation/deadline is propagated after keyword retrieval and prevents vector search; ordinary embedding failures still degrade to keyword evidence | PASS |
| Account login work factor | missing-account and stored-account generic failures both execute bcrypt cost 12; regression inspects the actual dummy hash passed to the password adapter | PASS |
| Web Search contract | SearXNG identity is fixed to the actual adapter; nonexistent cache/provider-selection surfaces are absent | PASS |
| Public-only scan | no private domains/platform names; dependencies resolve from public modules | PASS |
| Java absence | no Java/Maven/Gradle source or build files | PASS |
| Isolated migration and full product smoke | rollout T16, `scripts/smoke-product.sh`, GitHub Actions `33378359262` | PASS |
| Real model/Web and hosted-environment smoke | rollout task T17 | NOT RUN |

## Readiness Checklist

| Item | Status | Evidence |
|---|---|---|
| Human spec/design approval | PASS | T0, approved 2026-08-31 |
| Spec/plan/tasks/test plan/data model/contracts/report | PASS | this spec directory |
| All product PRE_MERGE behavior | PASS | T1-T15 and T20-T65 pass |
| Final documentation and latest code-bearing clean-checkout CI | VERIFYING | T39; local full gate passes, latest GitHub Actions requires the new revision |
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
| No shared hosted environment has run authenticated/admin full-product smoke | isolated T16 passed; hosted target still requires explicit approval |
| Public API abuse and provider-cost admission control are not implemented | governed by draft `../20260831-public-rollout-hardening/`; rollout remains blocked pending approval and delivery |
| Checked-in Render free database expires after 30 days and has no managed backups | demo only; choose durable PostgreSQL and tested backups before persistent rollout |
| Streamdown keeps the initial Vite chunk near 1 MB | accepted for demo; split only when measured load time matters |
| Sandbox payment is not a financial integration | explicit product label; real provider requires a separate spec |

## Decision

- Ready for review: yes; every local product PRE_MERGE behavior gate passes.
- Ready for merge: no; T39 requires the latest revision to pass clean-checkout GitHub Actions.
- Ready for rollout: no; T17 and abuse protection remain explicit rollout work.
