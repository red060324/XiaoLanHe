# 小蓝盒整洁架构重构 Spec

Status: APPROVED (2026-08-30)
Owner: red060324
Branch: codex/clean-architecture-refactor
Source: codex://threads/01a0526f-1221-7b90-ba86-2931581c744c
Baseline: master@49004fd55807085939508220fe01e88846fdffd2

## Goal

把现有 Java/Spring AI 骨架迁移为可验证、可回滚的 Go AI 应用，同时保留现有前端、HTTP/SSE 契约和 PostgreSQL/pgvector 数据。重构目标是清晰边界和行为可测，不是增加 Agent 数量。

## Decision

采用可验证的纯 Go 迁移：

1. 在同一仓库新增 Go 后端纵向切片。
2. 先覆盖直接对话，再覆盖检索增强与流式响应。
3. 迁移期间用同一份 API 契约和数据库 schema 做静态/自动化对比。
4. 对外接口、配置、迁移脚本和测试完成 Go 等价实现后，在本分支删除 Java 模块与 Maven 构建。

技术方向：

- Go 1.23（当前开发环境可复现构建的最低版本；后续升级独立验证）。
- CloudWeGo Eino v0.9 稳定线：模型、Tool、Graph/Chain、streaming 与 callbacks。
- CloudWeGo Hertz：HTTP、binding/validation、SSE。
- pgx/v5 + pgvector-go：复用现有 PostgreSQL/pgvector。
- Go 标准库 testing、slog、context；不增加通用 utils、DI 容器或自研 Agent 框架。

## Current-state Evidence

| Evidence | Classification | Conclusion |
|---|---|---|
| xiaolanhe-web/.../ChatController.java | local good pattern | HTTP 入口薄，现有 /api/chat/message 与 /api/chat/stream 契约应保留 |
| xiaolanhe-agent/.../ChatService.java | migration debt | 同时承担会话、编排、检索、流式、持久化和校验，需拆到 Presenter/UseCase/Domain/Adapter |
| xiaolanhe-agent/.../MainAgentService.java | mixed | 模型规划与规则 fallback 有价值；模型调用、JSON 解析和领域计划生成需要分边界 |
| xiaolanhe-search/.../DefaultSearchAgentService.java | migration debt | 查询分解、数据源 IO、融合排序、日志混在一个 Service，且多查询串行 |
| xiaolanhe-rag/.../KnowledgeDocumentService.java | mixed | 关键词降级和维度校验可保留；切块、embedding、检索与融合应拆开 |
| ConversationRepository.java | mixed | SQL 与 schema 可复用；find-or-create 会话语义和增量摘要游标需要单独刻画 |
| V1__initial_schema.sql | local good pattern | 迁移期复用现有表和 vector(1536)，禁止破坏性变更 |
| XiaolanheWebApplicationTests.java | migration debt | 只有 contextLoads，无法证明重构等价 |

## Scope

### In scope

- 保持现有聊天 REST 与 SSE 边界兼容。
- 保持现有会话、消息、知识文档和向量数据兼容。
- 把依赖方向收敛为 Entry -> Presenter -> UseCase -> Domain，外部系统由 Adapter 实现消费方接口。
- 用 Eino 表达现有主控路由、Research 检索和答案合成流程。
- Memory 与 Verifier 保持普通节点/能力，不额外包装为 Agent。
- 为当前行为补 characterization、unit、integration 和 API contract tests。
- 提供本地启动、配置说明、架构图和 CI。

### Non-goals

- 不在本次重构新增第四个 Agent、插件平台、MCP、复杂 Skills 市场或长期画像新产品能力。
- 不改变前端 UI。
- 不新增微服务、消息队列、缓存层或 Kubernetes 配置。
- 不迁移未接入公开主链路的 RocketMQ、Redis、MinIO 骨架。
- 不为了“整洁”给每个 struct 建接口；只有跨层或外部世界边界使用窄接口。

## Acceptance Criteria

AC1. master 不受影响，新工作位于 codex/clean-architecture-refactor。

AC2. 现有 POST /api/chat/message 的请求和响应字段保持兼容：
- request: sessionId, message
- response: sessionId, answer, createdAt
- 空 message 的协议行为由 characterization test 固化后保持一致。

AC3. POST /api/chat/stream 对现有前端保持 wire compatibility；token 顺序、终止和断连取消由 contract test 证明。

AC4. Go 代码不存在 Entry/Presenter 直连 PostgreSQL、模型、LightRAG 或 Web Search；UseCase 不依赖 Hertz/Eino/pgx DTO。

AC5. 目标架构只把需要自主决策的能力建模为 Agent：
- Orchestrator Agent: 意图、路由与任务控制
- Research Agent: 查询分解与数据源选择
- Planning Agent: 个性化决策；属于 FOLLOW_UP，不进入兼容重构首批
Answer、Memory、RRF、Verifier 为模型节点、确定性节点或领域能力，不包装成 Agent。

AC6. PostgreSQL schema 与历史数据可直接读取；迁移不删除、不重命名现有表或字段。

AC7. 本地知识不可用或 embedding 失败时保留关键词降级；Web Search 禁用时不发请求；依赖失败不会被伪装成成功空答案。

AC8. 并发有界：子查询最多 6 个，外部检索并发上限 4，所有下游调用使用 context timeout/cancel；不使用 time.Sleep 重试。

AC9. PRE_MERGE 测试全部通过，并生成历史契约/Go 对比报告；未完成项明确标记，不以“能编译”替代行为证明。

AC10. Go 服务具备 /healthz、结构化日志和请求/节点耗时；日志 label 不包含完整用户问题或高基数用户 ID。

AC11. 合并候选中不存在 `.java`、`pom.xml` 或 Java 运行时入口；默认后端、数据库迁移和 CI 全部由 Go 所有。

## Assumptions and Clarify Round 1

1. 默认保留现有前端与 API 契约；若允许前端同步升级，可单独设计更丰富的 SSE event schema。
2. 用户于 2026-08-30 明确要求最终只保留 Go、不保留 Java；删除动作以公开接口和数据兼容测试通过为前置条件。
3. 默认 Skills、个性化 Planning Agent 和完整 Eval 平台属于 FOLLOW_UP，不夹带进结构重构。

以上三项已于 2026-08-30 获得用户确认。

## Architecture Compliance

- Contract ownership: Presenter 拥有 HTTP DTO 适配；UseCase 拥有 ChatInput/ChatResult 和所消费的最小依赖接口；Domain 拥有 RoutePlan、Evidence、Conversation 等业务类型。
- Protocol boundary: Hertz request/response/SSE 类型只出现在 entry/presenter；Eino message、pgx row、LightRAG/SearXNG payload 不进入 UseCase public contract。
- Dependencies: 组合根一次性构造必需依赖并 fail fast。显式禁用的 Web Search/Verifier 使用 capability state 或 Noop，不用运行时 nil。
- Domain shape: 简单 sessionID/message 直接使用值或小型业务类型，不创建 Getter-only Input。跨字段请求校验由 ChatInput 承担。
- Error/absence: not-found、disabled、empty evidence 和 provider failure 分开建模；禁止 nil,nil 或空字符串同时表示成功与失败。
- Serialization: HTTP JSON 属于 Presenter；数据库 JSONB 属于 PostgreSQL adapter；模型结构化输出 JSON 属于 Eino adapter。Domain 不直接依赖 wire codec。
- Storage lifecycle: 本阶段不新增表或 key；复用现有 schema。新增 trace/eval 持久化需另开 spec，明确 retention。
- Concurrency/backpressure: Eino Graph 只执行有界节点；检索 fan-out 上限 4；SSE 使用 request context 传播取消。
- Rollout: 使用隔离数据库做契约回放；回滚为恢复迁移前 Git revision，不执行反向数据迁移。
- Observability: 记录 route、node、provider、result、latency；禁止记录密钥、完整 prompt、完整用户消息。
- Framework ownership: Eino/Hertz/pgx 都是 adapter 技术细节，不反向定义领域接口。
- Touched debt: 本次只收敛聊天主链路；管理端知识写入、长期画像和未使用 tool_call_log 不顺手扩展。
- Go generics/optional libraries: 不引入第三方 Result/Optional；使用 Go 的 (T, error)、显式 found bool 或命名结果类型，避免为样式增加依赖。

## Traceability

| Planned change | Requirement |
|---|---|
| Go chat vertical slice | AC2, AC4 |
| Hertz SSE adapter | AC3, AC8 |
| Eino workflow adapter | AC4, AC5 |
| pgx/pgvector adapters | AC6, AC7 |
| bounded retrieval and cancellation | AC7, AC8 |
| tests and parity report | AC9 |
| health/log/timing | AC10 |
