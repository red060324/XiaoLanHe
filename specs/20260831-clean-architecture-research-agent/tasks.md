# Tasks

Status: SUPERSEDED by `../20260831-mature-game-platform/tasks.md`
Authoritative spec: ./spec.md

| Class | Status | Task | Evidence |
|---|---|---|---|
| PRE_MERGE | DONE | Add architecture map, full development lifecycle, repo Skill, Make/CI gates and this spec set | `make ci` + reviewed files |
| PRE_MERGE | TODO | User approves spec, plan and test plan | explicit approval |
| PRE_MERGE | TODO | Add characterization tests for current direct, clarify and research behavior | test output |
| PRE_MERGE | TODO | Establish module-first Entry/Presenter/UseCase/Entity/Repository packages | import-boundary check |
| PRE_MERGE | TODO | Extract Router Node with structured output and fallback tests | unit tests |
| PRE_MERGE | TODO | Extract Answer Node with direct, evidence and streaming tests | unit tests |
| PRE_MERGE | TODO | Implement bounded read-only Research Agent with fake typed tools | Agent-loop tests |
| PRE_MERGE | TODO | Adapt existing knowledge and Web Search capabilities into Agent tools | adapter tests |
| PRE_MERGE | TODO | Model evidence, absence, degradation, budget and cancellation semantics | unit tests |
| PRE_MERGE | TODO | Remove fixed Research pipeline and misleading Agent naming | diff review |
| PRE_MERGE | TODO | Add trace fields and sensitive-data assertions | observability tests |
| PRE_MERGE | TODO | Update README and final report | docs review |
| ROLLOUT | TODO | Run direct/research/SSE smoke with PostgreSQL and real model credentials | rollout report |
| FOLLOW_UP | TODO | Add forum and catalog tools when their modules exist | separate spec |
| FOLLOW_UP | TODO | Evaluate Research Agent as a tool in a future Multi-Agent design | eval-backed spec |
| FOLLOW_UP | TODO | Add transactional Agent tools | separate security/product spec |

Any incomplete PRE_MERGE row blocks implementation handoff.
