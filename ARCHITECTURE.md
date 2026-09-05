# XiaoLanHe Architecture

## System Shape

XiaoLanHe is a modular monolith. Business modules share one Go deployment and
PostgreSQL instance, but own their behavior and storage access.

```text
HTTP / SSE
  -> Account | Catalog | Community | Promotion | Order | Flash Sale | Assistant | Knowledge
  -> PostgreSQL / pgvector / Redis / RocketMQ / model / search / official LightRAG
```

The optional flash-sale path remains in the same Go deployment but adds a
Redis-backed admission boundary and RocketMQ-backed asynchronous order path:

```text
Flash-sale HTTP -> RocketMQ transactional producer -> Redis Lua admission
  -> RocketMQ at-least-once consumer -> Flash-sale UseCase -> Order UseCase
  -> PostgreSQL final stock/idempotency guard
```

Redis is the fast admission and recovery-marker store, not the durable order
source of truth. RocketMQ delivery is at-least-once; idempotent PostgreSQL
constraints are the final correctness boundary. Provider types remain inside
the flash-sale repository adapters.

Start with these modules only when their product behavior exists:

- `account`: identity, profile, game preferences
- `catalog`: games, editions, prices, discovery
- `community`: posts, comments, reactions, moderation
- `promotion`: campaigns, coupon stock, eligibility, claims
- `order`: carts, orders, payment state, coupon redemption
- `knowledge`: ingestion, retrieval, evidence and citations
- `assistant`: conversation lifecycle, nodes, Agent runtime and read-only tools

Do not split a module into a service until it needs independent scaling,
isolation, availability, or ownership.

## Clean Architecture Rule

Runtime business flow:

```text
Entry -> Presenter -> UseCase -> Entity -> Repository -> External System
```

This is a responsibility map, not a requirement that every request touch every
layer. Compile-time dependencies point toward business policy. Composition is
owned by `internal/app` or the executable entry point.

- Entry: route registration, authentication context, request cancellation.
- Presenter: protocol validation and request/response mapping.
- UseCase: one application operation and its orchestration.
- Entity: stable business types, invariants and deterministic policies.
- Repository: database, model, search, object-store and remote API mechanics.

Consumer packages own the narrow contracts they need. Do not add forwarding
wrappers or one-implementation interfaces solely to make the directory tree
look architectural.

## Assistant Runtime

```text
Chat Entry
  -> Chat Presenter
  -> Chat UseCase -> context(summary + latest 8 + typed profile)
  -> Router Node -> immutable Skill registry
       DIRECT / CLARIFY -> Answer Node
       RESEARCH / PLANNING -> Query Planner Node
          -> Game Copilot supervisor (maximum 4 transitions)
               -> Research Agent (bounded ReAct, read-only search tools)
               -> Planning Agent (bounded read-only catalog/entitlement tools)
          -> Answer Node with validated evidence and optional plan artifact
  -> persist complete answer -> best-effort monotonic summary refresh
```

Definitions:

- Router Node and Query Planner Node: bounded structured-output model calls; neither
  is an Agent.
- Game Copilot: the supervisor Agent. It selects one legal next action per turn,
  enforces Research before Planning, prevents duplicate/cyclic delegation and shares
  one request budget across all children.
- Research Agent: model-controlled read-only tool loop that observes tool
  results and returns a typed evidence/facet artifact. In advanced mode its knowledge
  tool calls official LightRAG; catalog/forum and optional Web remain separate sources.
- Planning Agent: independently bounded read-only tool loop. It consumes only
  run-local evidence IDs and typed profile constraints, then revalidates catalog,
  ownership and price facts before returning a plan artifact.
- Answer Node: one bounded generation/streaming call; not an Agent.
- Skills, memory, evidence storage and citation formatting are deterministic
  capabilities, not Agents.

All supervisor/worker exchanges carry schema version, run ID, sequence and
Skill identity. Evidence IDs are generated server-side inside one run. Eino types
remain private to adapter packages.

## Agent Safety And Lifecycle

- Tools are read-only in the current scope.
- Tool identity and user context come from trusted request context, never model
  arguments.
- Each run has a total deadline, maximum iterations, maximum tool calls and
  per-provider limits.
- Cancellation propagates from HTTP/SSE to the Agent and every tool.
- Conversation memory is persisted by business code; it is not process memory.
- Structured events record route, Skill, Agent role, operation, bounded status,
  budget counts, latency and fallback reason without logging prompts, messages,
  answers, profile fields, evidence/document content or secrets.

The process-local metrics registry exports Prometheus text only on `GET /metrics`
when a separate operator token is configured. Labels use fixed allowlists and never
contain run/session/user/source identifiers or content. The Eino boundary records
provider token counts only when the provider returns usage metadata; missing usage is
reported as unavailable rather than estimated. Application metrics cover Agent,
model, LightRAG API/storage-contract/pipeline/managed-document, summary behavior,
and flash-sale admission/transaction/consume/final-guard/recovery/expiry/release.
Flash-sale labels are fixed enums; request, activity and user IDs remain log-only.
Volume bytes, host/process memory and filesystem capacity are deployment metrics and
must be collected by the host or orchestrator. This release does not install a
runtime OpenTelemetry exporter or emit spans.

The Assistant remains read-only in this project and cannot call Promotion or
Order mutation UseCases. Coupon claims, orders, and sandbox payments remain
explicit user actions through ordinary authenticated HTTP flows.

## Deployment

The HTTP server, ordinary business modules, optional flash-sale consumer and
workers, and Assistant Agents run in the same Go process. Official LightRAG is a
separate Python container because it owns indexing and native knowledge storage.
It runs as one service replica (two supported workers) with one persistent
`WORKING_DIR`; a second replica must never share that read-write workspace. Each
user request creates an independent Agent run.

Business PostgreSQL remains authoritative for accounts, conversations, summaries,
profiles, catalog, community, promotions and orders. In advanced mode LightRAG is
the sole knowledge-system record: there is no application projection, ID mapping,
outbox, dual write or continuous synchronizer. Legacy PostgreSQL knowledge is only a
disabled-mode baseline and an explicit one-time import source.
