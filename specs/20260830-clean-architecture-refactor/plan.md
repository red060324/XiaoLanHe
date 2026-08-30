# Technical Plan

Status: APPROVED (2026-08-30)
Authoritative spec: ./spec.md

## Selected Approach

选择 Go 渐进迁移，而不是保留 Java 原地重排或一次性重写。

- 原地 Java 重排风险最低，但无法达到用户希望的 Go 技术栈与 auto_msg 风格。
- 一次性 Go 重写最终目录最干净，但没有测试基线，无法区分“重构”和“行为重写”。
- 纵向切片短期保留双栈，但每一步可运行、可对比、可回滚，因此采用。

## Target Runtime Flow

HTTP/SSE Entry
  -> Chat Presenter
  -> Chat UseCase
  -> Assistant Workflow contract
  -> Domain nodes: context, route, retrieval policy, evidence fusion, answer policy
  -> Adapters: Eino, PostgreSQL, pgvector, LightRAG, Web Search

Eino Graph 仅负责拓扑、typed data flow、streaming 和 callback。业务路由、降级、证据融合与错误语义放在可独立测试的 Domain 能力中。

## Proposed Go Layout

cmd/xiaolanhe/main.go
internal/app/
internal/entry/http/
internal/presenter/chat/
internal/usecase/chat/
internal/domain/assistant/
internal/domain/conversation/
internal/domain/knowledge/
internal/adapter/eino/
internal/adapter/postgres/
internal/adapter/lightrag/
internal/adapter/websearch/
internal/config/
migrations/
frontend/xiaolanhe-web/

约束：

- 不新增 common、utils、base、manager。
- 接口放在消费方包，只为跨层或外部依赖创建。
- internal/app 是组合根，只构造依赖，不做业务流程。
- 每个包先有实际纵向切片需求，再创建；不先搭空目录。

## Workflow Topology

start
  -> load conversation context
  -> orchestrator route
  -> direct answer OR research
  -> research: decompose -> bounded parallel local/web retrieval -> RRF
  -> answer synthesis node
  -> optional verifier
  -> persist user/assistant messages
  -> result or stream

Verifier 默认沿用当前 disabled 配置。Answer、Memory、Verifier 和 RRF 都不是 Agent。Planning Agent 在兼容重构完成后通过独立产品 spec 引入。

## Contracts

ChatInput:
- SessionID optional string; empty means server creates an ID.
- Message required non-blank after protocol-level validation.

ChatResult:
- SessionID
- Answer
- CreatedAt

AssistantWorkflow:
- Run(ctx, ChatInput) (ChatResult, error)
- Stream(ctx, ChatInput) (TokenStream, error)

TokenStream 必须支持 Next/Close 或等价 channel contract，且由 request context 取消。公开契约不暴露 Hertz SSE writer 或 Eino stream 类型。

Repository boundaries:

- ConversationStore: find/create session, append message, load recent context, load/update summary.
- KnowledgeStore: keyword/vector search and document writes.
- Model clients and embedding live behind Eino adapter.
- LightRAG/Web Search adapters返回中性 EvidenceSourceResult；领域层决定降级与融合。

## Behavior Preservation Matrix

| Behavior | Java baseline | Go target |
|---|---|---|
| empty sessionId | create UUID/session | same |
| direct route | main model direct reply | same observable response |
| planning failure | conservative evidence route | same |
| embedding unavailable | keyword fallback | same |
| LightRAG unavailable | local pgvector/keyword fallback | same |
| web search disabled | skip provider | same |
| verifier disabled | return synthesized answer | same |
| persistence | save user then assistant | same ordering |
| stream disconnect | Reactor cancellation behavior to characterize | request context cancellation, contract-equivalent |
| memory summary cursor | current repeated-prefix behavior is debt | preserve initially; fix only under separate accepted criterion |

## Migration Sequence

1. Characterize Java HTTP/SSE, persistence order, fallback and error behavior.
2. Add minimal Go module and direct-chat vertical slice.
3. Add PostgreSQL conversation adapter against existing schema.
4. Add Eino orchestrator/answer adapter with fake-model tests.
5. Add knowledge retrieval adapters and deterministic RRF.
6. Add SSE streaming and cancellation.
7. Run contract comparison and document intentional differences.
8. Add CI and local run docs.
9. Cut over only after human review; remove Java later.

## Dependency Decisions

- Go 1.23: 当前开发环境可复现构建的最低版本；工具链升级不与架构迁移绑定。
- Eino stable v0.9 line; do not adopt v0.10 alpha during migration.
- Hertz for HTTP/SSE because it provides native binding, middleware and SSE support.
- pgx/v5 because the service is PostgreSQL-only; pgvector-go for vector values.
- No ORM, DI framework, message queue, Redis or custom workflow engine.
- Existing React/Vite frontend remains unchanged.

## Failure and Rollback

- Startup fails if required DB/model config is missing.
- Explicitly disabled providers become Noop capabilities with observable skipped status.
- Provider timeouts propagate as typed dependency errors; fallback is allowed only where spec says so.
- Rollback does not require data migration: restore Java process/route because schema remains compatible.
- No destructive SQL appears in the migration branch.

## Security

- Secrets only from environment.
- Logs redact prompts, API keys, authorization headers and full user messages.
- HTTP request size limit and message length limit are set before model invocation.
- Outbound URLs are config allowlisted; user input cannot choose arbitrary internal endpoints.

## Observability

Minimum fields: request_id, route, node, provider, result, latency_ms, fallback_reason.
Metrics: request count, route count, provider failure, fallback count, node latency, stream cancellation.
No userID/sessionID as metric labels.

## Review Gates

- Gate 1: approve spec/plan/test-plan.
- Gate 2: approve Java characterization report and any intentional contract delta.
- Gate 3: approve Go cutover after PRE_MERGE checks.
- Gate 4: approve Java deletion after rollout evidence.
