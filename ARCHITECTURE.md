# XiaoLanHe Architecture

## System Shape

XiaoLanHe is a modular monolith. Business modules share one Go deployment and
PostgreSQL instance, but own their behavior and storage access.

```text
HTTP / SSE
  -> Account | Catalog | Community | Promotion | Order | Assistant
  -> PostgreSQL / pgvector / model / search providers
```

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
  -> Chat UseCase
  -> Router Node
       DIRECT  -> Answer Node
       CLARIFY -> Answer Node
       RESEARCH
          -> Research Agent (bounded ReAct loop)
               -> search_knowledge
               -> search_web when enabled
          -> Answer Node with Evidence
  -> persist conversation
```

Definitions:

- Router Node: one bounded structured-output model call; not an Agent.
- Research Agent: model-controlled read-only tool loop that observes tool
  results and may refine its query until it returns `ResearchResult` or reaches
  its budget.
- Answer Node: one bounded generation/streaming call; not an Agent.
- Memory, embedding, RRF and citation formatting are capabilities or
  deterministic policies, not Agents.

The outer request lifecycle remains deterministic. Eino ADK is an adapter for
the Research Agent runtime; Eino types remain private to that boundary.
Catalog and forum search join this same read-only allowlist when their tool
adapters land; they are not placeholder Agents or mutation capabilities.

## Agent Safety And Lifecycle

- Tools are read-only in the current scope.
- Tool identity and user context come from trusted request context, never model
  arguments.
- Each run has a total deadline, maximum iterations, maximum tool calls and
  per-provider limits.
- Cancellation propagates from HTTP/SSE to the Agent and every tool.
- Conversation memory is persisted by business code; it is not process memory.
- Record route, Agent step, tool, result, latency and fallback reason without
  logging full prompts, user messages, or secrets.

Future transactional Agent tools must call the existing Promotion or Order
UseCases and require a separate reviewed spec covering authorization, explicit
user confirmation, idempotency, price revalidation and audit logging.

## Deployment

The HTTP server, ordinary business modules and Assistant Agent run in the same
Go process. Each user request creates an independent Agent run. A separate
Agent service is justified only by long-running/background execution,
independent scaling, unsafe tool isolation, or materially different SLOs.
