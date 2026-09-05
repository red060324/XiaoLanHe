# Delivery And Readiness Report

- Status: `VERIFYING`
- Date: 2026-09-02
- Branch: `codex/clean-architecture-refactor`
- Verified implementation: current worktree based on `e34338e`; no commit or push was performed
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
| AC2 identity and authorization | Account, server conversation ownership, browser account isolation/authentication single-flight, and defense-in-depth knowledge write tests; admin/owner/anonymous cases | PASS |
| AC3 catalog and ownership | Catalog UseCase/HTTP/PostgreSQL, locked final-price revalidation/snapshots, and frontend summary-to-purchase-detail tests | PASS |
| AC4 community | entity/UseCase/HTTP/PostgreSQL atomic visibility and Unicode-boundary tests plus viewer/detail-generation frontend tests | PASS |
| AC5 atomic coupon claim | entity/UseCase/HTTP/PostgreSQL concurrency tests plus cross-tab claim reconciliation and navigation-stable idempotency | PASS |
| AC6 order and entitlement | entity/UseCase/HTTP/PostgreSQL/frontend replay, final coupon/price revalidation, ownership, and duplicate-edition payment tests | PASS |
| AC7 bounded read-only Assistant | deterministic Agent/tool/allowlist/citation-safety and canonical SSE tests | PASS |
| AC8 failure and cancellation | provider, Agent budget, bounded request-body transport, shared HTTP 504 deadline contract, cancellation, and SSE tests | PASS |
| AC9 migrations and data invariants | fresh/repeat/concurrent/checksum and migrations 001-005 | PASS |
| AC10 delivery evidence | current-worktree `make ci` passes and skipped external checks are explicit; publishing and a latest clean-checkout GitHub Actions run remain T39 | VERIFYING |

## Verification

| Gate | Evidence | Result |
|---|---|---|
| Latest local full command | 2026-09-02 dirty worktree at the time of the run: `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 database URL> make ci BASE_REF=e34338e585e5a3cdeaea784fac0b4f1ceb49d947`; Go vet/tests/race, 69 frontend tests, TypeScript/Vite build, hooks, architecture and spec drift | PASS |
| Latest clean-clone command | committed `75c8a03` only: local no-hardlink clone, lockfile `npm ci`, fresh PostgreSQL 17 + pgvector, then `make ci BASE_REF=origin/master` | PASS |
| 2026-09-02 focused audit | affected Go packages, real PostgreSQL product integration, real-socket HTTP transport, and 5 frontend test files / 69 Vitest tests | PASS |
| Go vet and all tests | 2026-09-02 current-worktree `make ci`; historical full GitHub Actions `33378359262` at `c2ba3fd` | PASS |
| Go race | 2026-09-02 current-worktree full repository race; historical GitHub Actions `33378359262` | PASS |
| Frontend | 5 test files, 69 Vitest tests, TypeScript build, and Vite production build; shared REST/SSE Assistant errors, live/alert/pressed semantics, single-scroll Commerce layout, literal `data:` content, canonical sessions, responsive destinations, serialized auth mutations, real-contract catalog-summary purchase hydration, latest-request authentication/catalog/feed/detail/comment-page/reaction/Commerce/chat-stream state, same-post ABA isolation, abandoned navigation cleanup, cross-view/tab/user ownership, navigation-stable idempotency, cross-tab claim reconciliation, account-isolated chat history, and mixed-edition ownership regressions | PASS |
| Architecture/spec/docs | current-worktree hooks, architecture, spec drift and link checks | PASS |
| PostgreSQL | 2026-09-02 isolated PostgreSQL 17 + pgvector and historical GitHub Actions `33377050477` passed migrations plus identity, literal Catalog/knowledge search, forum, hidden-post comment-list/write rejection, promotion, available-coupon reservation, final campaign/coupon-definition/price order revalidation, and entitlement integration | PASS |
| Stale-campaign order regression | `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 database URL> go test -run '^TestProductPostgres$' -count=1 -v ./internal/adapter/postgres` | PASS; paused campaign rejected at final order transaction |
| Lock-expiry transaction regressions | focused `TestProductPostgres` price, order-campaign, and claim-campaign cases wait on the real database lock across the end boundary | PASS; no expired order/claim side effect and no stock increment |
| Claim transaction boundary | final locked claim uses post-lock database time for campaign validation and timestamps | PASS |
| Seed and container | GitHub Actions `33378359262`: repeated seed, health/readiness/SPA, account, admin catalog, community post/comment/reaction, coupon claim/replay/reservation, order create/replay, sandbox payment/replay, entitlement/ownership and logout | PASS |
| Assistant | Browser history isolation, UUIDv4/session ownership, bounded context retained across direct/clarify/evidence answers, fake model/tool iteration, historical-context boundary, failure/budget/cancellation, four-tool allowlist, REST/SSE citations | PASS |
| Assistant evidence boundary | retrieved text is quoted as untrusted Answer Node data under a fixed system rule; every Research tool caps evidence content at 800 Unicode runes | PASS |
| Knowledge ingestion boundary | short paragraphs retain their grouping; a single long paragraph is split by Unicode rune so every embedding/retrieval chunk stays within 800 runes without content loss | PASS |
| Request correlation | invalid client IDs replaced once and the validated ID is shared by response and REST/SSE completion logs | PASS |
| Search input boundary | query trim/blank/100-rune checks, literal Catalog and knowledge `%`/`_` semantics, and knowledge limit 1-10; invalid requests prove zero downstream calls | PASS |
| HTTP deadline contract | request-scoped `context.DeadlineExceeded` is mapped once to 504 `deadline_exceeded` and reused by Account, auth, Catalog, Community, Promotion, Order, Knowledge, Web Search, and Assistant entries | PASS |
| HTTP request-body boundary | standard transport accepts fixed/chunked non-chat bodies at 1 MiB, enforces the smaller chat route limit for REST/SSE including `Expect: 100-continue`, and rejects oversized/invalid streams with correlated envelopes and close; default netpoll stops active senders; cleanup uses an absolute read deadline | PASS |
| Knowledge cancellation | embedding cancellation/deadline is propagated after keyword retrieval and prevents vector search; ordinary embedding failures still degrade to keyword evidence | PASS |
| Knowledge write authorization | the UseCase itself rejects anonymous and non-admin principals before embedding/storage even if entry middleware is absent | PASS |
| Account login work factor | missing-account, stored-account, and disabled-account generic failures execute the expected bcrypt comparison; dummy and production hashes both use cost 12 | PASS |
| Web Search contract | SearXNG identity is fixed to the actual adapter; nonexistent cache/provider-selection surfaces are absent | PASS |
| Web Search response boundary | SearXNG response bodies are capped at 1 MiB and oversized payloads fail before JSON allocation | PASS |
| Embedding response boundary | provider response bodies are capped at 64 KiB per requested input and oversized payloads fail before JSON/vector allocation | PASS |
| Assistant error presentation | REST and SSE extract the shared server message; raw JSON envelopes are not shown to users | PASS |
| Assistant stream contract | SSE returns the canonical conversation ID; typed message/error parsing rejects empty success and preserves decoded content beginning with `data:` | PASS |
| Real client disconnect | default-netpoll real TCP disconnect cancels request context, closes the blocked upstream stream, and stores no partial Assistant reply | PASS |
| Async accessibility and layout | errors are alerts; current streamed Assistant output is a polite busy status; Commerce tabs and reactions expose pressed state; App and Commerce share one page scroll owner | PASS |
| Citation presentation boundary | untrusted title whitespace is collapsed, Markdown delimiters are escaped, and credential-bearing URLs are omitted | PASS |
| Community visibility and input boundary | one PostgreSQL statement enforces parent visibility while listing comments; escaped Unicode at the decoded rune limit is accepted and one rune over is rejected | PASS |
| Final order transaction boundary | locked campaign, coupon definition, claim, catalog, and regional price state is revalidated and current snapshots are stored; stale quotes insert no order | PASS |
| Public-only scan | no private domains/platform names; dependencies resolve from public modules | PASS |
| Java absence | no Java/Maven/Gradle source or build files | PASS |
| Isolated migration and full product smoke | rollout T16, `scripts/smoke-product.sh`, GitHub Actions `33378359262` | PASS |
| Real model/Web and hosted-environment smoke | rollout task T17 | NOT RUN |
| Incremental coverage threshold | repository/request define no threshold; non-Flux run | SKIPPED |
| In-app visual browser smoke | browser-control session could not initialize; DOM regressions and production build passed, but no visual result is inferred | NOT RUN |

## Current Worktree Traceability

| Files | Responsibility and evidence |
|---|---|
| `frontend/xiaolanhe-web/src/App.tsx`, `App.test.tsx` | responsive destinations, auth and chat-stream ownership, Commerce route scroll ownership, canonical sessions, error alerts, and logout single-flight |
| `frontend/xiaolanhe-web/src/components/ChatMessageList.tsx`, `ChatMessageList.test.tsx` | streaming-only polite live/busy semantics |
| `frontend/xiaolanhe-web/src/components/CommercePage.tsx`, `CommercePage.test.tsx` | request/viewer ownership, navigation-stable idempotency, reconciliation, error alerts, selected-view semantics, and checkout/order flows |
| `frontend/xiaolanhe-web/src/components/CommunityPage.tsx`, `CommunityPage.test.tsx` | feed/detail/viewer generations, mutation single-flight, async error alerts, and reaction pressed state |
| `frontend/xiaolanhe-web/src/lib/api.ts`, `api.test.ts` | canonical SSE conversation ID and typed message/error parsing |
| `frontend/xiaolanhe-web/src/styles.css` | six-destination responsive navigation and shared page-stage layout |
| `internal/adapter/eino/nodes.go`, `nodes_test.go` | citation title/URL rendering boundary |
| `internal/adapter/postgres/phase1_integration_test.go` | real PostgreSQL visibility, final-state, and lock-expiry invariants |
| `internal/community/entry/http.go`, `http_test.go` | decoded Unicode request boundaries |
| `internal/community/repository/postgres/store.go` | atomic public-parent reads and moderation/write serialization |
| `internal/community/usecase/service.go`, `service_test.go` | ownership and visibility delegation boundaries |
| `internal/entry/http.go`, `http_test.go` | route-specific body limits, absolute rejected-body deadline, SSE cancellation, and real standard/netpoll transport evidence |
| `internal/order/repository/postgres/store.go` | post-lock database time, locked current price/coupon revalidation, and authoritative snapshots |
| `internal/promotion/repository/postgres/store.go` | post-lock campaign validation and claim timestamps |
| `internal/usecase/knowledge.go`, `knowledge_test.go` | defense-in-depth admin authorization |
| `specs/20260831-mature-game-platform/tasks.md`, `test-plan.md`, `report.md` | current worktree scope, exact local evidence, skipped gates, and T39/T17 status |

## Readiness Checklist

| Item | Status | Evidence |
|---|---|---|
| Human spec/design approval | PASS | T0, approved 2026-08-31 |
| Spec/plan/tasks/test plan/data model/contracts/report | PASS | this spec directory |
| All product PRE_MERGE behavior | PASS | T1-T15 and T20-T85 behavior tests pass |
| Final documentation and latest code-bearing clean-checkout CI | VERIFYING | T39; committed `75c8a03` passed a historical clean local clone; the latest authorized push and clean-checkout GitHub Actions run remain pending |
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
| Streamdown previously kept the initial Vite chunk near 1 MB | resolved on 2026-09-04 with a lazy message-renderer boundary: initial entry is about 246 kB / 76 kB gzip; optional Mermaid/Shiki chunks remain deferred and may still exceed 500 kB |
| In-app visual smoke could not be executed in this run | retain DOM regressions and production-build evidence; repeat visual responsive/scroll inspection when browser control is available |
| Sandbox payment is not a financial integration | explicit product boundary; no real provider, refund, or tax implementation is planned |

## Decision

- Ready for review: yes; every local product PRE_MERGE behavior gate passes.
- Ready for merge: no; T39 requires the latest revision to pass clean-checkout GitHub Actions.
- Ready for rollout: no; T17 and abuse protection remain explicit rollout work.
