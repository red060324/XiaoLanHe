# Tasks

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | TODO | Human approves scope, policy defaults, design, and test plan | all | explicit approval |
| T1 | PRE_MERGE | TODO | Add strict public rate/concurrency configuration | AC2, AC6 | config tests |
| T2 | PRE_MERGE | TODO | Implement bounded concurrent token-bucket guard and 429 response | AC1-AC3, AC5 | unit + race tests |
| T3 | PRE_MERGE | TODO | Validate empty-or-canonical chat session IDs | AC4 | presenter/HTTP tests |
| T4 | PRE_MERGE | TODO | Protect registration and login without changing generic auth errors | AC1, AC3, AC5, AC7 | Account HTTP tests |
| T5 | PRE_MERGE | TODO | Protect REST/SSE chat, knowledge search, and Web Search with shared in-flight release | AC1, AC3, AC5, AC7 | HTTP/provider/cancellation tests |
| T6 | PRE_MERGE | TODO | Protect authenticated community/admin/promotion/order writes | AC1, AC3, AC5, AC7 | module HTTP tests |
| T7 | PRE_MERGE | TODO | Update public config/deployment docs and pass every repository gate | AC6, AC8 | local CI + GitHub Actions |
| T8 | ROLLOUT | TODO | Run isolated abuse/success smoke and configure provider cost alerts | AC3, AC7, AC8 | rollout report |
| T9 | FOLLOW_UP | TODO | Replace process-local quotas before horizontal scaling | excluded | trigger: second app instance |

Production code starts only after T0 is approved. Implementation order is
T1 -> T2 -> T3 -> T4 -> T5 -> T6 -> T7.
