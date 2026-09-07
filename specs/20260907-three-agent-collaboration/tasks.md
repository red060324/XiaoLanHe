# Tasks

Authoritative: [spec.md](spec.md)。本轮只完成起草与Git分支交付；所有生产实现须先经过用户对spec/plan/test-plan的明确批准。

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | DONE | 同步远端与Skill，建立分支，完成规范spec及文档校验 | AC1–12设计覆盖 | 本目录、report.md；不是功能验收通过 |
| T1 | PRE_MERGE | TODO | 用户评审目标、完整设计、契约、预算、测试计划；更新APPROVED | AC1–12 | 需明确审批记录，当前没有 |
| T2 | PRE_MERGE | TODO | 建立目标/约束/产物v2；实现来源、金额、总价、证据与方案验证 | AC4,5,8,10 | 契约与单元测试 |
| T3 | PRE_MERGE | TODO | 委派状态、任务指纹、共享预算、最终化预留与取消 | AC6–9 | deterministic loop、并发与cancel测试 |
| T4 | PRE_MERGE | TODO | 固定Eino v0.9.13最小验证：AgentTool参数/返回、事件隔离、终止、共享计数 | AC1,7,9 | 可编译适配器测试；不凭文档猜API |
| T5 | PRE_MERGE | TODO | Research查询拆解内聚；保留自主检索loop；证据与缺口产物 | AC1,3,4,8,10 | fake模型至少两次工具反馈、来源失败测试 |
| T6 | PRE_MERGE | TODO | Planning迁ADK；实现自主方案调整与validate_plan工具 | AC1,4,5,6 | 初始组合不合格→调整→通过；无解case |
| T7 | PRE_MERGE | TODO | Copilot承接/澄清/委派/收尾；消除新模式独立Router/QueryPlanner/Answer调用 | AC2,3,6,9 | 完整调用轨迹与计数断言 |
| T8 | PRE_MERGE | TODO | Skill v2、模式配置、组装、HTTP/SSE兼容与记忆 | AC7,9,12 | 配置/模式隔离/SSE/持久化测试 |
| T9 | PRE_MERGE | TODO | 更新AGENTS/ARCHITECTURE并标记旧spec局部superseded；保留旧模式回滚 | AC12 | 文档与最终diff映射 |
| T10 | PRE_MERGE | TODO | 版本化eval、观测白名单、安全用例及所有适用CI门禁 | AC3–12 | test-plan V1–V18；Linux GitHub Actions |
| T11 | PRE_MERGE | TODO | 最终report逐AC映射到实际diff和验证；全PRE_MERGE完成才READY | AC1–12 | 当前report仅spec阶段 |
| R1 | ROLLOUT | TODO | 经批准的隔离环境真实模型/LightRAG smoke，记录成本与模型版本 | AC3,4,7,10–12 | 授权、日志与结果；无生产写入 |
| R2 | ROLLOUT | TODO | 灰度开启three_agent，观察失败/延迟/token，演练切回legacy | AC9,11,12 | 开关与回滚记录 |
| F1 | FOLLOW_UP | TODO | 删除legacy引擎与v1配置 | AC12 | 触发：新模式上线验收且回滚窗口关闭；Owner:red060324，另评审 |

依赖顺序：T1 → T2/T4 → T3 → T5/T6 → T7/T8 → T9/T10 → T11。T0完成不意味着任何行为AC已实现。
