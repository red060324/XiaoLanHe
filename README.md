# XiaoLanHe

小蓝盒使用 Go 1.23、CloudWeGo Hertz/Eino、React 和 PostgreSQL/pgvector，按业务模块组织整洁架构。目前已包含账号、游戏目录、社区、优惠券、沙箱订单和只读游戏助手。

## 当前架构

```text
Hertz HTTP/SSE
  -> Presenter（协议校验与 DTO）
  -> Chat UseCase（会话、调用顺序、完整回答落库）
  -> Router Node（一次结构化路由）
       DIRECT/CLARIFY -> Answer Node
       EVIDENCE -> Research Agent（有界 ReAct 工具循环）
                    -> 本地知识 / 游戏目录 / 社区论坛 / 可选公开 Web
                  -> Answer Node（证据合成）
  -> consumer-owned ports
       -> Eino/OpenAI-compatible 模型
       -> pgx/PostgreSQL
```

Router 和 Answer 都是单次、有界的模型节点。只有 Research 是 Agent：它能读取工具结果、改写查询并继续调用下一个只读工具。每次请求默认最多 6 次模型迭代、8 次工具调用、总计 30 秒；取消会传递到模型和工具。当前没有任何领券、下单、支付或社区写入工具。

## 运行

需要 Go 1.23、PostgreSQL + pgvector，以及 OpenAI-compatible 模型密钥。

```bash
export XLH_DATABASE_URL='postgres://xlh:password@localhost:5432/xiaolanhe?sslmode=disable'
export XLH_AI_API_KEY='...'
export XLH_AI_BASE_URL='https://dashscope.aliyuncs.com/compatible-mode/v1'
export XLH_AI_CHAT_MODEL='qwen3.5-flash'
export XLH_AI_EMBEDDING_MODEL='text-embedding-v4'
export XLH_AI_TIMEOUT='60s'
export XLH_RESEARCH_TIMEOUT='30s'
export XLH_RESEARCH_TOOL_TIMEOUT='10s'
export XLH_RESEARCH_MAX_ITERATIONS='6'
export XLH_RESEARCH_MAX_TOOL_CALLS='8'
export XLH_SEARCH_ENABLED='true'
export SEARXNG_BASE_URL='http://127.0.0.1:8080'
export XLH_COOKIE_SECURE='false' # 仅本地 HTTP
export XLH_PUBLIC_ORIGIN='http://localhost:5173'
go run ./cmd/xiaolanhe
```

服务默认监听 `:8088`，提供：

- `GET /healthz`
- `GET /readyz`
- `POST /api/auth/register`、`POST /api/auth/login`、`POST /api/auth/logout`
- `GET /api/me`
- `GET /api/games`、`GET /api/games/:slug`
- `POST /api/admin/games`、`PUT /api/admin/games/:id`（admin）
- `GET /api/community/posts`、帖子/评论/反应写接口
- `GET /api/deals`、`POST /api/coupons/:code/claims`
- `POST /api/orders`、`GET /api/orders`、`GET /api/orders/:orderNo`
- `POST /api/orders/:orderNo/payments/sandbox`
- `POST /api/chat/message`
- `POST /api/chat/stream`
- `POST /api/knowledge/documents`（admin）
- `GET /api/knowledge/search`
- `GET /api/search/web`
- `GET /api/system/ping`

服务启动时会在 PostgreSQL advisory lock 下按文件名执行并记录 `migrations/*.sql`，不再手工执行可变 schema 文件。本地/demo 管理员和示例游戏使用幂等 seed 命令：

```bash
export XLH_SEED_ADMIN_PASSWORD='replace-with-a-strong-password'
go run ./cmd/seed
```

可用 `XLH_SEED_ADMIN_USERNAME` 覆盖默认用户名 `admin`。seed 会更新该账号密码，请勿对未知生产账号运行。

## Render 部署

仓库根目录的 `render.yaml` 会创建一个 Go Web Service 和一个支持 pgvector 的 PostgreSQL。React 会在镜像构建时编译，并由同一个 Go 服务提供，因此页面和 API 共用一个 `https://<service>.onrender.com` 地址。

1. 在 Render 选择 **New Blueprint** 并连接此仓库。
2. 部署时填写 `XLH_AI_API_KEY`；如果修改服务名，同时把 `XLH_PUBLIC_ORIGIN` 改成实际 `https://<service>.onrender.com`。
3. 创建完成后直接打开 Render 分配的 `onrender.com` 地址。

服务启动时会幂等执行有版本记录的迁移。默认关闭 Web 搜索；接入 SearXNG 后再设置 `XLH_SEARCH_ENABLED=true` 和 `SEARXNG_BASE_URL`。
Render 会提供可直接访问的 `onrender.com` 子域名，不购买自定义域名也能使用。仓库默认的免费方案仅用于演示：免费数据库 30 天后到期、无托管备份；长期部署应换成持久 PostgreSQL 方案并配置备份。Docker 自托管、环境变量、备份、回滚和 smoke 步骤见
[`docs/guidance/public-deployment.md`](docs/guidance/public-deployment.md)。

## 验证

```bash
make verify
make ci BASE_REF=origin/master
```

开发默认遵循“需求澄清 → spec/测试计划评审 → 实现 → 本地/CI 验证 →
readiness → rollout”的流程。入口文档：

- [`AGENTS.md`](AGENTS.md)
- [`docs/guidance/development-lifecycle.md`](docs/guidance/development-lifecycle.md)
- [`docs/guidance/spec-driven-development.md`](docs/guidance/spec-driven-development.md)
- [`docs/guidance/local-verification.md`](docs/guidance/local-verification.md)
- [`docs/guidance/public-deployment.md`](docs/guidance/public-deployment.md)

历史迁移决策见 [`specs/20260830-clean-architecture-refactor/`](specs/20260830-clean-architecture-refactor/)，当前产品建设计划见
[`specs/20260831-mature-game-platform/`](specs/20260831-mature-game-platform/)。
