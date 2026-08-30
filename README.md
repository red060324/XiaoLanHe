# XiaoLanHe

小蓝盒正在从 Java/Spring AI 增量迁移到 Go。当前分支包含可运行的直聊纵向切片，保留现有 React、REST/SSE 契约和 PostgreSQL 表；尚未切换默认后端。

## 当前架构

```text
Hertz HTTP/SSE
  -> Presenter（协议校验与 DTO）
  -> Chat UseCase（会话、调用顺序、完整回答落库）
  -> consumer-owned ports
       -> Eino/OpenAI-compatible 模型
       -> pgx/PostgreSQL
```

目标 Agent 只有需要自主决策的三个角色：Orchestrator 负责意图和任务控制，Research 负责查询分解和数据源选择，Planning 负责后续个性化决策。Answer、Memory、RRF、Verifier 是节点或领域能力，不包装成 Agent。当前批次只落地直聊 Answer 节点；Research/RAG 是下一批，Planning 属于独立 follow-up。

## 运行 Go 直聊切片

需要 Go 1.23、可访问现有 V1-V3 schema 的 PostgreSQL，以及 OpenAI-compatible 模型密钥。

```bash
export XLH_DATABASE_URL='postgres://xlh:password@localhost:5432/xiaolanhe?sslmode=disable'
export XLH_AI_API_KEY='...'
export XLH_AI_BASE_URL='https://dashscope.aliyuncs.com/compatible-mode/v1'
export XLH_AI_CHAT_MODEL='qwen3.5-flash'
export XLH_AI_TIMEOUT='60s'
go run ./cmd/xiaolanhe
```

服务默认监听 `:8088`，提供：

- `GET /healthz`
- `POST /api/chat/message`
- `POST /api/chat/stream`

从仓库根目录启动，或通过 `XLH_DIRECT_PROMPT_FILE` 指定直聊 prompt 文件。

## 验证

```bash
go test ./...
go test -race ./internal/usecase ./internal/entry
go vet ./...
```

迁移决策、任务和未完成门禁见 [`specs/20260830-clean-architecture-refactor/`](specs/20260830-clean-architecture-refactor/)。
