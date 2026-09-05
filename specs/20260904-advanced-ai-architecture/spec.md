# 高级 AI 架构补齐 Spec

- Status: `IMPLEMENTED — PRE_MERGE ENVIRONMENT VERIFICATION BLOCKED`
- Owner: red060324
- Source: 2026-09-04 user request and clarification to use official LightRAG's own storage
- Branch: `codex/clean-architecture-refactor`
- Mode: `FULL`
- Supersedes: `../20260831-assistant-capability-evolution/spec.md`

## Goal

Complete the advanced read-only Assistant promised by the XiaoLanHe project
narrative: a hierarchical Multi-Agent Game Copilot, reusable versioned Skills,
Agentic RAG backed by the official HKUDS LightRAG server, layered memory, evidence
contracts and measurable evals. Knowledge data is owned entirely by LightRAG in
advanced mode. Preserve chat REST/SSE behavior and never give an Agent transactional
commerce or community tools.

## Corrected LightRAG Boundary

Two discarded drafts first proposed a Go-written LightRAG-like graph and then an
external PostgreSQL backend for LightRAG. Neither is the requested deployment. This
revision runs the actual official LightRAG server and selects its four built-in local
storage implementations:

- `JsonKVStorage` for chunks, extraction results and caches;
- `NanoVectorDBStorage` for chunk/entity/relation vectors;
- `NetworkXStorage` for the entity-relation graph;
- `JsonDocStatusStorage` for documents and ingestion status.

LightRAG persists these stores under its own `WORKING_DIR` workspace on a mounted
volume. There is no PostgreSQL, Redis, Neo4j, Milvus, Qdrant, MongoDB or OpenSearch
dependency for LightRAG, and XiaoLanHe does not duplicate LightRAG documents, chunks,
vectors, graph records, status or ID mappings in its business database. XiaoLanHe's
existing PostgreSQL remains necessary for users, conversations, profiles, catalog,
community, commerce and other non-LightRAG business data.

The official LightRAG documentation classifies the four selected defaults as
in-memory stores with local-file persistence, suitable for small datasets, testing
and evaluation rather than large-scale production. This release therefore targets a
single LightRAG writer instance with a persistent volume, bounded corpus and tested
backup/restore. It must not claim stateless replicas, horizontal scaling, zero-RPO
durability or large-corpus production readiness. Moving to an external LightRAG
backend is a later measured migration, not part of this scope.
LightRAG still requires configured extraction/query/keyword LLM and embedding
providers; "all knowledge in LightRAG" removes the extra database, not those model
dependencies.

## Decisions

1. Implement three actual Agents with distinct responsibilities:
   - `GameCopilotAgent` is the bounded supervisor and may delegate typed work to
     Research and Planning.
   - `ResearchAgent` is the evidence worker and may call only Skill-approved
     read-only tools, including `search_lightrag`.
   - `PlanningAgent` is the decision-support worker and may call only read-only
     catalog, entitlement and deterministic constraint tools.
2. Router, Query Planner, Summary and Answer remain bounded Nodes. Context building,
   validation and scoring remain deterministic capabilities, not Agents.
3. Agent boundaries use versioned typed contracts and minimum context. Workers never
   receive the complete conversation, unrestricted user object or chain-of-thought.
4. Ship four checked-in declarative Skills: `generic_qa`, `recommend_games`,
   `research_guide` and `build_team`. Skills constrain delegates, tools, LightRAG
   modes, budgets, prompts, outputs and evals; they cannot load executable code.
5. Pin official `ghcr.io/hkuds/lightrag` release `1.5.7`, API `0344` and a reviewed
   immutable digest resolved before merge. Never deploy `latest`.
6. Go reaches LightRAG only through authenticated private-network HTTP. Query uses
   `POST /query/data`; document management uses `/documents/text`, track status,
   paginated listing and exact document deletion.
7. LightRAG is the sole knowledge-system record in advanced mode. Admin writes go
   directly from the authenticated Go facade to LightRAG and return its asynchronous
   `track_id`. Status later supplies LightRAG's `doc_id`. No projection/outbox table,
   synchronization worker or local knowledge copy is introduced.
8. New documents receive a deterministic source key
   `xlh-<sha256(canonical metadata + content)>.txt`. Structured source fields are
   encoded into the submitted document text so that LightRAG, rather than a local
   mapping table, owns all knowledge metadata. Same-payload retries reconcile through
   that key. The first release supports create/list/status/delete; replacement is
   delete then create.
9. Layered memory contains the latest eight messages, a versioned rolling summary
   and an authenticated user-edited profile. Memory remains in XiaoLanHe's business
   PostgreSQL and is never sent as LightRAG conversation history.
10. Advanced mode defaults off. When enabled, LightRAG is required. A LightRAG
    failure produces explicit knowledge-unavailable degradation; it never silently
    falls back to the legacy local knowledge index or labels another source as
    LightRAG. Catalog/forum/Web evidence may still be used when a Skill permits it.
11. XiaoLanHe application code remains Go. The only Python runtime is the pinned
    upstream LightRAG container; no Python application code is added here.
12. All Agent tools are permanently read-only. Knowledge/profile administration is
    an authenticated user-initiated HTTP UseCase and is never exposed to an Agent.

## Scope

### In scope

- Module-first `internal/assistant` and `internal/knowledge` boundaries for touched
  Go code.
- Typed Router output, QueryPlan, worker briefs/results, evidence bundles, plan
  artifacts, budgets, stop reasons and degraded outcomes.
- Bounded Game Copilot delegation to Research and Planning with cancellation and
  cycle prevention.
- Embedded, startup-validated, versioned Skill definitions and prompt versions.
- Read-only LightRAG, catalog, forum, optional Web, entitlement and constraint tools.
- Strict Go LightRAG adapters for health, structured query and direct document
  create/list/status/delete with caps, deadlines, auth and response validation.
- Official LightRAG single-instance deployment using all four built-in stores,
  workspace isolation, a persistent `WORKING_DIR` volume and private networking.
- One-time import tooling for selected legacy knowledge documents; it does not create
  an ongoing duplicate store or synchronization path.
- Summary refresh, recent-window context, explicit Assistant profile API/UI,
  ownership, retention and deletion behavior.
- Safe telemetry, deterministic eval fixtures, live LightRAG persistence tests and
  opt-in real-model eval reports.
- Additive migration 007 for summary/profile only, configuration, rollout/rollback
  documentation, container/CI coverage and a final report.

### Non-goals

- No Go-written replacement for LightRAG storage, extraction or retrieval.
- No LightRAG PostgreSQL or any other external LightRAG storage backend.
- No `knowledge_lightrag_projection`, local document-ID mapping, outbox or sync worker.
- No dual writes to legacy `knowledge_document`/`knowledge_chunk` in advanced mode.
- No live in-place document update in the first release; replacement is explicit
  delete followed by create and can have a temporary retrieval gap.
- No multi-replica LightRAG deployment or large-corpus/high-availability claim with
  the selected in-memory/file-persisted stores.
- No coupon, order, payment, flash-sale, community or moderation Agent mutation.
- No executable/user-uploaded Skills, hidden reasoning storage or fine-tuning.
- No automatic profile inference and no public browser access to LightRAG.
- No paid model run or shared-environment mutation without separate authorization.

## Current-State Evidence

### Reusable

- `internal/usecase/agent.go` already separates Router and Answer Nodes from a
  bounded Research Agent.
- `internal/adapter/eino/research_agent.go` already provides bounded read-only
  knowledge/catalog/forum/Web tool behavior and safe citations.
- Conversation ownership, recent-eight-message loading, HTTP request IDs, SSE
  cancellation, same-origin middleware and config validation are reusable.
- Existing PostgreSQL knowledge tables/retrieval remain a disabled-mode baseline and
  one-time import source only; they are not the advanced knowledge store.

### Verified upstream capability and limitation

- Official LightRAG exposes authenticated `/query/data`, `/documents/text`,
  `/documents/track_status/{track_id}`, `/documents/paginated`, exact deletion and
  `/health` APIs.
- Official LightRAG provides the selected `JsonKVStorage`, `NanoVectorDBStorage`,
  `NetworkXStorage` and `JsonDocStatusStorage`; workspace files persist under
  `WORKING_DIR`.
- The whole default dataset remains resident in LightRAG process memory. Upstream
  explicitly says this combination is not intended for large-scale production and
  recommends an external backend for that case.
- Embedding model/dimension and query/document prefix semantics must remain stable
  for an indexed workspace; changing them requires a new workspace and reindex.

### Implemented from the approved gap list

- Game Copilot supervisor, independently bounded Research and Planning Agents, typed
  inter-Agent contracts and a concurrent request-wide budget ledger.
- Four embedded declarative Skills with startup validation and tool/delegate/mode
  allowlists.
- Strict official LightRAG client, pinned native-store deployment manifest, direct
  knowledge administration and bounded one-time legacy import tooling.
- Explicit profile UI/API, rolling summaries, deterministic evaluation and protected
  low-cardinality Prometheus metrics.

Official-container ingestion/retrieval/restart/restore, live PostgreSQL migration and
real-provider evaluation remain verification work, not missing application behavior.
They require the isolated container/database/provider environments listed in the test
plan and must not be reported as passed from mock or static evidence.

## Acceptance Criteria

- **AC1 — Hierarchical Multi-Agent:** Evidence/planning requests use one bounded Game
  Copilot that may call real, independently bounded Research and Planning workers.
- **AC2 — Minimal typed delegation:** Every supervisor/worker exchange validates a
  versioned schema; malformed, oversized, stale or mismatched artifacts fail closed.
- **AC3 — Reusable Skills:** The four approved immutable Skills are startup-validated
  and enforce delegate/tool/mode/budget/output allowlists.
- **AC4 — Agentic query planning:** Evidence routes create 1-8 validated query units
  with allowed LightRAG mode/source hints, filters, freshness and required facets.
- **AC5 — Real LightRAG retrieval:** Advanced knowledge retrieval calls official
  `/query/data` and consumes structured entities, relationships, chunks and
  references for `local`, `global`, `hybrid` and `mix`; no Go substitute qualifies.
- **AC6 — LightRAG-native storage:** Live deployment proves the official server uses
  exactly `JsonKVStorage`, `NanoVectorDBStorage`, `NetworkXStorage` and
  `JsonDocStatusStorage` in workspace `xiaolanhe_v1`, with all `WORKING_DIR` data on
  one persistent volume surviving a clean service/container restart. No external
  LightRAG database is provisioned.
- **AC7 — LightRAG-owned knowledge:** In advanced mode, create/list/status/delete and
  both Assistant and `/api/knowledge/search` retrieval go only through the LightRAG
  API. Create returns `202` plus `trackId`; status returns the official document ID;
  delete is exact and asynchronous. No business-PostgreSQL knowledge read/write,
  projection table or dual-write occurs.
- **AC8 — Layered memory:** Context contains a monotonic versioned summary, at most
  eight newer messages and the authenticated user's explicit profile projection.
- **AC9 — Profile ownership:** A signed-in user can read, replace and clear only their
  validated Assistant profile; guests get none and no Agent can mutate it.
- **AC10 — Safety and limits:** Cancellation reaches all Agents, Nodes, Go adapters,
  business database calls and LightRAG HTTP calls. Budgets are bounded and LightRAG
  failures are explicit without hidden local-knowledge fallback.
- **AC11 — Read-only trust boundary:** Agents have no business mutation tools. The
  LightRAG key and endpoint stay server-side; provider JSON/references are untrusted,
  bounded and validated.
- **AC12 — Observability:** Protected low-cardinality application metrics cover route,
  Skill, delegation, model/tool calls, stop reason, LightRAG request/query/storage
  contract/pipeline/recovery/managed-document status, summary refresh and provider-
  reported token use without secrets or content. Correlation IDs remain only in safe
  structured logs. Volume bytes/free space and process/container memory are collected
  by the host/orchestrator. Runtime OpenTelemetry export is an optional follow-up, not
  a claim of this release.
- **AC13 — Measurable quality:** Versioned deterministic evals compare baseline and
  advanced flows for routing, facets, Recall@8, citations, faithfulness, memory,
  calls and latency, preserving all relevant model/Skill/LightRAG versions.
- **AC14 — Contracts and architecture:** Chat REST/SSE behavior stays compatible.
  Knowledge create/admin and public search contracts deliberately migrate to
  asynchronous LightRAG string IDs and provider-neutral evidence respectively.
  Provider DTOs stay in adapters, production application code stays Go and
  Account/Community/Commerce/flash-sale regressions pass.
- **AC15 — Controlled delivery:** Migration 007 is additive and contains no LightRAG
  knowledge table. Advanced mode defaults off; configuration/readiness fail closed;
  the single-replica limitation, backup procedure and rollback are tested and
  documented without an unsupported production-readiness claim.

## Proposed Defaults

1. LightRAG `1.5.7`, API `0344`, immutable image digest resolved before merge.
2. `WORKSPACE=xiaolanhe_v1`, `WORKING_DIR=/app/data/rag_storage`, one LightRAG
   service replica with two supported Gunicorn workers and a durable mounted volume.
3. `JsonKVStorage`, `NanoVectorDBStorage`, `NetworkXStorage`,
   `JsonDocStatusStorage`; no `POSTGRES_*` configuration.
4. Private `X-API-Key`; browser access and redirects disabled.
5. Overall request: 45 seconds, 12 model calls, 12 tools, three delegations; Game
   Copilot 4 iterations, Research 6/8 tools/25 seconds, Planning 4/4 tools/15 seconds.
6. LightRAG query: `mix`, `top_k=20`, `chunk_top_k=12`,
   `max_total_tokens=12000`, references enabled, no conversation history or
   request-controlled `user_prompt`.
7. Summary threshold 12,000 Unicode characters, cap 2,000, recent window eight.
8. Eval thresholds: route/Skill >= 0.90, facets >= 0.85, Recall@8 >= 0.80, citation
   and explicit-memory consistency 1.00, zero unauthorized calls/unsupported fixture
   claims and no baseline quality regression over 0.02.

## Clarify Decisions

Approved by the user on 2026-09-04: direct official LightRAG ownership using the four
built-in stores, one service replica with a persistent volume, the asynchronous
knowledge contract, three-Agent classification, Skills, profile/memory migration,
budgets, tests and rollout order. Production implementation may proceed.
