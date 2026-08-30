# XiaoLanHe

小蓝盒后端使用 Go 1.23、CloudWeGo Hertz/Eino 和 PostgreSQL/pgvector。保留现有 React、REST/SSE 契约与历史数据结构。

## 当前架构

```text
Hertz HTTP/SSE
  -> Presenter（协议校验与 DTO）
  -> Chat UseCase（会话、调用顺序、完整回答落库）
  -> Orchestrator（路由与检索计划）
  -> Research（有界并发检索与确定性融合）
  -> Answer Node（直接回答或证据合成）
  -> consumer-owned ports
       -> Eino/OpenAI-compatible 模型
       -> pgx/PostgreSQL
```

Orchestrator Agent 负责意图与主路由，Research Agent 负责查询分解和数据源选择；检索执行器将本地 pgvector/关键词结果与 Web 结果做最多 4 路并发、最多 6 个查询的确定性融合。Answer、Embedding 和 RRF 是普通节点。个性化 Planning Agent 仍属于独立产品需求。

## 运行

需要 Go 1.23、PostgreSQL + pgvector，以及 OpenAI-compatible 模型密钥。

```bash
export XLH_DATABASE_URL='postgres://xlh:password@localhost:5432/xiaolanhe?sslmode=disable'
export XLH_AI_API_KEY='...'
export XLH_AI_BASE_URL='https://dashscope.aliyuncs.com/compatible-mode/v1'
export XLH_AI_CHAT_MODEL='qwen3.5-flash'
export XLH_AI_EMBEDDING_MODEL='text-embedding-v4'
export XLH_AI_TIMEOUT='60s'
export XLH_SEARCH_ENABLED='true'
export SEARXNG_BASE_URL='http://127.0.0.1:8080'
go run ./cmd/xiaolanhe
```

服务默认监听 `:8088`，提供：

- `GET /healthz`
- `POST /api/chat/message`
- `POST /api/chat/stream`
- `POST /api/knowledge/documents`
- `GET /api/knowledge/search`
- `GET /api/search/web`
- `GET /api/system/ping`

首次建库执行 `psql "$XLH_DATABASE_URL" -f migrations/001_initial_schema.sql`。从仓库根目录启动，prompt 和外部服务地址均可通过 `XLH_*` 环境变量覆盖。

## Render 部署

仓库根目录的 `render.yaml` 会创建一个 Go Web Service 和一个支持 pgvector 的 PostgreSQL。React 会在镜像构建时编译，并由同一个 Go 服务提供，因此页面和 API 共用一个 `https://<service>.onrender.com` 地址。

1. 在 Render 选择 **New Blueprint** 并连接此仓库。
2. 部署时填写 `XLH_AI_API_KEY`。
3. 创建完成后直接打开 Render 分配的 `onrender.com` 地址。

服务启动时会幂等执行 `migrations/001_initial_schema.sql`。默认关闭 Web 搜索；接入 SearXNG 后再设置 `XLH_SEARCH_ENABLED=true` 和 `SEARXNG_BASE_URL`。

## 验证

```bash
go test ./...
go test -race ./internal/usecase ./internal/entry
go vet ./...
```

迁移决策、任务和未完成门禁见 [`specs/20260830-clean-architecture-refactor/`](specs/20260830-clean-architecture-refactor/)。
