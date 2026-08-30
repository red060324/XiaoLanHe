# Technical Plan

Status: SUPERSEDED by `../20260831-mature-game-platform/plan.md`
Authoritative spec: ./spec.md

## Target Runtime

```text
HTTP/SSE Entry
  -> Chat Presenter
  -> Chat UseCase
  -> Router Node
       DIRECT/CLARIFY -> Answer Node
       RESEARCH       -> Research Agent -> read-only Tools -> Repositories
                                      -> Answer Node with ResearchResult
  -> Conversation Repository
```

The composition root constructs required dependencies once. Request context,
deadlines and cancellation are passed through every call.

## Dependency Ownership

- Entry owns Hertz routes and SSE writer interaction.
- Presenter owns wire DTO validation and mapping.
- Chat UseCase owns application order and `ChatInput`/`ChatResult`.
- Entities own `Route`, `Evidence`, `Citation`, `ResearchResult` and
  deterministic merge/selection rules.
- Router Node, Research Agent and Answer Node satisfy consumer-owned
  application capabilities.
- Repositories own Qwen/Eino model calls, PostgreSQL/pgvector, forum/game data
  access and SearXNG protocol mechanics.
- Eino ADK is private to the Research Agent implementation.

## Proposed Packages

Create packages only as code moves into them:

```text
internal/app/
internal/assistant/entry/http/
internal/assistant/presenter/
internal/assistant/usecase/
internal/assistant/entity/
internal/assistant/agent/research/
internal/assistant/repository/eino/
internal/assistant/repository/postgres/
internal/assistant/repository/websearch/
```

The current `internal/usecase/agent.go` is migration debt. It mixes route
contracts, evidence entities, fixed research orchestration, concurrency and
RRF. Split behavior while preserving public API behavior; do not copy it into a
new package wholesale.

## Node And Agent Contracts

Router Node:

```text
Route(ctx, RouteInput) -> RouteDecision
```

It performs one structured model call and deterministic validation/fallback.

Research Agent:

```text
Research(ctx, ResearchInput) -> ResearchResult
```

It uses an Eino ADK ChatModelAgent/ReAct loop with a typed allowlist. This
delivery registers only tools backed by current production capabilities:

- `search_knowledge`
- `search_web` when enabled

`search_forum` and `search_game` are added later inside their product-module
specs; no empty interface or fake implementation is created now.

Answer Node:

```text
Generate(ctx, AnswerInput) -> Answer
Stream(ctx, AnswerInput) -> AnswerStream
```

It cannot call tools or decide new routes.

## Agent Budgets

- Total research budget: 30 seconds inside the request deadline.
- Model iterations: 6 maximum.
- Tool calls: 8 maximum.
- Existing retrieval fan-out remains bounded; tools may not create unbounded
  goroutines.
- HTTP cancellation terminates model and tool calls.

These are safety defaults, not product tuning claims. Eval evidence is required
before increasing them.

## Errors And Degradation

- Router model failure: deterministic conservative route with a trace reason.
- One research tool fails: retain successful evidence and report degradation.
- All attempted tools fail: typed dependency failure; no fake successful note.
- No relevant evidence: successful empty `ResearchResult`, distinct from error.
- Budget reached: structured stop reason; Answer decides whether to answer from
  partial evidence or ask for clarification.
- Cancellation: propagate `context.Canceled` without persistence of a partial
  assistant answer.

## Storage

No new table is required. Conversation persistence remains unchanged. Agent
steps are emitted to logs/traces, not stored in PostgreSQL in this delivery.
Persistent trace or checkpoint storage requires a later spec with retention and
privacy decisions.

## Configuration

Keep only settings with runtime behavior. Remove or separately implement dead
diagnostic values such as `agentMode` and `minioBucket`. Do not add provider
selection flags for providers that do not exist.

## Migration

1. Add domain contracts and characterization tests.
2. Extract Router and Answer bounded nodes behind consumer-owned capabilities.
3. Introduce read-only Research Agent with fake tools.
4. Wrap existing knowledge/web search as typed tools.
5. Replace fixed `Research.Retrieve` in Chat UseCase.
6. Remove superseded fixed planner/retrieval code and misleading Agent names.
7. Update README and verification report.

No API or destructive database migration is planned. Rollback is a Git revision
rollback.

## Future Transactional Agent Extension

Promotion and Order remain ordinary backend modules. A future Agent tool must
call their application UseCases; it never calls their repositories. That later
spec must add authentication, explicit confirmation, idempotency, price/stock
revalidation and audit evidence. No placeholder transaction Tool is added now.
