# Tasks

Authoritative spec: ./spec.md

| Class | Status | Task | Evidence |
|---|---|---|---|
| PRE_MERGE | DONE | Create codex/clean-architecture-refactor from master | remote branch |
| PRE_MERGE | IN_REVIEW | Approve spec, plan and test plan | user confirmation |
| PRE_MERGE | TODO | Characterize current Java REST/SSE, persistence order and fallbacks | contract fixtures/report |
| PRE_MERGE | TODO | Add the smallest runnable Go module and composition root | healthz + startup test |
| PRE_MERGE | TODO | Implement chat Presenter and UseCase contracts | unit tests |
| PRE_MERGE | TODO | Implement PostgreSQL conversation adapter against current schema | integration tests |
| PRE_MERGE | TODO | Implement Eino orchestrator/direct-answer vertical slice | fake-model tests |
| PRE_MERGE | TODO | Implement research retrieval, bounded fan-out and RRF | unit/integration tests |
| PRE_MERGE | TODO | Implement answer synthesis, optional verifier and persistence ordering | workflow tests |
| PRE_MERGE | TODO | Implement Hertz REST/SSE adapters and disconnect cancellation | API contract tests |
| PRE_MERGE | TODO | Add config validation, health, structured logs and node timings | startup/observability tests |
| PRE_MERGE | TODO | Add CI, local run documentation and Java/Go comparison report | CI link + report.md |
| ROLLOUT | TODO | Start Java and Go in parallel against an isolated database snapshot | owner: user; trigger: PRE_MERGE green |
| ROLLOUT | TODO | Smoke test chat, stream, retrieval and fallback paths | rollout report |
| ROLLOUT | TODO | Switch default backend to Go with Java rollback available | human approval |
| FOLLOW_UP | TODO | Delete Java modules after stable soak period | owner: user; trigger: no parity regressions |
| FOLLOW_UP | TODO | Add benchmark/eval dataset and trace UI | separate spec |
| FOLLOW_UP | TODO | Add read-only game Skills and personalized Planning Agent | separate product spec |

Incomplete PRE_MERGE items block implementation handoff and cutover.
