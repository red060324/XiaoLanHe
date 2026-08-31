# Technical Plan

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

## Runtime Design

```text
Chat Entry -> Chat Presenter -> Chat UseCase
  -> Context Builder (summary + recent window + explicit profile)
  -> Router Node (intent + Skill)
     DIRECT/CLARIFY -> Answer Node
     EVIDENCE
       -> Query Planner Node -> validated QueryPlan
       -> Research Agent (bounded plan/observe/refine)
            -> Skill read-only allowlist
            -> knowledge hybrid/graph search
            -> catalog search
            -> forum search
            -> optional Web search
       -> deterministic Evidence Fusion
       -> Answer Node with evidence/citations
  -> persist successful conversation state
```

The request lifecycle is deterministic. Only Research chooses subsequent tool
actions after observing results. Router, Planner, Summary, Fusion, and Answer
cannot call tools or loop autonomously.

## Packages And Dependency Direction

Touched Assistant behavior moves incrementally into:

```text
internal/assistant/entry
internal/assistant/presenter
internal/assistant/usecase
internal/assistant/entity
internal/assistant/agent/eino
internal/assistant/repository/postgres
internal/assistant/repository/websearch
internal/assistant/skill
internal/assistant/eval
```

`cmd/xiaolanhe` remains the composition root. Catalog and Community expose
narrow read capabilities to Assistant UseCases; Assistant never imports their
PostgreSQL adapters. Eino, pgx, SQL, provider DTOs, and prompt files remain
behind adapters. The move is limited to touched Assistant code and tests; no
unrelated module shuffle is included.

## Typed Planning And Skills

`RouteDecision` carries `Intent`, `SkillID`, and route only. Evidence routes call
`QueryPlanner.Plan(ctx, PlanRequest) (QueryPlan, error)`.

```text
QueryPlan
  planVersion
  subqueries[1..8]
    text (1..100 runes)
    sources (knowledge|catalog|forum|web)
    game/region/currency filters
    freshness (stable|recent)
  requiredEvidence[0..8]
```

Unknown sources, duplicate/blank/overlong queries, Web requests when disabled,
or excess items are rejected. Fallback uses the original question once against
local read tools. Planner output is a decision artifact, not model reasoning.

Skills are checked-in JSON files embedded at build time and validated at
startup. Each contains ID, semantic version, supported intents, prompt version,
allowed tools, retrieval defaults, limits, citation policy, and eval case IDs.
The registry is an immutable map; no plugin loader or one-implementation
interface is added.

## Memory

The Context Builder loads:

1. summary through a recorded message ID;
2. up to eight newer messages in chronological order;
3. the authenticated user's explicit profile; guests receive no profile.

When unsummarized history exceeds the configured character threshold, a
bounded Summary Node compacts the prior summary plus eligible messages before
the next answer. On failure it logs only safe metadata and continues with the
recent window. Summary persistence uses optimistic `summary_through_message_id`
so concurrent requests cannot move the watermark backward.

Profile changes are ordinary Account/Assistant UseCases behind authenticated,
same-origin HTTP endpoints. The Agent has no profile-write tool. Exact wire
behavior is in `contracts/assistant-profile-http.md`.

## Graph-Enhanced Knowledge

Migration 006 adds summary columns, a unique user profile index, knowledge
entities, relations, and provenance joins. Exact shape is in `data-model.md`.

An authenticated admin explicitly asks to build/rebuild a document graph. A
bounded extractor Node returns typed entities and directed relations. The
UseCase validates sizes/types, then replaces that document's graph provenance
in one transaction. Repeating the same extraction version is idempotent.

Knowledge search performs bounded candidate retrieval:

- PostgreSQL full-text search over chunks;
- existing pgvector cosine search;
- entity lookup and at most two relation hops;
- document/chunk reconstruction for every graph candidate.

Reciprocal Rank Fusion combines ranked lists without comparing incompatible raw
scores. Stable source priority and freshness break ties. The final eight
evidence items retain document/chunk IDs, URL, source kind, published time, and
retrieval routes.

## Failure, Cancellation, And Limits

- Router/Planner/Summary/Extractor are one bounded structured model call each.
- Existing Research defaults remain 30 seconds, six iterations, eight tool
  calls, and ten seconds per tool unless measured evals justify a reviewed
  change.
- Planner fallback is local-only. Optional Web failure yields partial results;
  required local storage failure remains explicit.
- Summary failure never blocks answering. Profile absence is normal.
- Graph extraction failure leaves vector/full-text data intact and records no
  partial graph transaction.
- HTTP/SSE cancellation reaches every model, database, and provider call.

## Security And Privacy

- Trusted user/session identity comes from request context, never model output.
- Logs contain route, intent, Skill/version, plan counts, retrieval routes,
  latency, budgets, and stop reason; never prompts, messages, summaries,
  profile values, cookies, tokens, or raw model output.
- Profile endpoints enforce owner-only access and same-origin mutation policy.
- Extractor input is stored admin-owned knowledge only. Model output is
  untrusted and validated before SQL.
- Every Skill tool is read-only. Transactional Agent work still needs separate
  confirmation, authorization, idempotency, price-revalidation, and audit
  approval.

## Evaluation And Observability

Checked-in JSONL cases contain question, expected route/Skill, required query
facets, fixture evidence IDs, and a citation/faithfulness rubric. Deterministic
tests use fake Nodes and repositories. A separate opt-in command runs real-model
evals and emits a non-versioned JSON report with prompt/Skill versions,
aggregate metrics, latency, calls, and token usage when available.

No resume or report may say "significantly improved" until the approved
baseline comparison and thresholds pass.

## Rollout And Rollback

1. Apply additive migration 006 to isolated PostgreSQL and backfill no model
   data automatically.
2. Enable Planner/Skills behind one Assistant capability flag; profile APIs may
   ship independently.
3. Build graphs only for demo knowledge, compare evals, then expand ingestion.
4. Enable summary refresh last and observe latency/fallback counts.

Rollback disables the capability flag and redeploys the prior image. Additive
tables/columns remain. Graph data can be deleted and rebuilt from knowledge
documents; messages, profiles, orders, and entitlements are never rolled back.

## Rejected Alternatives

- Python LightRAG service: violates the Go-only single-process target and adds
  another runtime/storage contract before its value is measured.
- Neo4j/Redis/queue: unnecessary for the current data volume and request-scoped
  lifecycle.
- Memory/Planner/Answer Agents: they do not require autonomous tool loops.
- Multi-Agent now: adds model cost, failure modes, and merge complexity without
  baseline evidence that independent workers improve the current product.
- Automatic profile inference: silently mutates durable personal data and makes
  correction/consent unclear.

## Traceability

| Planned change | Owner | Requirement |
|---|---|---|
| module-first Assistant migration | assistant | AC9, AC10 |
| typed Planner and Skills | assistant/usecase, skill, agent/eino | AC1-AC3 |
| summary/context/profile | assistant + account boundary | AC5, AC6 |
| graph extraction/retrieval | knowledge repository + Assistant | AC4, AC7 |
| fusion/citations | assistant/usecase | AC4, AC9 |
| eval harness and safe traces | assistant/eval | AC8, AC10 |
