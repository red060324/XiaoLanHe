# Tasks

Authoritative spec: ./spec.md

| Class | Status | Task | Evidence |
|---|---|---|---|
| PRE_MERGE | DONE | Create codex/clean-architecture-refactor from master | remote branch |
| PRE_MERGE | DONE | Approve spec, plan and test plan | confirmed 2026-08-30 |
| PRE_MERGE | DONE | Characterize legacy REST/SSE, persistence order and fallbacks | static evidence + contract tests |
| PRE_MERGE | DONE | Add the smallest runnable Go module and composition root | healthz test + Go build |
| PRE_MERGE | DONE | Implement chat Presenter and UseCase contracts | unit tests |
| PRE_MERGE | IN_PROGRESS | Implement PostgreSQL conversation adapter against current schema | adapter done; isolated DB integration pending |
| PRE_MERGE | DONE | Implement Eino orchestrator/direct-answer vertical slice | planner fallback + adapter tests |
| PRE_MERGE | DONE | Implement research retrieval, bounded fan-out and RRF | unit tests |
| PRE_MERGE | DONE | Implement answer synthesis and persistence ordering | workflow tests; verifier remains intentionally disabled |
| PRE_MERGE | DONE | Implement Hertz REST/SSE adapters and disconnect cancellation | API contract tests |
| PRE_MERGE | DONE | Add config validation, health, structured logs and node timings | config + health + disconnect tests |
| PRE_MERGE | DONE | Add CI, local run documentation and legacy/Go contract report | CI + README + report.md |
| ROLLOUT | TODO | Smoke test chat, stream, retrieval and fallback paths | rollout report |
| PRE_MERGE | DONE | Migrate knowledge, web search and system APIs to Go | contract tests |
| PRE_MERGE | DONE | Move SQL/prompts/config ownership to Go paths | clean startup/config check |
| PRE_MERGE | DONE | Delete Java modules and Maven build after parity checks | no .java/pom.xml remains |
| ROLLOUT | TODO | Deploy the Go backend with Git rollback available | human approval |
| FOLLOW_UP | TODO | Add benchmark/eval dataset and trace UI | separate spec |
| FOLLOW_UP | TODO | Add read-only game Skills and personalized Planning Agent | separate product spec |

Incomplete PRE_MERGE items block implementation handoff and cutover.
