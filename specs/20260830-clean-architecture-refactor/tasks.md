# Tasks

Authoritative spec: ./spec.md

| Class | Status | Task | Evidence |
|---|---|---|---|
| PRE_MERGE | DONE | Create codex/clean-architecture-refactor from master | remote branch |
| PRE_MERGE | DONE | Approve spec, plan and test plan | confirmed 2026-08-30 |
| PRE_MERGE | IN_PROGRESS | Characterize current Java REST/SSE, persistence order and fallbacks | static evidence + report; live Java fixtures pending |
| PRE_MERGE | DONE | Add the smallest runnable Go module and composition root | healthz test + Go build |
| PRE_MERGE | DONE | Implement chat Presenter and UseCase contracts | unit tests |
| PRE_MERGE | IN_PROGRESS | Implement PostgreSQL conversation adapter against current schema | adapter done; isolated DB integration pending |
| PRE_MERGE | IN_PROGRESS | Implement Eino orchestrator/direct-answer vertical slice | direct answer done; orchestrator pending |
| PRE_MERGE | TODO | Implement research retrieval, bounded fan-out and RRF | unit/integration tests |
| PRE_MERGE | TODO | Implement answer synthesis, optional verifier and persistence ordering | workflow tests |
| PRE_MERGE | DONE | Implement Hertz REST/SSE adapters and disconnect cancellation | API contract tests |
| PRE_MERGE | DONE | Add config validation, health, structured logs and node timings | config + health + disconnect tests |
| PRE_MERGE | IN_PROGRESS | Add CI, local run documentation and Java/Go comparison report | CI + README + report.md; live comparison pending |
| ROLLOUT | TODO | Start Java and Go in parallel against an isolated database snapshot | owner: user; trigger: PRE_MERGE green |
| ROLLOUT | TODO | Smoke test chat, stream, retrieval and fallback paths | rollout report |
| ROLLOUT | TODO | Switch default backend to Go with Java rollback available | human approval |
| FOLLOW_UP | TODO | Delete Java modules after stable soak period | owner: user; trigger: no parity regressions |
| FOLLOW_UP | TODO | Add benchmark/eval dataset and trace UI | separate spec |
| FOLLOW_UP | TODO | Add read-only game Skills and personalized Planning Agent | separate product spec |

Incomplete PRE_MERGE items block implementation handoff and cutover.
