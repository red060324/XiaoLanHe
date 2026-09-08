# XiaoLanHe

小蓝盒是一个面向游戏玩家的内容、攻略、优惠购买和智能决策平台。后端使用
Go 1.23、CloudWeGo Hertz/Eino 与 MySQL 8.4/InnoDB，前端使用 React；知识能力
统一使用官方 HKUDS LightRAG，另可选启用 Redis + Lua + RocketMQ 限时抢购与分层
Multi-Agent 编排。

## 已实现能力

- 账号：注册、登录、Cookie 会话、角色与用户自主管理的 Assistant 偏好。
- 游戏目录：游戏、版本、地区价格、搜索与已购权益。
- 社区：帖子、评论、反应、编辑/删除和管理员治理。
- 优惠与订单：优惠活动、每人限领、订单价格复核、幂等沙箱支付和权益发放。
- 限时抢购：Redis Lua 原子库存预扣与一人一单，RocketMQ 事务消息和至少一次消费，
  MySQL 最终幂等/库存防线，以及恢复、过期和库存释放 worker。
- 基础助手：Router Node、Research Agent 与 Answer Node；知识检索始终通过官方
  LightRAG，不存在本地 SQL/pgvector 兼容路径。
- 高级助手：Game Copilot supervisor 按 Skill 调度独立 Research 与 Planning
  Agent，执行严格结构化契约、共享预算、证据 ID 校验、只读工具和取消传播。
- 分层记忆：最近 8 条消息、带 CAS 水位的滚动摘要，以及只从认证 UserID 读取的
  typed 用户画像；这些业务数据持久化在 MySQL。
- 官方 LightRAG：所有模式通过固定私网 HTTP API 查询和异步管理文档；
  `JsonKVStorage`、`NetworkXStorage`、`JsonDocStatusStorage` 保存在持久
  `WORKING_DIR`，`MilvusVectorDBStorage` 只保存 LightRAG 创建的块、实体和关系向量。
- 质量与安全：结构化安全日志、LightRAG 对抗合同测试、8-case 版本化确定性评测。

Assistant 永久只读。领券、下单、支付、秒杀、发帖和评论只能由用户通过普通
认证 HTTP 接口触发，不会作为 Agent 工具暴露。

## 架构

```text
Browser -> Hertz HTTP/SSE -> module UseCases -> MySQL 8.4 / InnoDB
                         |-> Redis Lua -> RocketMQ -> MySQL order transaction
                         |-> Router / Query Planner Nodes
                              -> Game Copilot
                                   -> Research Agent -> LightRAG/catalog/forum/Web
                                   -> Planning Agent -> catalog/entitlement/constraint reads
                              -> Answer Node

Official LightRAG 1.5.7 (one service replica, persistent WORKING_DIR)
  -> JsonKV files + NetworkX files + JsonDocStatus files
  -> MilvusVectorDBStorage -> Milvus 2.6.11 standalone
  -> external LLM and embedding APIs
```

MySQL 是账号、会话、画像、目录、社区、优惠、订单、支付和秒杀持久状态的业务
事实源。LightRAG 是所有知识读写的唯一边界；Milvus 只是 LightRAG 管理的向量
投影，Go 应用不会直接查询或写入 Milvus。由于 KV、graph 和 document status 仍是
LightRAG 持久文件，本版本只支持一个 LightRAG 服务副本；standalone Milvus 也不代表
高可用。完整边界见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。
生产 MySQL DSN 必须包含 `tls=xlh-verified`，并通过
`XLH_DATABASE_TLS_CA_FILE` 与 `XLH_DATABASE_TLS_SERVER_NAME` 验证证书和主机名；
若启用 mTLS，`XLH_DATABASE_TLS_CERT_FILE`/`XLH_DATABASE_TLS_KEY_FILE` 必须成对。
`XLH_DATABASE_ALLOW_INSECURE=true` 只允许用于本地测试。
LightRAG 传输同样默认 fail closed：`XLH_LIGHTRAG_BASE_URL` 默认为
`https://127.0.0.1:9621`，生产必须使用 HTTPS 并保持
`XLH_LIGHTRAG_ALLOW_INSECURE=false`。只有本地或其他受控环境才能显式设为 `true`
后连接 HTTP；明文传输可能泄露 LightRAG API key 和查询正文。

## 本地运行

1. 准备 Go 1.23、Node 22、Docker，以及兼容 OpenAI API 的模型。
2. 复制 `.env.example`，替换占位凭证与 fence 路径，然后加载本地配置。
3. 启动 MySQL 8.4、Redis 7.4 和 RocketMQ 5.3.2 集成环境：

```bash
cp .env.example .env
# 编辑 .env；host-run 应用须把两个 fence 路径设为同一个绝对宿主目录。
set -a
. ./.env
set +a
make middleware-config
make middleware-up
```

4. 为知识服务配置 LightRAG、Milvus、etcd 和 MinIO。首次空环境先生成并记录
   deployment generation 与 fence contract；已有 NanoVectorDB 工作区不能走此空库流程：

```bash
export XLH_LIGHTRAG_API_KEY='replace-with-a-distinct-private-key-of-32-plus-chars'
export XLH_LIGHTRAG_LLM_API_KEY='...'
export XLH_LIGHTRAG_EMBEDDING_API_KEY='...'
# 仅本地 Compose 的 loopback 明文端点需要此显式 override；生产不要设置为 true。
export XLH_LIGHTRAG_ALLOW_INSECURE=true
export XLH_MILVUS_MINIO_USER='replace-with-a-local-user'
export XLH_MILVUS_MINIO_PASSWORD='replace-with-a-local-password'
export XLH_MILVUS_ROOT_PASSWORD='replace-with-a-distinct-root-password'
export XLH_MILVUS_TOKEN='xlh_lightrag:replace-with-a-distinct-runtime-password'
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION='local-empty-v1'
export XLH_LIGHTRAG_ATTEMPT_ID="bootstrap-$(date +%Y%m%d%H%M%S)"
export XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR="$(pwd -P)/.state/lightrag-fence"
export XLH_LIGHTRAG_REBUILD_FENCE_DIR="$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR"
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR="$(pwd -P)/.state/lightrag-writer-evidence"
# 仅非 root；可省略，bootstrap 默认使用这个非 0 primary GID。
# export XLH_LIGHTRAG_SHARED_GID=$(id -g)
# 仅 root；替换为专用的非 0 数字 GID。
# export XLH_LIGHTRAG_SHARED_GID=replace-with-dedicated-nonzero-gid
export XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers
make lightrag-static
bootstrap_output=$(make --no-print-directory lightrag-bootstrap-empty)
printf '%s\n' "$bootstrap_output"
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_DEPLOYMENT_GENERATION=//p' | tail -n 1
)
export XLH_LIGHTRAG_ATTEMPT_ID=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_ATTEMPT_ID=//p' | tail -n 1
)
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=//p' | tail -n 1
)
export XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=//p' | tail -n 1
)
export XLH_LIGHTRAG_SHARED_GID=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_SHARED_GID=//p' | tail -n 1
)
test -n "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION"
test -n "$XLH_LIGHTRAG_ATTEMPT_ID"
test -n "$XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256"
test -n "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"
[[ "$XLH_LIGHTRAG_SHARED_GID" =~ ^[1-9][0-9]{0,9}$ ]]
(( XLH_LIGHTRAG_SHARED_GID <= 2147483647 ))
make lightrag-up
```

5. 将 bootstrap 输出的四个证据值和 `XLH_LIGHTRAG_SHARED_GID` 更新到当前 shell
   并持久化到后续部署配置，然后启动应用：

```bash
go run ./cmd/xiaolanhe
```

`make lightrag-up` 是唯一受支持的 steady-state Compose 启动入口：它在宿主机先
验证绝对 fence 路径和 generation/contract；容器入口先持有 `serving.lock` 共享租约，
再次验证 fence，再在整个 Gunicorn 进程组生命周期内监督并保留租约。控制器必须先
独占该锁再取得 `rebuild.lock`，因此运行中的 steady writer 与 fence 变更不能竞态。
不要使用裸 `docker compose up`；一次性 `milvus-init`/`lightrag-bootstrap` 位于
`bootstrap` profile，正常重启不会再次执行。Milvus server 使用 root 密码完成认证
配置，只有初始化 job 获得 root token；LightRAG 只获得独立 runtime token，且该
身份不能建库或管理用户。

bootstrap 会用固定 LightRAG 镜像运行一次有界 root ownership 初始化，只处理 fence
与 writer-evidence bind mount：fence 使用 `1000:<shared-gid>`，writer evidence 使用
`<operator-uid>:1000`。初始化器会先通过 no-follow descriptor 完整只读验证两棵树并
拒绝意外节点、alias、hardlink 和超限内容，并通过两个稳定的根目录 inode 排除并发
initializer，之后才将已验证目录临时冻结为 `root:root`/`0700`。可变普通文件会复制到
私有新 inode，仅新 inode 接受 chown/chmod，随后 fsync 并原子替换；因此预持 FD 或
后建外部 hardlink 指向的旧 inode 不会被特权修改。两把 lock 只在缺失时安全创建一次；
已有 lock 必须已经满足精确 owner/group/mode，且其 inode 永不替换，从而保持所有预开
FD 与路径访问处于同一 `flock` 域。子目录验证并 fsync 后，两棵根先以最终 owner 和
`0000` 模式完成持久化，再先发布并 fsync writer root，最后在初始化锁仍被持有时用一次
fence root `fchmod` 作为提交点；提交前 fence 不可遍历，提交后整棵树已满足契约，不依赖事后回滚。
最终目录为 `02750`，文件为 `0640`；中断遗留的 frozen/sealed 状态由普通重试
fail closed，必须显式执行 privileged 运维检查/恢复。正常
LightRAG 仍由官方 entrypoint 以 root 初始化数据卷后降权至 UID/GID `1000`；这些路径
都没有 world 权限。仅有界初始化器与 bootstrap/controller job 以读写方式挂载 fence；
steady LightRAG 和 readiness reader 均只读挂载。
若 Go 应用也在容器中运行，保持应用镜像自身 UID/GID，将 fence 只读挂载并添加
`--group-add "$XLH_LIGHTRAG_SHARED_GID"`；不要用 `--user 1000` 或 world-readable
权限绕过共享组。

默认监听 `:8088`。React 开发服务器可在 `frontend/xiaolanhe-web` 中执行
`npm run dev`；生产镜像会构建并由 Go 服务托管前端。

### LightRAG 与高级编排

LightRAG 是基础和高级模式共同的知识边界。`XLH_ADVANCED_AI_ENABLED` 只选择
Assistant 编排方式，不决定是否使用 LightRAG。应用启动与 `/readyz` 会验证
LightRAG 版本/API、workspace、工作目录、四种存储类型、恢复状态和部署围栏；
基础设施门禁另行验证 Milvus 版本、三类 collection 的 1024 维
`AUTOINDEX`/`COSINE` 契约。不匹配或依赖不可用时 fail closed，不会回退到 MySQL、
PostgreSQL、pgvector 或 NanoVectorDB。

管理员可以在账号页提交、跟踪、分页查看和精确删除 LightRAG 文档。已有
NanoVectorDB 工作区必须在停止所有 writer、完成全量备份并取得单独授权后，运行三类
官方 rebuild 和 fence 验证；仅修改环境变量不构成迁移。操作说明见
[`deploy/lightrag/README.md`](deploy/lightrag/README.md)。

一次性导入旧 PostgreSQL 知识默认只 dry-run。它是隔离的迁移工具，不会链接到
正常服务，也不会启动持续同步：

```bash
go run ./cmd/import-knowledge --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json
go run ./cmd/import-knowledge --execute --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json
go run ./cmd/import-knowledge --reconcile --limit 20 \
  --manifest ./artifacts/legacy-knowledge-manifest.json \
  --checkpoint ./artifacts/legacy-knowledge-checkpoint.json \
  --reconciliation-report ./artifacts/legacy-knowledge-reconciliation.json
```

第一条命令会扫描已冻结源并生成不可变 manifest 和初始 checkpoint；`--execute`
每次最多处理 100 份文档，重复同一命令会从 checkpoint 的连续成功水位安全续跑，
不再接受旧的 `--after-id` 参数。完成后必须运行 `--reconcile` 并保留报告。此隔离工具
默认使用 HTTPS LightRAG 端点；只有本地或受控环境才能显式设置
`XLH_LIGHTRAG_ALLOW_INSECURE=true` 后连接 HTTP，避免将 API key 和导入正文意外发送到
明文端点。它不会把 LightRAG ID 写回 PostgreSQL。历史 PostgreSQL migration
保留为不可变迁移记录和 cutover 输入，不会由 MySQL 启动流程执行。

### 启用 Redis + RocketMQ 限时抢购

```bash
export XLH_FLASH_SALE_ENABLED=true
export XLH_REDIS_URL='redis://:password@127.0.0.1:6379/0'
export XLH_REDIS_ALLOW_INSECURE=true # local Compose only; production uses rediss://
export XLH_ROCKETMQ_NAMESERVERS='127.0.0.1:9876'
export XLH_ROCKETMQ_ALLOW_INSECURE=true # local Compose only; production configures ACL
export XLH_METRICS_TOKEN='replace-with-a-distinct-32-character-operator-token'
go run ./cmd/xiaolanhe
```

本地 Compose 是集成环境，不是 MySQL、Redis、RocketMQ 或 Milvus 的高可用生产
拓扑。生产部署必须使用私网、认证、持久化、监控和经过演练的备份/恢复。

## 主要接口

- 健康：`GET /healthz`、`GET /readyz`。
- 运行指标：`GET /metrics`，仅配置 `XLH_METRICS_TOKEN` 时注册，并要求独立的
  `Authorization: Bearer <token>` 运维凭证；高级 AI 或秒杀开启时该 token 必填。
- 账号与画像：`/api/auth/*`、`GET /api/me`、
  `GET/PUT/DELETE /api/me/assistant-profile`。
- 目录、社区、优惠、订单和限时抢购：`/api/games`、`/api/community/*`、
  `/api/deals`、`/api/coupons/*`、`/api/orders/*`、`/api/flash-sales/*`。
- 助手：`POST /api/chat/message`、`POST /api/chat/stream`。
- 知识：`GET /api/knowledge/search`、`POST /api/knowledge/documents`、
  `GET /api/admin/knowledge/tracks/:trackId`、
  `GET /api/admin/knowledge/documents`、
  `DELETE /api/admin/knowledge/documents/:documentId`。管理接口要求 admin；写接口还要求
  同源校验。

## 验证

```bash
make mysql-static
make milvus-static
make fence-static
make verify
make ci BASE_REF=origin/master
```

需要相应本地服务和显式配置时再运行 `make mysql-live`、`make milvus-live`、
`make fence-live` 和 `make lightrag-live`。`make lightrag-lifecycle` 会创建隔离环境、
调用真实模型/embedding，并执行写入、删除、备份和恢复，因此需要明确确认且可能产生
费用；它不属于无凭证的日常检查。

`make eval` 运行固定 transcript 上的离线确定性门禁，输出 baseline/candidate 的
路由、facet、Recall@8、引用、画像一致性、调用预算和 fixture latency。它不调用
真实模型，也不等价于真实 LightRAG/Milvus 效果；凭证化的真实评测属于 rollout。

部署拓扑、备份、恢复和回滚见
[`docs/guidance/public-deployment.md`](docs/guidance/public-deployment.md)。当前迁移的
权威规格见
[`specs/20260907-mysql-milvus-migration/`](specs/20260907-mysql-milvus-migration/)；
生产云资源采购、数据 cutover、破坏性清理和付费 rebuild 均需单独批准，仓库配置不会
自动执行这些动作。
