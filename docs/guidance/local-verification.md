# Local And CI Verification

Run from the repository root. `Makefile` is the canonical command interface.

## Commands

| Command | Purpose |
|---|---|
| `make fmt-check` | Verify Go formatting without rewriting files |
| `make vet` | Go static analysis |
| `make test` | Go unit/integration tests that need no external service |
| `make test-race` | Race detector for concurrent/streaming/Agent work |
| `make web-test` | Frontend Vitest behavior tests |
| `make web-build` | TypeScript and Vite production build |
| `make architecture` | Clean Architecture import boundary check |
| `make spec-drift BASE_REF=origin/master` | Require spec/docs evidence for risky diffs |
| `make verify` | Normal local Go/frontend/architecture gate |
| `make ci BASE_REF=origin/master` | Full repository CI gate |
| `make docker-build` | Deployment image build when deployment files change |

Use an exact package/test first during development:

```bash
go test ./internal/assistant/... -run '^TestName$' -count=1
```

The final pre-merge signal is `make ci` plus GitHub Actions on a clean Linux
checkout.

## Change-To-Check Matrix

| Change | Minimum evidence |
|---|---|
| Docs only | `git diff --check`, relevant link/manual review |
| Go behavior | exact test, affected package, `make verify` |
| Agent/stream/concurrency | deterministic loop tests, cancellation, `make test-race` |
| HTTP/SSE contract | presenter/handler tests and wire-level case |
| PostgreSQL/schema | migration on clean DB, compatibility/constraint cases |
| Provider adapter | local stub success/error/timeout/malformed cases |
| Frontend | affected test when present and `make web-build` |
| Docker/deploy | `make docker-build`, health and rollback plan |
| Real model/search | ROLLOUT smoke only; record cost-bearing credentials/environment |

## Agent Verification

Unit and pre-merge tests must use deterministic fake model events and fake
tools. Cover tool selection, observation, refined queries, stop conditions,
partial failure, all failure, timeout and cancellation.

Real-model output is nondeterministic and cannot replace unit tests. Record
route accuracy, tool calls, citation validity, latency, token usage and stop
reason during rollout smoke.

## Result Classification

| Outcome | Classification |
|---|---|
| Named tests executed and passed | PASS |
| Assertion/panic after execution | CODE/TEST FAILURE |
| No matching tests or only compilation | UNVERIFIED |
| Missing database/provider/credential | ENVIRONMENT BLOCKED |
| User explicitly skips a relevant check | SKIPPED WITH RISK |

Exit code zero without an executed test is not a test pass. Record exact
commands, executed tests, elapsed time when relevant and the first stable
failure.

## External Services

Do not use production data or paid providers for routine unit tests. Before a
shared-environment write, paid model smoke, deployment or destructive migration,
show the target, payload/action, expected side effect and rollback, then obtain
human approval.

## Handoff Evidence

Report separately:

- local unit/static result
- GitHub Actions result
- integration/interface result
- rollout/real-provider result
- skipped or blocked checks and residual risk
