# Clean Architecture Baseline And Research Agent Spec

Status: SUPERSEDED by `../20260831-mature-game-platform/spec.md`
Owner: red060324
Source: current Codex conversation
Branch: codex/clean-architecture-refactor

This spec is authoritative for the target architecture and Agent role
classification. `../20260830-clean-architecture-refactor/` remains historical
migration evidence; its conflicting architecture and Agent assertions are
superseded by this spec.

## Goal

Establish the repository-wide Clean Architecture rules and replace the fixed
Research planner/retriever pipeline with one bounded, read-only autonomous
Research Agent. Keep Router and Answer as bounded model nodes.

## Decisions

1. XiaoLanHe uses Clean Architecture's dependency rule.
2. The product remains a modular monolith and one Go deployment.
3. Router is a Node, Research is an Agent, Answer is a Node.
4. The Research Agent tool surface is read-only. This delivery connects the
   existing local-knowledge and optional Web Search capabilities; forum and
   game-catalog tools are added only after those ordinary backend modules
   exist. It cannot perform transactions or mutate product data.
5. Agent and ordinary backend APIs remain in the same process; each request
   creates a bounded Agent run.
6. Eino v0.9.13 remains pinned for this change; no framework upgrade is bundled.

## In Scope

- Add durable repository architecture and feature-workflow guidance.
- Move domain types and deterministic evidence policies out of the current
  repository-wide `internal/usecase` package.
- Implement Router as a structured-output model node.
- Implement Research as an Eino ADK ReAct Agent with typed read-only tools.
- Implement Answer as direct/synthesis generation nodes with streaming.
- Preserve existing REST/SSE contracts and conversation persistence order.
- Add Agent traces and a deterministic eval suite using fake model/tool events.

## Non-goals

- No shopping, coupon claim, order, payment or community-write Agent tools.
- No Multi-Agent system in this delivery.
- No Planning Agent, verifier loop, MCP, skills marketplace or background jobs.
- No microservice split, queue, Redis or Eino version upgrade.
- No unrelated community, catalog, promotion or order feature implementation.

## Acceptance Criteria

### AC1 Architecture

Repository documentation defines module-first Clean Architecture boundaries.
Hertz, Eino and pgx types do not appear in public UseCase or Entity contracts.

### AC2 Role classification

Runtime and documentation expose exactly one autonomous role: Research Agent.
Router and Answer remain bounded Nodes and are not called Agents in code,
prompts, metrics or documentation.

### AC3 True Research Agent

For a research route, the model can choose a read-only tool, observe its typed
result, refine the query and choose another tool before returning a structured
`ResearchResult`.

### AC4 Read-only boundary

The Research Agent tool registry contains no write, coupon, order, payment or
community mutation capability. Tool implementations cannot derive trusted user
identity or provider endpoints from model arguments.

### AC5 Bounded execution

One run enforces an explicit total deadline, maximum model iterations, maximum
tool calls, per-tool timeout and request cancellation. Reaching a limit returns
an observable bounded-result or typed failure; it never loops indefinitely.

### AC6 Evidence semantics

`ResearchResult` distinguishes evidence, no result, partial provider failure,
all-provider failure and cancellation. Citations preserve source and URL where
available. Provider failures are not represented as successful empty evidence.

### AC7 Compatibility

`POST /api/chat/message` and `POST /api/chat/stream` remain wire compatible.
User and assistant message persistence order remains unchanged.

### AC8 Observability

Traces record route, Agent iteration, selected tool, provider, result,
latency and stop reason. Logs and metrics exclude full user messages, prompts,
session IDs and secrets.

### AC9 Verification

PRE_MERGE unit, Agent-loop, interface, cancellation, race and static checks
pass. Real-model smoke remains a ROLLOUT check.

### AC10 Development lifecycle

The repository provides a discoverable requirement-to-rollout lifecycle,
spec rules, verification guide, readiness checklist, repo Skill, canonical Make
commands, architecture/spec-drift hooks, CI gate and pull-request evidence
template. The documented normal path is executable without ByteDance-internal
infrastructure.

## Assumptions Requiring Review

- Initial Research Agent budget: at most 6 model iterations and 8 tool calls
  within a 30-second request budget. Values will be adjusted only from eval and
  latency evidence.
- Web Search remains explicitly optional. Forum and game search are FOLLOW_UP
  tools and are not represented by placeholder interfaces in this delivery.
- Partial evidence may still produce an answer when at least one source
  succeeds; the answer must carry degradation metadata for logs and tracing.
