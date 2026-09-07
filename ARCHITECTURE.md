# XiaoLanHe Architecture

## System Shape

XiaoLanHe is a modular monolith. Business modules share one Go deployment and
one MySQL 8.4/InnoDB system of record, but own their behavior and storage
access. Knowledge has a separate, single boundary: every knowledge operation
uses the official LightRAG HTTP API.

```text
HTTP / SSE
  -> Account | Catalog | Community | Promotion | Order | Flash Sale | Assistant
       -> MySQL 8.4 / InnoDB
       -> Redis Lua -> RocketMQ -> MySQL finalization
  -> Knowledge -> official LightRAG 1.5.7
       -> JsonKVStorage files in persistent WORKING_DIR
       -> NetworkXStorage files in persistent WORKING_DIR
       -> JsonDocStatusStorage files in persistent WORKING_DIR
       -> MilvusVectorDBStorage -> Milvus 2.6.11
```

MySQL owns accounts, sessions, conversations and summaries, profiles, catalog,
community, promotions, orders, payments, entitlements and durable flash-sale
records. It is not a knowledge or vector store. Milvus owns only the chunk,
entity and relationship vector projections created by LightRAG. The Go
application never reads or writes Milvus directly.

The optional flash-sale path remains in the same Go deployment and retains its
Redis-backed admission boundary and RocketMQ-backed asynchronous order path:

```text
Flash-sale HTTP -> RocketMQ transactional producer -> Redis Lua admission
  -> RocketMQ at-least-once consumer -> Flash-sale UseCase -> Order UseCase
  -> MySQL final stock/idempotency guard
```

Redis is the fast admission and recovery-marker store, not the durable order
source of truth. RocketMQ delivery is at-least-once; idempotent InnoDB
constraints and transactions are the final correctness boundary. Provider types
remain inside the flash-sale repository adapters.

Start with these modules only when their product behavior exists:

- `account`: identity, profile, game preferences
- `catalog`: games, editions, prices, discovery
- `community`: posts, comments, reactions, moderation
- `promotion`: campaigns, coupon stock, eligibility, claims
- `order`: carts, orders, payment state, coupon redemption
- `knowledge`: LightRAG ingestion, retrieval, evidence and citations
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
look architectural. MySQL driver types stay in repository/adapter packages, and
LightRAG/Milvus provider details do not enter public UseCase or Entity contracts.

## Knowledge Ownership

LightRAG is the sole knowledge-system boundary in both basic and advanced
Assistant modes and for the public/admin knowledge APIs. Disabling advanced AI
changes orchestration only; it never enables a SQL, pgvector, in-process vector
or direct-Milvus fallback. A LightRAG or Milvus failure is reported explicitly
and boundedly.

LightRAG owns all knowledge documents, chunks, extraction results, graph,
ingestion status and retrieval. Its stores have deliberately distinct roles:

- `JsonKVStorage` persists full documents, chunks and caches as LightRAG files.
- `NetworkXStorage` persists the entity/relation graph as LightRAG files.
- `JsonDocStatusStorage` persists ingestion status as LightRAG files.
- `MilvusVectorDBStorage` owns the derived chunk/entity/relation vectors.

The three file stores live in the complete persistent `WORKING_DIR`. Moving the
vector projection to Milvus does not make the whole knowledge system
horizontally scalable: this release supports one LightRAG service replica with
one read-write workspace and standalone Milvus. No multi-replica or HA claim is
made.

Existing NanoVectorDB data can enter Milvus only through the approved offline
rebuild. All writers must be stopped, the complete source workspace backed up,
all three official rebuild functions run, and the external five-state fence must
be `verified` for the exact deployment generation and embedding contract before
readiness can pass. A storage configuration flip is not a migration.

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

- Router Node and Query Planner Node are bounded structured-output model calls;
  neither is an Agent.
- Game Copilot is the supervisor Agent. It selects one legal next action per turn,
  enforces Research before Planning, prevents duplicate/cyclic delegation and shares
  one request budget across all children.
- Research Agent is a model-controlled read-only tool loop. Its knowledge tool
  always calls official LightRAG; catalog/forum and optional Web remain separate
  evidence sources.
- Planning Agent is an independently bounded read-only tool loop. It consumes only
  run-local evidence IDs and typed profile constraints, then revalidates catalog,
  ownership and price facts before returning a plan artifact.
- Answer Node is one bounded generation/streaming call, not an Agent.
- Skills, memory, evidence storage and citation formatting are deterministic
  capabilities, not Agents.

All supervisor/worker exchanges carry schema version, run ID, sequence and Skill
identity. Evidence IDs are generated server-side inside one run. Eino types
remain private to adapter packages.

## Agent Safety And Lifecycle

- Tools are read-only in the current scope.
- Tool identity and user context come from trusted request context, never model
  arguments.
- Each run has a total deadline, maximum iterations, maximum tool calls and
  per-provider limits.
- Cancellation propagates from HTTP/SSE to the Agent and every tool.
- Conversation memory is persisted in MySQL by business code; it is not process
  memory.
- Structured events record route, Skill, Agent role, operation, bounded status,
  budget counts, latency and fallback reason without logging prompts, messages,
  answers, profile fields, evidence/document content or secrets.

The process-local metrics registry exports Prometheus text only on `GET /metrics`
when a separate operator token is configured. Labels use fixed allowlists and never
contain run/session/user/source identifiers or content. The Eino boundary records
provider token counts only when the provider returns usage metadata; missing usage is
reported as unavailable rather than estimated. Application metrics cover Agent,
model, MySQL, LightRAG storage/fence/pipeline/managed-document behavior, summaries and
flash-sale admission/transaction/consume/final-guard/recovery/expiry/release. Volume
bytes, host/process memory and filesystem capacity are deployment metrics collected by
the host or orchestrator. This release does not install a runtime OpenTelemetry
exporter or emit spans.

The Assistant remains read-only and cannot call Promotion or Order mutation
UseCases. Coupon claims, orders, sandbox payments, reservations and community
mutations remain explicit user actions through ordinary authenticated HTTP flows.

## Deployment And Migration Isolation

The HTTP server, ordinary business modules, optional flash-sale consumer and
workers, and Assistant Agents run in the same Go process. MySQL is a separate
stateful service. Official LightRAG is a separate Python service with its
persistent `WORKING_DIR`; it reaches Milvus over private networking. Milvus
standalone additionally persists its Milvus, etcd and MinIO volumes.

Normal application and seed binaries use MySQL only. They must not import pgx, a
PostgreSQL repository, pgvector or NanoVectorDB. The only permitted PostgreSQL
code is isolated, operator-invoked migration tooling under
`cmd/import-knowledge`, `internal/knowledge/importer/postgres`,
`cmd/migrate-postgres-to-mysql` and `internal/migration/postgrestomysql`. Those
packages must never be linked into the application, seed or evaluation binaries.
The architecture gate enforces this exact allowlist.

Historical PostgreSQL migrations under `migrations/` remain immutable migration
history and cutover input. They are not executed against MySQL; the MySQL baseline
lives under `migrations/mysql/`. Legacy PostgreSQL knowledge remains read-only through
the approved rollback window and is never a runtime fallback. Physical source deletion
requires separate approval.

Repository configuration and scripts describe the target topology but do not buy or
provision production MySQL, Milvus, backup storage or provider credentials. Production
provisioning, destructive retirement, paid embedding/rebuild, data cutover and traffic
enablement are separate rollout actions requiring explicit approval and recorded
backup/restore evidence.
