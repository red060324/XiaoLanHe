# Implementation Plan

- Status: DRAFT。规范来源：[spec](spec.md)。本文件是批准后的实现计划，不描述已交付行为。

## Selected Approach / Alternatives

采用三 ChatModelAgent + Agent-as-Tool。Copilot 是父角色，Research/Planning 是受控工具委派；业务 UseCase 控制状态/契约/预算，Eino 只负责模型消息与 loop。研究与规划依赖证据，初版不并发。

不选固定 SequentialAgent/Workflow：会重复当前唯一动作仍问模型的问题。固定的鉴权、加载、存储用普通代码。可用 compose 节点包装真正独立的数据处理，但不另建总 DAG。也不引入 DeepAgent/自由互相 transfer、无限 Plan-Execute-Replan；它们的动作空间超出本需求。Planning 保留自主工具循环，不替换成普通排序函数。

## Layers / Affected Modules

运行流：Entry → Presenter → Chat UseCase → Assistant UseCase → Eino runtime adapter → Worker 工具边界 → 业务读 UseCase/Repository；完成后返回 Chat UseCase 持久化。

编译依赖指向领域：Entry/Presenter 只做 HTTP/SSE 适配；`internal/assistant/usecase` 拥有所消费的最小 runtime/worker capability；`entity` 定义 Goal/Constraint/Evidence/Plan，不导入 Eino/Hertz/pgx；Eino DTO、工具 schema、事件过滤、provider 错误映射留在 `internal/assistant/agent/eino` 与现有 `internal/adapter/eino` 边界。只在本次触及处收敛重复，不开展全仓搬家。

| 变更 | 位置/负责人 | AC |
|---|---|---|
| 目标/约束/缺口/产物v2 | assistant/entity | AC4–6,8 |
| 请求状态、预算、最终化门禁 | assistant/usecase | AC2,6–10 |
| 三ADK角色与工具包装 | assistant/agent/eino；复用 adapter/eino 检索 | AC1–4,8 |
| 方案约束验证和当前事实 | assistant 实体/用例，目录只读能力 | AC4,5,10 |
| Stream适配与持久化 | 现有 chat usecase/presenter/entry | AC9 |
| 注册表/配置/组装 | assistant/skill、internal/config、cmd/xiaolanhe/main.go | AC7,12 |
| eval/遥测/文档 | assistant/eval、telemetry、AGENTS/ARCHITECTURE | AC3,11,12 |

## Runtime State And Dispatch

RunState 持有 server runId、绑定 Skill、确认过的 Goal/Constraints、证据仓、最新 plan、任务记录、共享预算；绝不把整个对象交给模型。

1. 接入：复用鉴权、输入校验、上下文过滤，创建 run。Copilot 首轮只得到可信 Skill 描述和受控 `prepare_task`/`finalize_response` 工具。`prepare_task` 是确定性配置工具，不是第四个 Agent 或额外独立模型节点。
2. Copilot 可调用 prepare_task 提议 Skill/目标/约束。UseCase 校验允许的 Skill、字段来源与政策，绑定一次；只能收紧预算；模型不能修改工具白名单/预算值。没有归一化成功的硬约束不能进入 complete 规划。
3. 绑定后获准 research/planning 工具在下一轮可用（动态工具配置由固定版本 ADK 支持的 hook/实例配置实现；同一角色/状态不重置）。每次调用都要做服务端门禁，隐藏工具不是唯一权限保证。
4. Research 输入经契约过滤；输出 Evidence 由服务端分配 ID。QueryPlan 留在 Research 内部生成与校验，复用来源/mode 限制，但不再先调用外部 QueryPlanner。
5. Planning 必须有可信目标、约束、候选和有效事实；跨轮历史证据仅是提示，需按当前 run 重新读取/校验。价格和拥有状态本轮重新读；已有证据复用仅限本 run 与来源时效范围。
6. Worker 仅返回 complete/partial/needs_information/infeasible/unavailable/bounded。Copilot 据此选择下一步，代码阻止违法状态与无进展循环。
7. 两角色每 run 各最多2次，合计4次。第二次 Research 需具体 missingFact 及未完成请求指纹；第二次 Planning 必须有相关新增证据或用户明确新约束。每个缺口至多一次补查；没有变化返回 no_progress，停止同任务循环。用户新消息是新 run，不复活旧额度。

## Three Loops / Finalization

所有角色都遵循：共享预算扣减 → 模型 → ToolCalls → 参数/权限检查 → 执行 → ToolMessage → 再次模型；若输出无工具则解析产物。Research/Planning 可连续多轮，不能做成固定模型→工具→终止。完整子事件只用于内部记录，不直接作为聊天内容。

Copilot 获准工具：prepare_task、research、planning、finalize_response。finalize_response 参数只含 kind(answer/clarify/limited)、plan/evidence 引用与限制，不让模型伪造业务校验结果。代码确认引用、状态与剩余额度后原子进入 FINALIZING，冻结 snapshot，禁用所有工具，用同一 Copilot 角色/指令中的最终化段发起最终流式模型请求。该请求计入 Copilot 与全局预算；它不是额外 Answer 角色。

如果 deliberation 轮返回无 ToolCalls，文本必须暂存而非直接 SSE；将其作为收尾提议经同一门禁进入最终化，防止先发中间文字后又发起工具。最终化是不可逆单向状态，不允许重新委派；同时返回多个工具含 finalize 的响应作为无效调度拒绝，禁止先后部分执行产生歧义。初版每轮只准一个 Worker 委派，多委派 ToolCalls 返回 typed invalid_dispatch；不暗中并行。

最终流禁用工具，从供应商接收文本即可转既有 message 事件；SSE error 语义不变，不新增 done/progress 事件。只向客户端输出固定白名单文本事件；引用格式继续使用服务器证据映射。引用源必须存在，但不宣称结构校验能证明任意自然语言语义正确，结论与计划一致性另以 eval 验证。

## Budgets / Backpressure

| 范围 | 初始设计上限 |
|---|---|
| 单run全部角色 | 16次模型、16次叶子工具、4次Worker委派、45秒 |
| Copilot | 7次模型（包含prepare与最终化），根控制工具最多6次 |
| Research每次委派 | 6次模型、8次叶子工具、25秒 |
| Planning每次委派 | 4次模型、4次叶子工具、15秒 |
| 每个叶子工具 | 10秒或来源配置更短值；不得超出run/Worker剩余deadline |
| 收尾预留 | 1次模型、5秒；Worker不能消耗 |
| 单模型输出 | 最多2048 tokens；单模型输入应用侧序列化上限64KiB |

计数定义：research/planning 包装器只计委派，不与其子工具双重计数；所有实际模型请求包括最终化、无效结构修复都计 model。prepare/finalize 计独立受限控制工具次数。预算对象跨所有角色共享，Skill只能取更小值。recommend_games 当前2委派和12模型需在新模式版本配置中同步调整，不覆盖旧模式定义。不得累加各角色上限后承诺全部可用。

根请求deadline不因重委派重置；执行阶段有效deadline至少预留5秒收尾。临近预算时由确定性门禁停止新委派，进入 limited 最终化；最后模型不可用/超时只发既有错误，不宣称一定能成功回答。模型未知usage记 unavailable；不换算虚假的美元上限。输入字节限制不是token精确计量，供应商内重试必须禁用或纳入统一计数，不得出现无限透明重试。真实成本上线前用批准的provider smoke观测。

## Trust / Tools / Contracts

详细 schema 与限制见 [contracts.md](contracts.md)。所有工具只读；交易和社区写能力不注册。模型参数不接受 userId/sessionId/runId、白名单或剩余预算；服务端从可信 context 注入。

当前轮硬约束通过类型、值范围、原文span和字段解析校验；从非结构化语言无法可靠确定的字段标 unverified 并澄清，不能把模型抽取当成数学保证。字段有 current_turn/confirmed_context/profile 来源；明确本轮条件覆盖画像软偏好，冲突硬条件返回 clarification。不得持久化推断为用户画像修改。

Required：鉴权/会话存储、模型。Optional：按任务需求的 LightRAG、catalog、forum、Web；有价格/拥有状态要求时 catalog 是该结论必要依赖。LightRAG 失败不得重标本地知识为 LightRAG。外部文本均是资料，不是可执行指令。

## Failures / Cancellation / Concurrency

部分来源失败：保留其他来源真实证据和缺口；全部失败：unavailable，不生造当前事实。无结果、不可行、缺信息、超额与供应商错误是不同枚举。未知工具、越权身份、跨run引用直接拒绝；模型无效结构最多1次有预算修复，且相同错误不重试第二次；不得重跑旧引擎隐瞒错误。

HTTP/SSE取消传到共享context、ADK Runner、每个模型/工具，关闭迭代器/流；取消后禁止新委派、持久化完整答案和摘要刷新。同会话并发沿用已有消息/summary顺序规则，新增测试证明不串run证据；不凭本次改造引入新全局锁。

## Storage / Memory

不新增表/索引/迁移。复用现有会话、消息、摘要、画像及保留/删除规则。run证据、约束投影、Worker产物与任务指纹仅在请求内存中，取消/结束释放；不把内部推理或原始工具结果写入用户会话。规划最多10项、查询最多8单元、缺口最多8项；超出拒绝而非无界缓存。完整Copilot答案沿用现有存储；持久化失败走错误，摘要仅完成后best-effort且单调。跨请求恢复Agent循环与checkpoint不在范围。

## Config / Rollout / Rollback

拟新增 `XLH_ASSISTANT_ORCHESTRATION=legacy|three_agent`，默认 legacy；解析无效值启动失败。仅advanced模式支持 three_agent，否则报配置冲突。沿用现有数值配置并按模式提供默认值；同一run固定模式与prompt/Skill版本，不中途切换。

Prompt/Skill：版本化 copilot_v2/research_v2/planning_v2 和绑定schemaVersion=2；旧模式保留v1，部署观测记录固定版本标签。影子付费双跑不默认开启。先本地/CI fake评测，批准后隔离环境真实模型smoke，再显式开启three_agent。回滚设置legacy并重启正常发布；新请求使用旧实现，旧run取消/排空遵循现有部署逻辑，无数据迁移回滚。

## Observability / Evals

复用日志/指标框架：固定role、phase、skill_version、outcome、stop_reason；记录模型/叶子工具/委派/控制工具次数、耗时、供应商明确返回的usage。runId仅结构化日志，不做高基数指标。禁止提示词、答案、画像、证据正文、tool参数或密钥进入日志。SSE阶段错误和持久化失败分别计数，不虚报成功。

告警关注 invalid_contract、no_progress、预算耗尽、无证据、finalization失败、取消后仍执行。版本化eval覆盖路由/委派选择、工具选择、方案硬约束、引用有效性、只读和不泄漏。所有安全/硬约束case必须100%通过；候选质量分不得低于同fixture legacy基线，新增能力按适用场景单独计，不能让旧模式不支持场景稀释比较。

## Debt And Documentation

复用现有边界而非跨仓抽象。旧模式重复代码在回滚窗口内保留并标 legacy；待上线验证与移除开关另评审清理。当前改造批准后必须更新根AGENTS的“唯一Research”规则、ARCHITECTURE调用图、Skill权限和旧spec局部替代说明。不会用历史规则掩盖用户已明确选择三个Agent的目标。
