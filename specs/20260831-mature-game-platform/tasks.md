# Tasks

- Status: `PHASE_3_IN_PROGRESS`
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
| T11 | PRE_MERGE | IN_PROGRESS | Implement order, sandbox payment, redemption, and entitlement | AC6 | state/idempotency/integration tests pending |
| T12 | PRE_MERGE | TODO | Add Deals/checkout/orders/ownership UI | AC5, AC6 | frontend tests + build |
| T13 | PRE_MERGE | TODO | Implement Router/Answer Nodes and bounded Research Agent | AC7, AC8 | deterministic Agent-loop tests |
| T14 | PRE_MERGE | TODO | Add knowledge/catalog/forum/Web read-only tools and citations | AC7 | tool/contract tests |
| T15 | PRE_MERGE | TODO | Complete docs, API contracts, readiness report, and full CI | AC10 | report + `make ci` |
| T16 | ROLLOUT | TODO | Run isolated PostgreSQL migrations and product smoke | AC9, AC10 | rollout report |
| T17 | ROLLOUT | TODO | Run approved real-model/Web smoke and inspect safe traces | AC7, AC8 | rollout report |
| T18 | FOLLOW_UP | TODO | Add real payment provider, refund, tax, and abuse controls | excluded | separate security/product spec |
| T19 | FOLLOW_UP | TODO | Add transactional Agent tools after ordinary commerce is proven | excluded | separate approval/idempotency spec |

Implementation proceeds by completed vertical slice. T3-T7 form Phase 1;
T8-T9 form Phase 2. Phase 3 started after revision `3ae736a` passed GitHub
Actions against PostgreSQL.
