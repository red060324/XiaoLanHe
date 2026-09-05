# XiaoLanHe

小蓝盒是一个面向游戏玩家的内容、攻略、优惠购买和智能决策平台。后端使用
Go 1.23、CloudWeGo Hertz/Eino 与 PostgreSQL，前端使用 React；可选生产能力包括
Redis + Lua + RocketMQ 限时抢购，以及官方 HKUDS LightRAG 驱动的分层
Multi-Agent 助手。

## 已实现能力

- 账号：注册、登录、Cookie 会话、角色与用户自主管理的 Assistant 偏好。
- 游戏目录：游戏、版本、地区价格、搜索与已购权益。
- 社区：帖子、评论、反应、编辑/删除和管理员治理。
- 优惠与订单：优惠活动、每人限领、订单价格复核、幂等沙箱支付和权益发放。
- 限时抢购：Redis Lua 原子库存预扣与一人一单，RocketMQ 事务消息和至少一次消费，
  PostgreSQL 最终幂等/库存防线，以及恢复、过期和库存释放 worker。
- 基础助手：Router Node、Research Agent、Answer Node 和本地 PostgreSQL/pgvector
  知识检索，作为高级模式关闭时的兼容路径。
- 高级助手：Game Copilot supervisor 按 Skill 调度独立 Research 与 Planning
  Agent，执行严格结构化契约、共享预算、证据 ID 校验、只读工具和取消传播。
- 分层记忆：最近 8 条消息、带 CAS 水位的滚动摘要，以及只从认证 UserID 读取的
  typed 用户画像。
- 官方 LightRAG：高级模式下通过固定私网 HTTP API 查询和异步管理文档；知识域
  只使用 LightRAG 的 `JsonKVStorage`、`NanoVectorDBStorage`、`NetworkXStorage`、
  `JsonDocStatusStorage`，不使用独立 PostgreSQL 或应用侧双写/同步表。
- 质量与安全：结构化安全日志、LightRAG 对抗合同测试、8-case 版本化确定性评测。

Assistant 永久只读。领券、下单、支付、秒杀、发帖和评论只能由用户通过普通
认证 HTTP 接口触发，不会作为 Agent 工具暴露。

## 架构

```text
Browser -> Hertz HTTP/SSE -> module UseCases -> PostgreSQL
                         |-> Redis Lua -> RocketMQ -> order transaction
                         |-> Router / Query Planner Nodes
                              -> Game Copilot
                                   -> Research Agent -> LightRAG/catalog/forum/Web
                                   -> Planning Agent -> catalog/entitlement/constraint reads
                              -> Answer Node

Official LightRAG (one service replica, persistent WORKING_DIR)
  -> JsonKV + NanoVectorDB + NetworkX + JsonDocStatus
  -> external LLM and embedding APIs
```

业务 PostgreSQL 继续保存用户、会话/摘要、画像、目录、社区、优惠和订单。
“知识全部使用 LightRAG”只针对高级知识域；LightRAG 内置文件存储适合受控小语料和
单服务实例，不应被描述为多副本、高可用或大规模语料方案。完整边界见
[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## 本地运行

1. 准备 Go 1.23、Node 22、PostgreSQL + pgvector，以及兼容 OpenAI API 的模型。
2. 复制 `.env.example` 并替换占位凭证。
3. 可用 Docker 启动本地业务中间件：

```bash
cp .env.example .env
make middleware-config
make middleware-up
```

4. 启动应用：

```bash
set -a
. ./.env
set +a
go run ./cmd/xiaolanhe
```

默认监听 `:8088`。React 开发服务器可在
`frontend/xiaolanhe-web` 中执行 `npm run dev`；生产镜像会构建并由 Go 服务托管前端。

### 启用官方 LightRAG 高级模式

LightRAG 需要自己的 API key，以及 LLM/Embedding 服务凭证；它不需要独立
PostgreSQL：

```bash
export XLH_LIGHTRAG_API_KEY='replace-with-a-distinct-private-key-of-32-plus-chars'
export XLH_LIGHTRAG_LLM_API_KEY='...'
export XLH_LIGHTRAG_EMBEDDING_API_KEY='...'
export XLH_METRICS_TOKEN='replace-with-a-distinct-32-character-operator-token'
make lightrag-config
make lightrag-up
export XLH_ADVANCED_AI_ENABLED=true
go run ./cmd/xiaolanhe
```

应用在高级模式启动时会验证 LightRAG 版本、API 版本、Gunicorn/双 worker
运行拓扑、workspace、工作目录、四种存储类型和恢复围栏；不匹配或服务不可用时
fail closed，不会偷偷回退到 PostgreSQL 知识库。
管理员可以在账号页提交、跟踪、分页查看和精确删除 LightRAG 文档。

一次性迁移旧知识默认只 dry-run：

```bash
go run ./cmd/import-knowledge --limit 20
go run ./cmd/import-knowledge --execute --limit 20 --after-id 0
```

命令每次最多处理 100 份文档，可用 `--after-id` 续跑；它不会把 LightRAG ID
写回 PostgreSQL，也不会启动持续同步。

### 启用 Redis + RocketMQ 限时抢购

```bash
export XLH_FLASH_SALE_ENABLED=true
export XLH_REDIS_URL='redis://:password@127.0.0.1:6379/0'
export XLH_ROCKETMQ_NAMESERVERS='127.0.0.1:9876'
export XLH_METRICS_TOKEN='replace-with-a-distinct-32-character-operator-token'
go run ./cmd/xiaolanhe
```

本地 Compose 是集成环境，不是 Redis/RocketMQ 的高可用生产拓扑。生产部署必须使用
私网、认证、持久化、监控和经过演练的备份/恢复。

## 主要接口

- 健康：`GET /healthz`、`GET /readyz`。
- 运行指标：`GET /metrics`，仅配置 `XLH_METRICS_TOKEN` 时注册，并要求独立的
  `Authorization: Bearer <token>` 运维凭证；高级 AI 或秒杀开启时该 token 必填。
- 账号与画像：`/api/auth/*`、`GET /api/me`、
  `GET/PUT/DELETE /api/me/assistant-profile`。
- 目录、社区、优惠、订单和限时抢购：`/api/games`、`/api/community/*`、
  `/api/deals`、`/api/coupons/*`、`/api/orders/*`、`/api/flash-sales/*`。
- 助手：`POST /api/chat/message`、`POST /api/chat/stream`。
- 高级知识：`GET /api/knowledge/search`、`POST /api/knowledge/documents`、
  `GET /api/admin/knowledge/tracks/:trackId`、
  `GET /api/admin/knowledge/documents`、
  `DELETE /api/admin/knowledge/documents/:documentId`。管理接口要求 admin；写接口还要求
  同源校验。

## 验证

```bash
make verify
make ci BASE_REF=origin/master
make eval
```

`make eval` 运行固定 transcript 上的离线确定性门禁，输出 baseline/candidate 的
路由、facet、Recall@8、引用、画像一致性、调用预算和 fixture latency。它不调用
真实模型，也不等价于真实 LightRAG 效果；凭证化的真实模型/检索评测属于 rollout。
应用指标覆盖 Agent 路由/Skill/调用/停止原因、模型请求和 provider 实际返回的
token usage、LightRAG API/查询/管线/托管文档状态及摘要刷新。`usage_reported=false`
表示 provider 未返回 token 统计，系统不会估算。LightRAG 卷字节、进程内存和宿主
容量必须由容器平台或宿主监控；当前版本没有运行时 OpenTelemetry exporter/spans。

部署拓扑、备份、恢复和回滚见
[`docs/guidance/public-deployment.md`](docs/guidance/public-deployment.md)。高级 AI
权威规格见
[`specs/20260904-advanced-ai-architecture/`](specs/20260904-advanced-ai-architecture/)。
