# Current-State Audit

Date: 2026-08-31
Baseline: `codex/clean-architecture-refactor@f868c6b` plus uncommitted AI Coding harness

## Capability Matrix

| Capability | Evidence | State |
|---|---|---|
| Go backend | `cmd/xiaolanhe`, `internal/` | implemented |
| Chat REST/SSE | `/api/chat/message`, `/api/chat/stream` | implemented and tested |
| Conversation persistence | `conversation_session`, `conversation_message` | implemented; no user ownership enforcement |
| Knowledge ingestion/search | knowledge endpoints + pgvector | implemented; ingestion is anonymous |
| Web Search | SearXNG adapter | optional; failures are collapsed into successful notes |
| True Research Agent | fixed Plan -> Decompose -> parallel retrieval | not implemented |
| Login/account | unused account tables and no-op buttons | not implemented |
| Game catalog | no tables, UseCases, endpoints, or UI | not implemented |
| Community | no tables, UseCases, endpoints, or UI | not implemented |
| Coupons | no tables, transaction rules, endpoints, or UI | not implemented |
| Orders/payment/ownership | no tables, state machine, endpoints, or UI | not implemented |
| Deployment | Dockerfile + Render blueprint | implemented baseline; no migration versioning/readiness/graceful shutdown |

## Defect And Debt Map

| ID | Severity | Evidence | Required response |
|---|---|---|---|
| D1 | critical | `POST /api/knowledge/documents` has no authentication or authorization | require admin identity before write |
| D2 | high | SearXNG timeout, non-2xx, and invalid JSON return `nil` error | return typed provider failures and preserve partial-result semantics |
| D3 | high | application executes all of `001_initial_schema.sql` at every startup | ordered migrations with version table and advisory lock |
| D4 | high | client-selected conversation session key is not bound to a user | bind authenticated sessions and prevent cross-user access |
| D5 | medium | server only exposes static liveness and has no graceful shutdown | add readiness and signal-driven shutdown |
| D6 | medium | `XLH_AGENT_MODE` and `XLH_MINIO_BUCKET` are displayed but have no runtime behavior | remove dead configuration and response fields |
| D7 | medium | frontend Login/Register buttons have no action | implement auth flow or remove false affordance; selected: implement |
| D8 | medium | an in-flight stream cannot be aborted when changing/new chat | AbortController and explicit aborted/error message state |
| D9 | medium | frontend has no test command or component/API tests | add focused Vitest coverage when UI behavior changes |
| D10 | medium | repository-wide horizontal `internal/usecase` mixes assistant entities, ports, and orchestration | migrate per delivered module; do not big-bang move unrelated code |
| D11 | low | frontend production build reports several >500KB chunks | lazy-load heavy Markdown/diagram code after functional slices |
| D12 | verification | PostgreSQL/pgvector integration and real-model smoke remain unrun | add reproducible local DB and rollout evidence |

## Reusable Code

- Go/Hertz entry, REST/SSE compatibility, pgx pool, Eino model adapter, pgvector
  knowledge store, SearXNG HTTP test pattern, React shell, Docker multi-stage
  build, Render blueprint, and the new AI Coding harness.
- Current consumer-owned interfaces are useful evidence, but contracts move
  into their owning business module as each vertical slice is touched.

## Migration Debt, Not Precedent

- `internal/usecase/agent.go` labels a fixed workflow as Agent and combines
  entities, ports, concurrency, and ranking.
- Public knowledge/search endpoints are operational/debug surfaces, not a
  reviewed product API.
- Existing `user_account` and `player_profile` tables do not contain credential,
  role, status, or session lifecycle data and are not sufficient for login.
- The checked-in frontend `dist/` bundle is deployment output; source remains
  authoritative.

## Baseline Verification

- The existing full `make ci BASE_REF=origin/master` passed Go vet, all Go
  tests, race tests, architecture/spec hooks, and the frontend production build.
- The build retained the existing Vite large-chunk warning.
- No `.java` file was found.
- `go test -cover ./...` could not write its temporary coverage build output in
  the restricted sandbox; this is environment evidence, not a code failure.
