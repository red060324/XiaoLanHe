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
| V11 | PRE_MERGE | catalog/PostgreSQL | admin aggregate update omits a previously active regional price | omitted price is no longer returned or purchasable; submitted replacement remains active |
| V12 | PRE_MERGE | catalog/commerce | user owns one of multiple editions | owned edition is marked unavailable; another edition remains purchasable |
| V13 | PRE_MERGE | frontend | an initial identity lookup or earlier explicit login finishes after a newer login or logout | the latest explicit authentication result remains authoritative |
| V14 | PRE_MERGE | frontend | initial catalog read finishes after a later search | the latest search result and loading/error state remain authoritative |
| V15 | PRE_MERGE | frontend | game-detail loading finishes after the user leaves the catalog | returning to the catalog shows the game list, not the abandoned detail |
| V16 | PRE_MERGE | catalog/PostgreSQL | name/slug query contains SQL wildcard characters such as `%` or `_` | characters are matched literally and do not broaden the result set |
| V17 | PRE_MERGE | knowledge/usecase | one paragraph exceeds the 800-rune chunk target | every chunk stays within 800 runes and reconstructs the original content |
| V18 | PRE_MERGE | knowledge/PostgreSQL | keyword query contains SQL wildcard characters such as `%` or `_` | characters are matched literally and cannot inject unrelated evidence into Assistant retrieval |
| V19 | PRE_MERGE | HTTP | a product UseCase returns `context.DeadlineExceeded` before a response starts | 504 `deadline_exceeded` with the standard request-ID envelope |
| V20 | PRE_MERGE | knowledge/usecase | embedding returns request cancellation or deadline after keyword retrieval | context error reaches the Research Agent and HTTP caller; vector search is not started |
| V21 | PRE_MERGE | account/security | login uses a missing/invalid username instead of a stored account | dummy bcrypt comparison uses the production cost so the generic 401 path does not expose a lower work factor |
| V22 | PRE_MERGE | account/security | login uses a disabled account with a validly shaped password | stored password hash is still compared before returning the generic 401 response |
| V23 | PRE_MERGE | knowledge/usecase | a caller invokes knowledge creation without the HTTP role middleware | the UseCase rejects an anonymous or non-admin principal before embedding or storage; an admin principal retains the approved write path |
| V24 | PRE_MERGE | HTTP/standard transport | fixed-length and chunked bodies are exactly at or one byte above the 1 MiB application limit; an oversized request uses `Expect: 100-continue`; a chunked body terminates early | exact-limit bodies reach the handler; oversized bodies return the standard 413 envelope and request ID then close; malformed streams return the standard 400 envelope and close |
| V25 | PRE_MERGE | HTTP/default netpoll | an oversized sender remains active while the server rejects the request, and rejected-stream cleanup is inspected with a recording connection | the 413 response and connection close occur before the declared body is drained; cleanup sets an absolute read deadline rather than a relative timeout |
| V26 | PRE_MERGE | HTTP/chat transport | REST or SSE chat declares or streams more than `MaxMessageLength + 1024` bytes | reject at the route-specific transport boundary with correlated 413 and connection close, including before `100 Continue` |
| V27 | PRE_MERGE | frontend/accessibility | async failures, streaming Assistant output, Commerce view selection, Community reactions, and Commerce loading failure layout | errors are alerts; only the current stream is a polite busy status; selected buttons expose `aria-pressed`; App and Commerce share one scroll owner |
| V28 | PRE_MERGE | PostgreSQL/time | claim/order waits on a lock while campaign or selected price crosses its exclusive end time | post-lock database time rejects the operation and no claim, stock increment, or order is committed |
| V29 | PRE_MERGE | frontend/auth | logout is activated twice in the same tick or overlaps another auth mutation | exactly one cookie mutation runs; both logout controls remain disabled while it owns the gate |
| V30 | PRE_MERGE | frontend/performance | production build with heavy Assistant Markdown/diagram renderer | renderer is lazy-loaded; module entry referenced by `dist/index.html` stays at or below 500 KiB |

Phase-one HTTP assertions use `contracts/phase1-http.md` as the wire source of
truth.

## Phase 2: Community

- Post/comment validation, ownership, admin hide, soft delete, missing game.
- Feed/game-feed keyset pagination under concurrent inserts.
- Reaction create/replay/delete and uniqueness under concurrency.
- HTTP contracts and frontend create/feed/detail/error flows.
- An older feed request finishing after a newer game filter cannot replace the
  filtered posts, cursor, loading state, or error state.
- An older detail request cannot replace a later selected post or reopen a
  detail after returning to the feed.
- An older comment page from a prior post cannot append comments or replace the
  cursor after another post opens.
- A completed comment submission from a prior post cannot append to or change
  the comment count of a later selected post.
- A completed comment deletion updates its original feed item, but cannot
  remove comments from or decrement a later selected post.
- A completed comment edit cannot close a comment editor or replace the error
  state of a later selected post.
- A reaction response may refresh its post in the feed, but cannot reopen a
  closed detail or replace a different selected post.
- A completed post edit may refresh its feed item, but cannot reopen a detail
  that the user closed while the save was pending.
- A completed post deletion removes its feed item, but cannot close a different
  post opened while the deletion was pending.
- A completed post creation remains in the feed, but cannot replace a post the
  user opened while the creation was pending or overwrite that detail's state.
- Repeated comment form submission while the first request is pending creates
  one comment request and exposes a disabled progress state.
- Community repository writes revalidate that the target post is published;
  moderation racing with a new comment or reaction cannot create engagement on
  hidden or deleted content.
- Comment listing validates parent visibility and reads published comments in one
  PostgreSQL statement: a published post with no comments returns an empty page,
  while a hidden or missing post returns `not_found` without a check/read race.
- Post JSON whose valid title/content reaches the rune limit through escaped
  surrogate pairs is accepted; one decoded rune above the limit is rejected.
- Feed/detail/comment/reaction completions remain bound to the initiating viewer
  and view generation. Closing and reopening the same post is a new generation,
  so an older reaction failure cannot leak into it, and one post cannot start a
  second reaction mutation while the first remains pending.
- Reaction buttons expose their selected state independently of color or CSS.

## Phase 3: Promotion And Commerce

- Discount entity boundary table: fixed, percentage basis points, minimum spend,
  currency mismatch, cap at subtotal, and zero total.
- Coupon inactive/not-started/expired/ineligible/exhausted/per-user-limit paths.
- Concurrent final-stock claims prove no oversell; same idempotency key returns
  one claim, different keys still respect per-user limits.
- Order uses server price, rejects client price/currency, stores snapshots, and
  rejects invalid transitions.
- Payment replay creates one payment, one redemption, and one entitlement.
- Concurrent payments for two pending orders of the same user and edition yield
  one paid order and one `already_owned` failure; the rejected order remains
  pending with no payment.
- Cross-user order access is forbidden; history pagination remains stable.
- Frontend Deals/checkout/orders/owned states and error recovery.
- Catalog summaries are resolved through the detail contract before the
  purchase list renders editions, prices, ownership, and checkout controls.
- Available unredeemed claims reload after navigation or refresh; claims already
  attached to an order are not offered for another checkout.
- A superseded deal or order-history request that fails after a newer filter or
  completed payment cannot replace the current success state with a stale error.
- An order-history failure is cleared when the user returns to the deals tab;
  errors from one commerce view cannot be presented as failures of another.
- An order-history request completed after the user leaves the orders tab cannot
  write a stale error into the deals view.
- A completed checkout consumes its captured coupon but cannot clear another
  coupon the user selected while that checkout was pending.
- A coupon claim completed after the game filter changes cannot restore deals
  from the filter captured when the claim started.
- Navigating away from a failed view clears that view's error instead of
  displaying it on the Catalog, Assistant, Account, or Commerce page.
- A reaction failure completed after the user leaves its post cannot surface
  an error in the community feed or a different post.
- An order transaction rejects a coupon quote when its campaign is paused or
  expires after quoting but before the order snapshot is written.
- An order transaction rejects a quoted edition price when the active catalog
  price changes or becomes unavailable before the order snapshot is written.
- The final order transaction locks and revalidates the current coupon definition
  as well as campaign, claim, edition, game, and regional price state. A changed
  discount or price rejects the stale quote without inserting an order, while an
  accepted order stores the locked catalog and money snapshots.
- Claim/order/payment idempotency keys survive leaving and re-entering Commerce
  so a retry after an ambiguous response reuses the original key; successful
  completion consumes its key, and an authenticated user change clears all keys.
- A coupon claim that succeeds after switching to Orders is accepted only for the
  same user. It does not mutate the inactive tab, and returning to Deals reloads
  authoritative claims so the new claim becomes selectable.
- Coupon claim revalidates campaign availability after acquiring its user and
  coupon locks. A campaign that expires while the claimant waits returns
  unavailable, inserts no claim, and does not increment `claimed_stock`.
- Commerce and its App-owned loading error share one `.page-stage` scroll owner;
  its selected view is exposed with button pressed state.

## Phase 4: Assistant

- Browser conversation history is partitioned by guest or trusted user ID;
  changing accounts hides the prior account's messages and cannot reuse its
  bound session ID.
- Client-supplied conversation IDs must be UUIDv4 values. Authenticated
  conversations bind to the trusted user ID; a guest conversation may be
  claimed on login, while another user or an anonymous request cannot reuse a
  bound conversation.
- Router structured direct/clarify/research decisions and deterministic fallback.
- Direct, clarify, and evidence answers retain bounded prior conversation
  context while excluding the current message from that history.
- Research one/multiple/refined tool calls, no result, partial failure, all
  failure, max iterations, max tool calls, deadline, and cancellation.
- Deterministic Knowledge/Web/Catalog/Forum input errors produce `invalid`
  observations without being counted as provider failures.
- Registry contains only read-only knowledge/catalog/forum/optional Web tools;
  mutation-like or unknown tool request is rejected.
- Retrieved knowledge, catalog, forum, and Web fields are quoted as untrusted
  data under a fixed Answer Node system rule; instructions inside evidence do
  not gain prompt authority.
- Every successful Research tool result caps each evidence content field at 800
  Unicode runes before it is stored in the run or returned to the model.
- Knowledge/Web queries are trimmed and reject blank or over-100-rune input
  before storage, embedding, or provider calls; malformed knowledge limits
  return 400 without downstream work.
- Web Search identifies its actual SearXNG adapter and does not expose a cache
  hit field or provider selector until those capabilities exist.
- SearXNG responses larger than 1 MiB fail as provider errors without being
  fully buffered or represented as successful evidence.
- Embedding responses are bounded in proportion to the requested input count;
  oversized provider payloads fail before JSON decoding and vector allocation.
- Answer direct/evidence/degraded/clarify/stream cases with citations; an empty
  model stream emits a fallback reply before EOF.
- Chat context excludes the current message while user-before-model and
  assistant-after-success persistence order remains intact; no partial
  assistant persistence occurs on failure.
- A non-abort stream failure replaces an empty provisional assistant message
  with an explicit failure state while retaining any partial response.
- REST and SSE Assistant failures extract the shared server error message and
  never expose the raw JSON error envelope as user-facing text.
- The SSE response exposes the canonical conversation ID. The browser parses
  event framing exactly once, rejects error events and successful empty streams,
  and preserves decoded assistant content that legitimately begins with `data:`.
- After a stopped chat is replaced by a new request, completion cleanup from
  the older stream cannot clear the newer stream's loading or cancellation state.
- Changing conversation/history cancels the owned stream and releases loading;
  late chunks and cleanup are scoped to the originating conversation/message.
- Only the latest in-progress Assistant response is a polite live status with
  `aria-busy=true`; completed and historical messages are not live regions.
- Citation titles collapse attacker-controlled whitespace and escape Markdown link
  delimiters; credential-bearing URLs remain omitted from rendered citations.
- Logs/traces exclude full messages, prompts, tokens, cookies, and passwords.
- Real model/Web calls remain ROLLOUT and never substitute for deterministic
  PRE_MERGE tests.

## Static And Build Gates

- `gofmt`, `go vet ./...`, all Go tests, relevant race tests.
- Architecture, spec-drift, link, placeholder, and diff checks.
- Frontend tests and production build.
- Docker image build after lifecycle/deployment changes.
- Search for Java/Maven runtime files; expected none.

## Recorded Verification — 2026-09-02

- Focused frontend: `perl -e 'alarm shift; exec @ARGV' 120 npm --prefix
  frontend/xiaolanhe-web test -- src/App.test.tsx
  src/components/ChatMessageList.test.tsx
  src/components/CommercePage.test.tsx
  src/components/CommunityPage.test.tsx src/lib/api.test.ts` — 5 files / 69
  tests PASS after the final accessibility review.
- Lock-expiry integration: `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 URL>
  go test -run '^TestProductPostgres$/(coupon_claim_rejects_a_campaign|order_create_rejects_a_(price|coupon_campaign))_that_expires_while_waiting_for_its_lock$'
  -count=1 -v ./internal/adapter/postgres` — 3/3 PASS.
- HTTP transport: `go test -run
  '^(TestHTTPOversizedRequestContract|TestHTTPStreamCancelsOnClientDisconnect)$'
  -count=1 -v ./internal/entry` — route-specific standard-transport body
  limits and default-netpoll disconnect cancellation PASS.
- Full local dirty-worktree gate: `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17
  URL> BASE_REF=e34338e585e5a3cdeaea784fac0b4f1ceb49d947 make ci` —
  formatting, vet, all Go tests, full race, 69 Vitest tests, hooks,
  architecture, spec drift, TypeScript, and Vite build PASS.
- Incremental coverage gate: SKIPPED because neither the repository nor the
  request defines a coverage threshold and this run is not a Flux report run.
- In-app visual browser smoke: NOT RUN because the local browser-control
  session could not initialize; DOM regressions and the production build passed,
  but no visual result is inferred.

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
