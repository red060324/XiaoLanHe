# Technical Plan

- Status: `IMPLEMENTED — PRE_MERGE ENVIRONMENT VERIFICATION BLOCKED`
- Authoritative spec: `./spec.md`

## Selected Runtime Design

```text
Browser -> XiaoLanHe Go HTTP/SSE
  -> Assistant UseCase
     -> Context Builder
        -> rolling summary + recent eight messages + explicit profile projection
     -> Router Node -> route + Skill ID/version
        DIRECT/CLARIFY -> Answer Node
        RESEARCH/PLANNING
          -> Query Planner Node -> validated QueryPlan or safe fallback
          -> Game Copilot Agent (bounded supervisor)
             -> Research Agent task(s)
                -> search_lightrag -> private official LightRAG Server
                -> search_catalog / search_forum / optional search_web
             -> Planning Agent task(s), only when the Skill permits
                -> read_catalog / read_entitlements / score_constraints
             -> validates and combines worker artifacts
          -> Answer Node with validated evidence/plan artifact
     -> persist successful Assistant message

Admin knowledge request -> XiaoLanHe auth/validation facade
  -> direct official LightRAG document API
     create -> track ID -> status -> official document ID
     list/status/delete -> official LightRAG storage only

LightRAG Server (official pinned Python container; one service replica)
  -> WORKSPACE=xiaolanhe_v1 under persistent WORKING_DIR volume
     -> JsonKVStorage
     -> NanoVectorDBStorage
     -> NetworkXStorage
     -> JsonDocStatusStorage
```

The browser never reaches LightRAG. XiaoLanHe owns authorization, request budgets,
Agent orchestration, memory and final citations. LightRAG is the sole knowledge-data
owner in advanced mode: document text/status, chunks, vectors, graph and retrieval
indexes are not duplicated into XiaoLanHe's PostgreSQL.

## Node And Agent Classification

- `RouterNode`: one structured model call choosing route, intent and Skill.
- `QueryPlannerNode`: one structured model call producing bounded query units.
- `SummaryNode`: one bounded model call producing a rolling summary.
- `AnswerNode`: one generation/streaming call over validated artifacts.
- `GameCopilotAgent`: model-controlled supervisor loop over typed worker delegates.
- `ResearchAgent`: model-controlled loop over Skill-scoped read-only search tools.
- `PlanningAgent`: model-controlled loop over Skill-scoped read-only decision tools.
- Context building, schema/evidence validation, budgets and deterministic scoring are
  capabilities, not Agents.

Workers cannot delegate, Game Copilot cannot delegate to itself, and a request-level
ledger bounds all nested work.

## Agent Contracts And Minimum Context

All envelopes carry `schemaVersion=1`, server-generated `runId`, monotonic `sequence`
and `skillId/skillVersion`. Model output never controls identity, permissions,
budgets, allowed tools/delegates, LightRAG credentials or endpoints. Exact shapes are
in `contracts/agent-contracts.md`.

- Game Copilot receives the bounded question, Context Builder result, QueryPlan,
  selected Skill and aggregate budget.
- Research receives only objective, query units/modes/filters, required facets and
  allowed tools; it receives no full profile/history.
- Planning receives constraints, a Skill-selected explicit profile projection,
  required owned IDs and bounded run-local evidence.
- Worker results contain evidence IDs, conclusions, assumptions, unresolved facets,
  status, usage and stop reason, never chain-of-thought.

Unknown fields, wrong run/sequence/Skill/version, unknown evidence IDs and size
overflow fail closed.

## Skills

Checked-in JSON definitions under `internal/assistant/skill/definitions` are embedded
and startup-validated. Each declares ID/version, prompt version, intents, delegates,
tools, LightRAG modes, budgets, output contract, citations and eval cases.

| Skill | Workers | Read-only tools | Output |
|---|---|---|---|
| `generic_qa` | Research optional | LightRAG, catalog, forum, optional Web | evidence answer |
| `research_guide` | Research | LightRAG, catalog, forum, optional Web | evidence answer |
| `recommend_games` | Research then Planning | LightRAG/catalog/forum/Web; catalog/entitlements/scoring | ranked recommendations |
| `build_team` | Research then Planning | LightRAG/forum/Web; catalog/scoring | evidence-linked team plan |

Unknown versions, write tools, duplicate IDs, cycles and limit escalation fail
startup when advanced mode is enabled.

## Query Planning, Retrieval And Evidence

Evidence/planning routes call Query Planner once. A QueryPlan contains 1-8 unique
units with 1-100 Unicode-rune text, permitted filters, `stable|recent` freshness,
required facets and allowed sources. LightRAG modes are `local`, `global`, `hybrid`
or `mix`; `naive` and `bypass` are not model-selectable.

The LightRAG adapter sends `POST /query/data` with deployment caps for `top_k`,
`chunk_top_k` and token budgets, references enabled, and no conversation history or
request-controlled `user_prompt`. It consumes structured entities, relationships,
chunks and references rather than trusting LightRAG's generated final answer.

Each returned object is bounded and normalized into provider-neutral run-local
evidence. References must have a safe `file_path` belonging to the managed source
namespace (`xlh-*.txt` or the explicit one-time legacy-import namespace). Unknown
sources, unsafe URLs, malformed relationships, missing references and oversized text
are dropped. If all evidence is dropped, the result is `no_result`.

Research may refine queries within budget. A LightRAG outage is explicitly recorded
as `lightrag_unavailable`; the old local knowledge retriever is not a hidden fallback
in advanced mode. Other permitted sources can still produce a partial artifact.

## Planning Agent

Planning is decision support, not execution. It can read current catalog/edition/price
facts, trusted ownership IDs, deterministic filters/scoring and authorized evidence.
The UseCase re-reads mutable catalog/ownership facts, validates every evidence ID and
drops unsupported claims. No cart, coupon, reservation, order or payment capability
is reachable.

## Official LightRAG Boundary

### Version, image and API

The target is official HKUDS LightRAG `1.5.7`, API `0344`. Manifests pin
`ghcr.io/hkuds/lightrag:v1.5.7` plus a reviewed immutable multi-platform digest.
`latest`, source checkouts and forks are rejected. Upgrade needs a compatibility,
backup/restore and reindex decision.

### Native storage

The service starts with exactly:

```text
LIGHTRAG_KV_STORAGE=JsonKVStorage
LIGHTRAG_VECTOR_STORAGE=NanoVectorDBStorage
LIGHTRAG_GRAPH_STORAGE=NetworkXStorage
LIGHTRAG_DOC_STATUS_STORAGE=JsonDocStatusStorage
WORKSPACE=xiaolanhe_v1
WORKING_DIR=/app/data/rag_storage
WORKERS=2
```

`/app/data/rag_storage` is one durable mounted volume owned by the LightRAG runtime.
No `POSTGRES_*` or external storage settings are supplied. All four stores keep their
working set in process memory and persist local files under the workspace directory.
The pinned server may run its supported Gunicorn workers within one service instance,
but only one independently deployed LightRAG service replica may own this volume/
workspace. Container memory, volume capacity and corpus size require explicit
orchestrator limits and alerts.

This is a conscious small-corpus single-instance deployment. It is deployable and
restart-persistent, but is not represented as large-scale/high-availability
production storage. A need for replicas, stateless failover or corpus growth beyond
measured memory limits triggers a separately reviewed external-backend migration.

The embedding model, dimension, asymmetry and prefixes are immutable workspace
metadata in deployment configuration. A change creates a new workspace and full
reindex.

### Network, auth and readiness

LightRAG is private. A distinct `LIGHTRAG_API_KEY` is sent as `X-API-Key`; it is
never returned, logged or traced. Public ingress exposes neither port 9621, WebUI nor
OpenAPI. Go owns a fixed base URL, no proxy inheritance, disabled redirects, bounded
connections, timeouts and response bodies.

Advanced startup validates config plus authenticated `/auth/verify`, `/health` and
`/documents/pipeline_status`. Readiness verifies the expected release/API, selected
Gunicorn/two-worker topology, four store classes, workspace/working-directory contract
and no recovery-required pipeline state. The public health probe is also checked not
to expose authenticated configuration. A busy but healthy indexing pipeline does not
make retrieval unready.
Writable mount, free bytes, filesystem errors and container/process memory cannot be
proven through that API and are host/orchestrator deployment checks.

## Direct LightRAG Knowledge Ownership

The Go knowledge module is a security and contract facade, not a repository. It has
no PostgreSQL knowledge port in advanced mode. The public contract is in
`contracts/knowledge-lightrag-http.md`.

### Create

1. Authenticate admin and validate/cap all fields.
2. Canonicalize bounded metadata plus content and generate
   `xlh-<lowercase-sha256>.txt`.
3. Encode a stable `XiaoLanHe-Knowledge-v1` metadata header plus content into the
   LightRAG text. This keeps title/source/game/region/patch metadata in the sole
   knowledge record without an application mapping table.
4. Call `/documents/text` and return `202` with `trackId`, `sourceKey` and
   `status=accepted`.
5. The caller polls the facade's track endpoint; terminal status exposes the official
   LightRAG `documentId`.

An ambiguous timeout is not blindly retried in the same call. The error returns the
safe deterministic `sourceKey`; replaying the same canonical payload produces the
same key, and the facade reconciles a 409 through a bounded document-list lookup. It
returns the existing status/document as an idempotent replay when exactly one managed
source matches, otherwise a conflict/unknown outcome.

### List, status, delete and replace

- List maps `/documents/paginated` into a bounded XiaoLanHe response and suppresses
  provider errors/internal metadata.
- Track status maps `/documents/track_status/{track_id}`.
- Delete accepts only one validated official document ID and calls exact
  `/documents/delete_document`; it returns asynchronous deletion status.
- The first release has no in-place replacement. Admin deletes the old ID, waits
  until absent, then creates the new content; the temporary retrieval gap is explicit.
- Clear-all, cache clear, force recovery, graph editing, arbitrary upload and source
  conflict repair are not implemented by the Go facade.

Retrieval is limited to managed source namespaces. This blocks accidental use of
documents inserted through an exposed upstream UI, which production does not expose.

### Legacy import

A separately invoked Go command supports dry-run and bounded one-time import from
existing `knowledge_document` rows. It uses deterministic
`xlh-legacy-<document-id>.txt` sources, submits only after explicit operator action,
polls statuses and emits an aggregate report. Re-runs reconcile 409 sources through
bounded LightRAG listing. It never becomes a continuous synchronizer and does not
write LightRAG IDs back to PostgreSQL. After cutover, advanced reads/writes use only
LightRAG; legacy rows are retained solely for rollback until separately retired.

## Layered Memory

Context Builder loads the summary watermark, at most eight newer ordered messages and
the authenticated owner's explicit profile. Workers receive only Skill-required
projections. Guests receive no profile.

Above 12,000 unsummarized Unicode characters, Summary Node receives the prior summary
and messages through a fixed watermark. Output is capped at 2,000 characters. A
compare-and-set update never advances backward. Failure records a safe reason and
answers with the recent window. Only the current validated retrieval query reaches
LightRAG.

## Packages And Dependency Direction

```text
internal/assistant/entity
internal/assistant/usecase
internal/assistant/entry
internal/assistant/presenter
internal/assistant/agent/eino
internal/assistant/repository/postgres
internal/assistant/repository/lightrag
internal/assistant/repository/websearch
internal/assistant/skill
internal/assistant/eval
internal/knowledge/entity
internal/knowledge/usecase
internal/knowledge/entry
internal/knowledge/presenter
internal/knowledge/repository/lightrag
```

`cmd/xiaolanhe` remains the composition root. Eino, LightRAG DTOs, pgx, SQL and Hertz
stay at boundaries. No Go package imports Python or LightRAG internals. The advanced
knowledge module deliberately has no PostgreSQL repository.

## Storage And Migration

Migration `007_advanced_ai.sql` affects only the business database and adds explicit
conversation-summary columns plus profile uniqueness. Existing migrations 001-006
and legacy knowledge tables remain immutable; migration 007 creates no new knowledge,
chunk, vector, graph, document-status, projection or LightRAG-ID table.

LightRAG owns its workspace files. Their filenames/layout are implementation details
of the pinned upstream release, not application contracts. Integration relies on its
HTTP API plus whole-volume lifecycle tests. `data-model.md` records both boundaries.

## Public Contracts

- Existing chat `/api/chat/message` and `/api/chat/stream` bodies/events stay compatible.
- Profile routes are in `contracts/assistant-profile-http.md`.
- Migrated LightRAG knowledge search and asynchronous admin routes are in
  `contracts/knowledge-lightrag-http.md`.
- Internal Agent envelopes are in `contracts/agent-contracts.md`.
- Upstream LightRAG DTOs are not re-exported verbatim.

## Failure, Cancellation And Backpressure

- A request ledger enforces the minimum of global, Skill and worker budgets.
  Cancellation reaches nested Agents and LightRAG requests.
- Overall: 45 seconds/12 model calls/12 tools/three delegations. Game Copilot: four
  iterations. Research: six iterations/eight tools/25 seconds. Planning: four
  iterations/four tools/15 seconds.
- Query/document request and response fields have strict caps. Retry is limited to
  safe pre-response transport failures; ambiguous writes are not blindly replayed.
- LightRAG's own pending-document ceiling supplies write backpressure. The Go facade
  maps 429 and pipeline-busy states without buffering a second application queue.
- 401/403 is configuration failure; 409 is source/pipeline conflict; 422 is permanent
  input/config error; 429/5xx/timeouts are explicit dependency failures.

## Security And Privacy

- Identity and permissions come from XiaoLanHe middleware, never model output.
- Query tools can call only `/query/data`; admin code has a separate fixed allowlist
  for text/status/list/exact delete. No model can construct a LightRAG URL.
- Retrieved content and provider JSON are untrusted evidence.
- Logs/metrics never contain questions, answers, summaries, profiles, cookies, keys,
  raw provider output, full documents or chain-of-thought.
- The persistent volume is encrypted/permission-restricted by the deployment
  environment. Backups inherit the same access and retention policy.

## Observability And Evaluation

The process-local registry exposes Prometheus text at authenticated `GET /metrics`
only when a distinct operator token is configured; advanced mode requires it. Fixed
label vocabularies cover route, Skill, Agent role, operation/status/stop reason,
LightRAG API/query mode/latency, storage contract, document status, pipeline/recovery,
summary outcome and model requests. Provider token counts are recorded only when the
provider returns usage metadata; `usage_reported=false` is explicit and no estimate is
invented. Run/session/user/source/content values are never labels. Safe correlation
IDs remain log fields. Host/orchestrator monitoring owns LightRAG volume bytes/free
space, filesystem errors, process/container memory and restarts. This release does
not install a runtime OpenTelemetry exporter or emit spans; that is an optional
rollout/follow-up.

Checked-in JSONL fixtures run baseline and advanced variants. Reports preserve app,
prompt, Skill, dataset, LightRAG image/API/storage classes, embedding/model versions
and per-case/aggregate metrics. Real-model LightRAG eval requires credential/cost
approval.

## Deployment, Backup, Rollout And Rollback

Local/CI Compose adds one pinned LightRAG service with a named persistent volume,
private network, API key, resource limits and health check. Production-equivalent
testing uses one persistent-volume-capable host/orchestrator; the volume must not be
mounted read-write by another LightRAG replica.

Consistent backup procedure:

1. stop accepting knowledge mutations and wait for the pipeline to become idle;
2. stop the LightRAG service cleanly;
3. snapshot/archive the complete `WORKING_DIR` workspace volume and versioned config;
4. restart, or restore into an empty volume with the same image/model configuration;
5. validate document counts and fixture queries before re-enabling writes.

Rollout order:

1. Resolve image digest and provision private service plus persistent volume.
2. Verify auth, four store classes, volume persistence and backup/restore.
3. Apply migration 007 while advanced mode remains disabled.
4. Dry-run then import approved demo/legacy documents and verify citations.
5. Run deterministic comparison and enable an internal allowlisted cohort.
6. Observe quality, latency, errors, process memory, corpus/volume size and pipeline.

Rollback disables advanced mode and knowledge mutations, returning Assistant reads to
the existing baseline. The LightRAG volume is retained for diagnosis/reuse. Legacy
PostgreSQL rows were never continuously mutated, so rollback needs no reverse sync.

## Rejected Alternatives

- Hand-written LightRAG-like graph storage in Go: not the requested product.
- Separate PostgreSQL for LightRAG: explicitly rejected for this deployment.
- Dual-write/projection between business PostgreSQL and LightRAG: contradicts sole
  knowledge ownership and creates consistency machinery the requirement removed.
- Browser-to-LightRAG access: exposes credentials and bypasses XiaoLanHe authorization.
- Multiple LightRAG replicas sharing the local files: lacks the required safe shared
  storage semantics.
- Silent legacy-local retrieval fallback: makes provenance and quality claims false.
- Planning transaction tools: violates the permanent read-only Agent boundary.

## Architecture Traceability

| Planned change | Owner | Requirement |
|---|---|---|
| module-first migration | assistant, knowledge | AC14 |
| supervisor and worker Agents | assistant/usecase, assistant/agent/eino | AC1, AC2, AC10 |
| Skill registry | assistant/skill | AC3 |
| query planning/evidence | assistant/entity, assistant/usecase | AC2, AC4 |
| LightRAG query adapter | assistant/repository/lightrag | AC5, AC10, AC11 |
| direct document facade | knowledge/usecase, knowledge/repository/lightrag | AC7, AC11, AC14 |
| native-store deployment/volume | deploy, config | AC6, AC15 |
| summary/profile memory | assistant/usecase, assistant/repository/postgres | AC8, AC9 |
| telemetry/evals | assistant/eval, observability | AC12, AC13 |
