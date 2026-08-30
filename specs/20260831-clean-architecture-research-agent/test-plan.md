# Test Plan

Status: SUPERSEDED by `../20260831-mature-game-platform/test-plan.md`
Authoritative spec: ./spec.md

## PRE_MERGE Unit And Agent Tests

- Router Node: direct, clarify, research, malformed structured output, timeout
  and deterministic fallback.
- Research Agent: one tool, multiple sequential tools, refined query, no
  evidence, partial failure, all failure, maximum iterations, maximum tool
  calls, total deadline and cancellation.
- Tool allowlist: only read-only tools are registered; unknown and mutation-like
  tool requests are rejected.
- Answer Node: direct answer, evidence answer, degraded evidence, clarification,
  stream EOF and stream error.
- Entities: evidence identity, citation preservation, deterministic fusion and
  explicit absence/error semantics.
- Chat UseCase: persistence order, no partial assistant persistence, request
  cancellation and provider failure.

Use deterministic fake model events and fake tools. Do not call a paid model or
the public internet in unit tests.

## PRE_MERGE Integration And Interface

- PostgreSQL conversation and knowledge adapters against the existing schema.
- SearXNG via `httptest` for success, timeout, malformed JSON and non-2xx.
- Existing `POST /api/chat/message` response contract remains compatible.
- Existing `POST /api/chat/stream` event order, EOF and disconnect cancellation
  remain compatible.
- Disabled Web Search produces no outbound call.

## PRE_MERGE Architecture And Static Checks

- `gofmt` and `git diff --check`.
- `go vet ./...`.
- `go test ./...`.
- `go test -race` for Agent, streaming and concurrent retrieval packages.
- Import check: public UseCase/Entity packages do not import Hertz, Eino, pgx or
  provider DTO packages.
- Search Agent tool registry for coupon, order, payment and write capabilities;
  expected result is none.
- `make architecture` and `make spec-drift BASE_REF=origin/master` execute
  successfully; CI calls the same canonical Make targets.

## PRE_MERGE Observability And Security

- Every Research run reports stop reason, iteration/tool counts and latency.
- Provider failures and degraded answers are distinguishable.
- Logs/metrics do not contain full prompt, full user message, API key, session
  ID or high-cardinality user identifiers.
- Model arguments cannot override trusted identity or provider endpoints.

## ROLLOUT

- Real-model smoke: direct, clarify, local research, Web research and streaming.
- Record route accuracy, tool selection, citation validity, first-token time,
  total latency, model/tool call counts and token usage.
- Compare the fixed pipeline and Research Agent on an approved question set
  before deleting rollback code.

## Exit Criteria

- All PRE_MERGE tasks and checks have actual PASS evidence.
- No public API or destructive schema delta.
- Agent cannot access a mutation capability.
- Real-model checks remain explicitly ROLLOUT and are never reported as unit
  verification.
