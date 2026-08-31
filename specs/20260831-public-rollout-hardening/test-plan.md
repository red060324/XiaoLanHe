# Test Plan

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

## Scope And Environments

Use deterministic clocks and local fakes for PRE_MERGE tests. No real model,
search provider, shared environment, or paid service is used before rollout.
Run exact tests first, then affected packages, `make ci`, container smoke, and
public GitHub Actions.

## Cases

| ID | Class | Layer | Scenario | Expected result | Command/evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | config | defaults, min/max, malformed, overflow | valid settings load; invalid settings fail startup | config exact tests | TODO |
| V2 | PRE_MERGE | unit | keyed token refill/burst and global backstop | deterministic allow/reject/retry decisions | httplimit exact tests | TODO |
| V3 | PRE_MERGE | unit | 10,000 rotating keys, idle cleanup, capacity fallback | memory stays bounded; global guard remains active | httplimit exact tests | TODO |
| V4 | PRE_MERGE | race | concurrent checks, cleanup, reset, and permits | no race, deadlock, over-release, or over-admission | targeted `go test -race` | TODO |
| V5 | PRE_MERGE | contract | rejected request | 429 + `Retry-After` + shared error envelope + request ID | HTTP tests | TODO |
| V6 | PRE_MERGE | auth | repeated register/login, normalized equivalent usernames, success reset | threshold enforced; generic credential behavior retained; downstream suppressed | Account HTTP/fake call counts | TODO |
| V7 | PRE_MERGE | presenter | empty, canonical UUID, overlong/arbitrary session ID | empty/canonical accepted; invalid returns 400 before store/model | presenter + HTTP tests | TODO |
| V8 | PRE_MERGE | Assistant | REST/SSE per-session/global limit | below-limit compatibility; rejection before conversation/model | HTTP/fake call counts | TODO |
| V9 | PRE_MERGE | lifecycle | provider error, EOF, and client disconnect | in-flight permit released exactly once on every path | REST/SSE tests | TODO |
| V10 | PRE_MERGE | provider | knowledge/Web global limit and shared concurrency cap | embedding/search calls bounded and suppressed on rejection | HTTP/provider fakes | TODO |
| V11 | PRE_MERGE | authorization | community/admin/coupon/order writes | Principal user key enforced after auth; forbidden/anonymous behavior unchanged | module HTTP tests | TODO |
| V12 | PRE_MERGE | idempotency | repeated claim/order/payment within allowed budget | original replay contract unchanged | module HTTP/usecase regression | TODO |
| V13 | PRE_MERGE | observability | all rejection reasons/capacity fallback | safe fields present; no raw username/session/cookie/message/IP | captured structured logs | TODO |
| V14 | PRE_MERGE | frontend | API 429 response | existing UI leaves loading state and shows server message | Vitest | TODO |
| V15 | PRE_MERGE | repository | Go-only/public-only, static, tests, race, frontend, architecture, build | all gates pass; no Java/private artifacts | `make ci BASE_REF=origin/master` | TODO |
| V16 | PRE_MERGE | container | default config, health/readiness, ordinary flow | image starts and healthy flow remains usable | GitHub container smoke | TODO |
| V17 | ROLLOUT | smoke | isolated threshold and recovery | exact 429 point, retry recovery, safe logs, no provider call after reject | rollout report | TODO |
| V18 | ROLLOUT | operations | provider spending limit and billing alert | external cost ceiling configured and recorded | provider dashboard evidence | TODO |

## Not Applicable

- No migration/PostgreSQL invariant case: this slice adds no persistent state.
- No Agent decision/eval change: Router, Research Agent, Answer Node, prompts,
  tools, and budgets are unchanged. Only admission before the Assistant runs is
  added.
- No multi-instance consistency case: horizontal scaling is an explicit
  FOLLOW_UP trigger, not a claimed capability.

## Exit Criteria

All V1-V16 cases have executed PASS evidence, every limited request proves zero
downstream side effects, and no critical/high security defect remains in this
slice. V17-V18 stay explicit rollout work and cannot be reported as run without
the target and provider evidence.
