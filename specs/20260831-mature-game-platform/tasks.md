# Tasks

- Status: `VERIFYING`
- Authoritative spec: `./spec.md`

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | DONE | Human approves spec, plan, data model, phase order, and test plan | all | approved 2026-08-31 |
| T1 | PRE_MERGE | DONE | Audit current capabilities and defects | AC10 | `research.md` |
| T2 | PRE_MERGE | DONE | Install AI Coding harness and canonical CI gates | AC1, AC10 | `make ci` |
| T3 | PRE_MERGE | DONE | Add ordered migration runner and foundation lifecycle fixes | AC8, AC9 | fresh/repeat/concurrent/checksum PostgreSQL CI PASS |
| T4 | PRE_MERGE | DONE | Implement Account register/login/logout/me and authorization | AC2 | unit + HTTP + PostgreSQL session CI PASS |
| T5 | PRE_MERGE | DONE | Implement Catalog read/admin APIs and demo seed command | AC3 | unit + HTTP + PostgreSQL price/ownership/seed CI PASS |
| T6 | PRE_MERGE | DONE | Add frontend auth/catalog navigation and tests | AC2, AC3 | 6 Vitest PASS + production build PASS |
| T7 | PRE_MERGE | DONE | Fix anonymous knowledge write, Web Search failure semantics, dead config, and cancellable chat UI | AC2, AC8 | Go/HTTP/frontend regression PASS |
| T8 | PRE_MERGE | DONE | Implement Community posts/comments/reactions/feed | AC4 | entity/UseCase/HTTP/PostgreSQL tests + `contracts/phase2-http.md` |
| T9 | PRE_MERGE | DONE | Add Community UI and ownership/error states | AC4 | 13 Vitest tests + production build |
| T10 | PRE_MERGE | DONE | Implement atomic idempotent coupon campaign/claim | AC5 | local `make ci` + GitHub Actions `33352631674` PASS |
| T11 | PRE_MERGE | DONE | Implement order, sandbox payment, redemption, and entitlement | AC6 | local `make ci` + GitHub Actions `33354207598` PASS |
| T12 | PRE_MERGE | DONE | Add Deals/checkout/orders/ownership UI | AC5, AC6 | 21 frontend tests + local `make ci` + GitHub Actions `33354854151` PASS |
| T13 | PRE_MERGE | DONE | Implement Router/Answer Nodes and bounded Research Agent | AC7, AC8 | deterministic Agent-loop tests + local `make ci` + GitHub Actions `33356038295` PASS |
| T14 | PRE_MERGE | DONE | Add knowledge/catalog/forum/Web read-only tools and citations | AC7 | tool/contract/PostgreSQL tests + local `make ci` + GitHub Actions `33356971706` PASS |
| T15 | PRE_MERGE | DONE | Complete docs, API contracts, readiness report, and full CI | AC10 | public deployment guide + local `make ci` + GitHub Actions `33357654243` PASS |
| T16 | ROLLOUT | DONE | Run isolated PostgreSQL migrations and product smoke | AC9, AC10 | repeat seed + full account/admin catalog/community/coupon/order/payment/entitlement container smoke; GitHub Actions `33378359262` PASS |
| T17 | ROLLOUT | TODO | Run approved real-model/Web smoke and inspect safe traces | AC7, AC8 | rollout report |
| T18 | FOLLOW_UP | TODO | Add real payment provider, refund, and tax | excluded | abuse protection moved to `../20260831-public-rollout-hardening/`; payment stays a separate spec |
| T19 | FOLLOW_UP | TODO | Add transactional Agent tools after ordinary commerce is proven | excluded | separate approval/idempotency spec |
| T20 | PRE_MERGE | DONE | Correct chat log correlation to use the validated request ID | AC10 | failing-then-passing REST/SSE regression tests |
| T21 | PRE_MERGE | DONE | Enforce one bounded query contract across HTTP and Research tools | AC7, AC8 | failing-then-passing UseCase/HTTP tests + GitHub Actions `33360429384` PASS |
| T22 | PRE_MERGE | DONE | Classify deterministic Research tool-input errors separately from provider failures | AC7, AC8 | failing-then-passing Agent adapter test + GitHub Actions `33361284676` PASS |
| T23 | PRE_MERGE | DONE | Prevent successful empty model streams from producing empty assistant replies | AC8 | failing-then-passing SSE Answer adapter test + GitHub Actions `33362087122` PASS |
| T24 | PRE_MERGE | DONE | Prevent two orders for one user and edition from both completing payment | AC6 | GitHub Actions `33362876531` failed before fix; `33363326436` PASS after fix |
| T25 | PRE_MERGE | DONE | Remove Web Search cache/provider surfaces that have no implementation | AC1, AC8 | failing-then-passing adapter/HTTP tests + GitHub Actions `33364425654` PASS |
| T26 | PRE_MERGE | DONE | Keep the current chat message out of historical conversation context | AC7, AC8 | failing-then-passing Chat UseCase test + local `make ci` + GitHub Actions `33365566954` PASS |
| T27 | PRE_MERGE | DONE | Deactivate prices omitted from a complete Catalog aggregate update | AC3, AC9 | GitHub Actions `33366323928` failed before fix; local `make ci` and `33366575106` PASS after fix |
| T28 | PRE_MERGE | DONE | Replace an empty assistant placeholder when a non-abort chat stream fails | AC8 | failing-then-passing frontend regression + local `make ci` + GitHub Actions `33367744541` PASS |
| T29 | PRE_MERGE | DONE | Preserve edition-level ownership in Catalog and Commerce | AC3, AC6 | failing-then-passing frontend regression + HTTP/PostgreSQL coverage + local `make ci` + GitHub Actions `33369003504` PASS |
| T30 | PRE_MERGE | DONE | Bind authenticated conversations to their owner and reject predictable session keys | AC2, AC7, AC8 | failing-then-passing Presenter regression + UseCase/HTTP/PostgreSQL coverage + local `make ci` + GitHub Actions `33370520530` PASS |
| T31 | PRE_MERGE | DONE | Isolate browser conversation history when the authenticated account changes | AC2, AC8 | failing-then-passing account-switch regression + local `make ci` + GitHub Actions `33371992090` PASS |
| T32 | PRE_MERGE | DONE | Prevent a stale initial identity lookup from overwriting a completed authentication action | AC2, AC8 | failing-then-passing auth-race regression + local `make ci` + GitHub Actions `33373433217` PASS |
| T33 | PRE_MERGE | DONE | Restore a user's available coupon claims after navigation or refresh | AC5, AC6 | failing-then-passing frontend remount/API, UseCase/HTTP/PostgreSQL reservation tests; local `make ci`; GitHub Actions `33377050477` PASS |
| T34 | PRE_MERGE | DONE | Prevent stale catalog and community reads from overwriting newer search/filter state | AC3, AC4, AC10 | failing-then-passing controlled-response frontend regressions; 29 Vitest tests + production build PASS |
| T35 | PRE_MERGE | DONE | Keep the latest community detail authoritative and prevent duplicate comment submission | AC4, AC10 | failing-then-passing controlled-response and pending-submit frontend regressions; 31 Vitest tests + production build PASS |
| T36 | PRE_MERGE | DONE | Ignore stale Commerce read failures after newer filter or payment state | AC5, AC6, AC10 | failing-then-passing controlled-rejection frontend regression; local full `make ci` PASS with 34 Vitest tests |
| T37 | PRE_MERGE | DONE | Prevent an older comment page from being appended after another post opens | AC4, AC10 | failing-then-passing controlled-response frontend regression; local full `make ci` PASS with 34 Vitest tests |
| T38 | PRE_MERGE | DONE | Keep a completed reaction request from reopening a closed or replaced post detail | AC4, AC10 | failing-then-passing controlled-response frontend regression; local full `make ci` PASS with 34 Vitest tests |
| T39 | PRE_MERGE | IN_PROGRESS | Publish the latest audited revision and verify clean-checkout GitHub Actions | AC10 | local full `make ci` PASS; exact remote push authorization and latest GitHub Actions run pending |
| T40 | PRE_MERGE | DONE | Ignore an abandoned game-detail response after navigation | AC3, AC10 | failing-then-passing controlled-response frontend regression; local full `make ci` PASS with 35 Vitest tests |
| T41 | PRE_MERGE | DONE | Keep a completed comment submission scoped to its original post | AC4, AC10 | failing-then-passing controlled-response frontend regression; local full `make ci` PASS with 36 Vitest tests |
| T42 | PRE_MERGE | DONE | Preserve the active game filter when a prior coupon claim completes | AC5, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 37 Vitest tests |
| T43 | PRE_MERGE | DONE | Keep a completed post edit from reopening an abandoned detail | AC4, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 38 Vitest tests |
| T44 | PRE_MERGE | DONE | Keep a completed post deletion from closing a newer detail | AC4, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 39 Vitest tests |
| T45 | PRE_MERGE | DONE | Keep a completed comment deletion scoped to its original post | AC4, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 40 Vitest tests |
| T46 | PRE_MERGE | DONE | Keep a completed comment edit scoped to its original post | AC4, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 41 Vitest tests |
| T47 | PRE_MERGE | DONE | Keep the latest explicit authentication attempt authoritative | AC2, AC8, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 42 Vitest tests |
| T48 | PRE_MERGE | DONE | Preserve bounded multi-turn context for direct and clarify answers | AC7, AC10 | failing-then-passing AssistantFlow regression; full local `make ci BASE_REF=origin/master` passed |
| T49 | PRE_MERGE | DONE | Hydrate catalog summaries before exposing purchase controls | AC3, AC6, AC10 | failing-then-passing real-contract frontend regression; full local `make ci BASE_REF=origin/master` passed with 43 Vitest tests |
| T50 | PRE_MERGE | DONE | Keep a completed post creation from replacing a newer selected detail | AC4, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 44 Vitest tests |
| T51 | PRE_MERGE | DONE | Keep an older chat stream cleanup from disabling a newer stream | AC7, AC8, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 45 Vitest tests |
| T52 | PRE_MERGE | DONE | Preserve a newer coupon selection when an earlier checkout completes | AC6, AC10 | failing-then-passing controlled-response frontend regression; full local `make ci BASE_REF=origin/master` passed with 46 Vitest tests |
| T53 | PRE_MERGE | DONE | Revalidate coupon campaign eligibility at the final order transaction boundary | AC5, AC6, AC10 | `TestProductPostgres` passed locally against isolated PostgreSQL 17 + pgvector 0.8.6; full local `make ci BASE_REF=origin/master` PASS |
| T54 | PRE_MERGE | DONE | Prevent a failed view from leaking its error into another page | AC8, AC10 | failing-then-passing Assistant-to-Catalog regression; full local `make ci BASE_REF=origin/master` passed with 47 Vitest tests |
| T55 | PRE_MERGE | DONE | Ignore a stale reaction failure after the user leaves its post | AC4, AC8, AC10 | failing-then-passing controlled-rejection frontend regression; full local `make ci BASE_REF=origin/master` passed with 48 Vitest tests |
| T56 | PRE_MERGE | DONE | Prevent an order-history error from leaking into the deals tab | AC5, AC6, AC10 | failing-then-passing Commerce tab regression; full local `make ci BASE_REF=origin/master` passed with 49 Vitest tests |
| T57 | PRE_MERGE | DONE | Ignore an order-history failure completed after leaving the orders tab | AC5, AC6, AC10 | failing-then-passing controlled-rejection regression; full local `make ci BASE_REF=origin/master` passed with 50 Vitest tests |
| T58 | PRE_MERGE | DONE | Revalidate public post status at final comment and reaction writes | AC4, AC10 | failing-then-passing PostgreSQL Store regressions; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T59 | PRE_MERGE | DONE | Revalidate the active edition price at the final order transaction boundary | AC3, AC6, AC10 | failing-then-passing PostgreSQL Store regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T60 | PRE_MERGE | DONE | Treat Catalog name/slug searches as literal substrings | AC3, AC10 | failing-then-passing PostgreSQL Store regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T61 | PRE_MERGE | DONE | Bound every knowledge chunk when one paragraph exceeds the target size | AC7, AC10 | failing-then-passing `TestChunkText` regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T62 | PRE_MERGE | DONE | Treat knowledge keyword searches as literal substrings | AC7, AC10 | failing-then-passing PostgreSQL Store regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T63 | PRE_MERGE | DONE | Map request deadlines to the shared HTTP 504 contract across product modules | AC8, AC10 | failing-then-passing Assistant HTTP regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T64 | PRE_MERGE | DONE | Propagate request cancellation and deadlines from knowledge embedding | AC7, AC8, AC10 | failing-then-passing Knowledge UseCase regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T65 | PRE_MERGE | DONE | Equalize missing-account and real-account bcrypt work factors | AC2, AC10 | failing-then-passing Account Login regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |
| T66 | PRE_MERGE | DONE | Preserve password work on disabled-account login failures | AC2, AC10 | failing-then-passing Account Login regression; full local `make ci BASE_REF=origin/master` passed against PostgreSQL 17 + pgvector with 50 Vitest tests |

Implementation proceeds by completed vertical slice. T3-T7 form Phase 1;
T8-T9 form Phase 2; T10-T12 form Phase 3. Phase 4 started after Research Agent
revision `41847fe` passed GitHub Actions against PostgreSQL.
