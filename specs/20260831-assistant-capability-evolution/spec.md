# 游戏助手能力演进 Spec

- Status: `DRAFT`
- Owner: red060324
- Source: current mature-platform goal and 2026-08-31 Agent design discussion
- Branch: `codex/clean-architecture-refactor`
- Mode: `FULL`

This spec extends the completed read-only Assistant in
`../20260831-mature-game-platform/`. It does not supersede the independent
public-rollout abuse-protection spec.

## Goal

Evolve the current bounded Research Agent into a measurable game-research
system with structured query planning, reusable business Skills, layered
conversation memory, graph-enhanced local retrieval, deterministic evidence
fusion, and an evaluation harness while keeping the backend Go-only and the
Assistant read-only.

## Decisions

1. Keep Router, Query Planner, Summary, Evidence Fusion, and Answer as bounded
   Nodes or deterministic capabilities. Research remains the sole autonomous
   Agent in this delivery.
2. Persist a typed `QueryPlan`; do not request, expose, or store model
   chain-of-thought.
3. Ship three checked-in, versioned, declarative Skills:
   `recommend_games`, `research_guide`, and `build_team`. A Skill restricts
   tools and retrieval policy; it cannot load arbitrary code.
4. Use an eight-message recent window, a versioned conversation summary, and an
   authenticated user-edited profile. The model does not silently write
   long-term profile facts.
5. Implement LightRAG-inspired graph/vector/full-text retrieval natively in Go
   and PostgreSQL/pgvector. Do not add the Python LightRAG server, Neo4j,
   Redis, a queue, or a second runtime.
6. Run graph extraction through an explicit admin action in this slice. Vector
   ingestion remains usable if graph extraction fails.
7. Add adaptive Supervisor/Worker Multi-Agent only as a follow-up when evals
   prove that the bounded Research Agent cannot meet an approved quality target.

## Scope

### In scope

- Intent and Skill selection separated from typed evidence-query planning.
- Validated subqueries with source hints, filters, freshness, and evidence
  requirements.
- Skill-specific tool allowlists, prompts, version, limits, and eval cases.
- Best-effort summary refresh plus recent-window context construction.
- Authenticated profile read/replace/clear APIs and account UI.
- Knowledge entities, directed relations, chunk provenance, graph extraction,
  one/two-hop expansion, PostgreSQL full-text search, existing vector search,
  and reciprocal-rank evidence fusion.
- Citation provenance, partial/no-result/degraded behavior, safe traces, and
  deterministic offline eval reports.
- Incremental migration of touched Assistant code into a module-first package.

### Non-goals

- No transactional Agent tools, autonomous writes, background Agent, real
  payment action, user-generated code Skill, prompt marketplace, or hidden
  reasoning storage.
- No Supervisor/Worker Multi-Agent implementation in this delivery.
- No Python, Java, LightRAG service, external graph database, Redis, queue, or
  microservice split.
- No automatic profile inference from ordinary conversation and no claim that
  quality improved until the checked-in eval comparison proves it.

## Current-State Evidence

- Reusable: `internal/usecase/agent.go` already separates Router/Answer Nodes
  from the bounded Eino Research Agent and exposes provider-neutral contracts.
- Reusable: the Agent has four read-only tools, request cancellation, total and
  per-tool deadlines, iteration/tool limits, typed observations, and citation
  output.
- Reusable: PostgreSQL already stores conversations, vectorized knowledge
  chunks, catalog, forum, promotion, orders, and entitlements.
- Migration debt: Router currently selects the route and emits subqueries in
  one call, so planning cannot be evaluated or versioned independently.
- Migration debt: context loads `summary_text` from session metadata but no
  code creates or advances a summary; the current effective memory is only the
  latest eight messages.
- Migration debt: `player_profile` exists but has no public behavior or
  uniqueness contract.
- Migration debt: evidence is de-duplicated in tool-call order; scores from
  heterogeneous sources are not fused into a comparable ranking.
- Missing: knowledge entities/relations, graph provenance, graph extraction,
  Skill registry, profile management, and a requirement-linked eval dataset.

## Acceptance Criteria

- AC1: Evidence routes produce a validated typed `QueryPlan` with 1-8 bounded
  subqueries, source hints, freshness, and evidence requirements. Invalid or
  failed planner output falls back to one safe local query without storing CoT.
- AC2: Router selects exactly one versioned Skill or the generic default.
  Research can call only that Skill's read-only allowlist and records the Skill
  ID/version without logging message or prompt content.
- AC3: Research remains the sole Agent, respects the existing total/tool/model
  budgets and cancellation, and cannot reach Account, Community mutation,
  Promotion, Order, or payment capabilities.
- AC4: Evidence Fusion deterministically de-duplicates and ranks vector,
  full-text, graph, catalog, forum, and optional Web evidence with complete
  provenance; partial/all-failed/no-result semantics remain explicit.
- AC5: Context contains a recent window, a versioned summary when available,
  and the authenticated user's explicit profile. Summary failure degrades to
  the recent window and never blocks a reply.
- AC6: A user can inspect, replace, and clear only their own Assistant profile.
  Profile values are validated and never inferred or mutated by the Agent.
- AC7: Admin-triggered graph extraction records entities, relations, and source
  chunks idempotently. Graph failure does not corrupt the document or vector
  index; graph retrieval never returns evidence without chunk/document
  provenance.
- AC8: Checked-in evals compare the current baseline, planner+Skills, memory,
  and graph retrieval using route/Skill accuracy, subquery coverage, Recall@K,
  citation coverage, faithfulness rubric, latency, model/tool calls, and token
  usage where the provider reports it.
- AC9: Existing REST/SSE success, cancellation, citation, conversation
  ownership, and read-only safety contracts remain compatible.
- AC10: All backend production code remains Go; Clean Architecture, migration,
  security, race, PostgreSQL, frontend, container, public-only, and Java-absence
  gates pass.

## Assumptions And Open Questions

1. Summary refresh is triggered when unsummarized history exceeds 12,000
   Unicode characters; the stored summary is capped at 2,000 characters. These
   are tunable positive config values, not claimed token counts.
2. The profile contract contains favorite genres, preferred platforms, default
   region, preferred languages, and an optional price ceiling/currency. Owned
   games continue to come from Entitlements, not duplicated profile data.
3. Graph expansion is capped at two hops and 50 candidate nodes/relations per
   request before fusion.
4. Graph extraction is synchronous and admin-triggered for the first release;
   a background job is introduced only after measured ingestion latency or
   volume requires it.
5. The default Skill handles questions outside the three named business Skills.

## Clarify Decisions

Awaiting human approval of scope, single-Agent classification, profile
contract, PostgreSQL-native graph design, phase order, tasks, and test plan.
Production implementation must not begin before approval.
