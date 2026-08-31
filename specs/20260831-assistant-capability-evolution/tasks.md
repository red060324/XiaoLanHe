# Tasks

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | TODO | Human approves scope, single-Agent design, storage, API, rollout, and test plan | all | explicit approval |
| T1 | PRE_MERGE | TODO | Capture current Router/Research baseline cases and metrics schema | AC8 | deterministic baseline report |
| T2 | PRE_MERGE | TODO | Move touched Assistant code into its module-first boundary without contract drift | AC9, AC10 | import/static and regression tests |
| T3 | PRE_MERGE | TODO | Implement typed Query Planner and versioned declarative Skills | AC1-AC3 | Node/Skill validation and fallback tests |
| T4 | PRE_MERGE | TODO | Implement deterministic evidence fusion and provenance | AC4, AC9 | ranking/dedup/citation tests |
| T5 | PRE_MERGE | TODO | Add versioned summary, recent-window context, and explicit profile APIs/UI | AC5, AC6, AC9 | unit/HTTP/PostgreSQL/frontend tests |
| T6 | PRE_MERGE | TODO | Add graph schema, admin extraction, graph search, and hybrid retrieval | AC4, AC7 | migration/extractor/retrieval/PostgreSQL tests |
| T7 | PRE_MERGE | TODO | Complete eval comparisons, safe observability, docs, and repository gates | AC8, AC10 | local CI + public GitHub Actions |
| T8 | ROLLOUT | TODO | Run isolated migration/product/eval smoke with demo knowledge | AC7-AC10 | rollout report |
| T9 | ROLLOUT | TODO | Run approved real-model and optional Web eval; inspect safe traces/cost | AC1-AC5, AC8 | versioned aggregate report |
| T10 | FOLLOW_UP | TODO | Add adaptive Supervisor/Worker Agents | excluded | trigger: approved eval target fails for separable complex questions |
| T11 | FOLLOW_UP | TODO | Move extraction to a background worker | excluded | trigger: synchronous admin extraction exceeds approved latency/volume |

Implementation order is T1 -> T2 -> T3 -> T4 -> T5 -> T6 -> T7. Production
code starts only after T0 is approved.
