# Test Plan

Status: APPROVED (2026-08-30)
Authoritative spec: ./spec.md

## PRE_MERGE

### Characterization

- Record current Java request/response for /api/chat/message.
- Record exact SSE wire format for /api/chat/stream, including normal completion, provider failure and client disconnect.
- Record database write order for user and assistant messages.
- Record routing fallback, embedding failure, LightRAG failure and Web Search disabled behavior.
- Freeze only observable behavior; do not snapshot unstable model prose as an exact string.

### Unit

- Presenter: nil/blank message, optional sessionId, DTO mapping.
- UseCase: success, provider failure, persistence failure, cancellation and no partial success.
- Route policy: direct, clarify, evidence and planner fallback.
- Retrieval: query cap, deterministic dedupe, RRF ordering, source absence, timeout and bounded concurrency.
- Memory: recent window and summary cursor behavior.
- Eino adapter: typed plan parsing, malformed output fallback, streaming close/error.
- No real network or model calls in unit tests; use consumer-owned fakes.

### Integration

- PostgreSQL/pgvector service container uses the existing V1-V3 migrations.
- Existing rows created by Java are readable by Go.
- Go writes remain readable by Java during the parallel phase.
- Keyword, vector and hybrid search cover empty, hit and dimension mismatch.
- LightRAG and Web Search use local stub HTTP servers for success, timeout, malformed payload and non-2xx.
- No new ORM/test framework is required; use Go testing, httptest and a CI PostgreSQL service.

### Interface

- POST /api/chat/message fields and status behavior match characterization.
- POST /api/chat/stream bytes/events match characterization.
- CORS behavior required by the existing Vite frontend remains valid.
- /healthz distinguishes process health from dependency readiness.
- Request size/message length limits reject before model invocation.

### E2E

Deterministic fake-model scenarios:

1. direct greeting
2. clarify ambiguous query
3. local knowledge answer
4. freshness query with Web Search enabled
5. Web Search disabled
6. LightRAG unavailable with pgvector/keyword fallback
7. planner malformed JSON fallback
8. synthesis failure
9. client disconnect during stream
10. existing session context continuation

Real-model smoke is ROLLOUT, not a merge gate, because output is nondeterministic and costs money.

### Static and Build

- gofmt check.
- go vet ./...
- go test ./...
- go test -race ./... for packages with workflow/stream concurrency.
- Frontend npm build unchanged.
- Java build remains green until Java removal is separately approved.

### Observability

- Assert route/node/provider/result/latency fields.
- Assert fallback reason on degraded paths.
- Assert secrets, full prompts and full user messages are absent from logs.
- Assert cancelled streams stop downstream work.

## ROLLOUT

- Run Java and Go against an isolated snapshot, not production data.
- Replay approved contract cases and compare status, schema, persistence and fallback.
- Measure first-token and total latency by route; record baseline and delta before cutover.
- Verify rollback by switching traffic back to Java without schema changes.

## FOLLOW_UP

- Real-model benchmark for route accuracy, Recall@5, citation fidelity, TTFT, P95 and token cost.
- Prompt injection and tool allowlist cases before adding Skills.
- Long-session summary cost and correctness evaluation.

## Exit Criteria

- Every PRE_MERGE test has an actual PASS/FAIL result.
- No unreviewed contract delta.
- No destructive migration.
- Race tests pass for streaming/retrieval concurrency.
- report.md lists executed, skipped and blocked checks.
