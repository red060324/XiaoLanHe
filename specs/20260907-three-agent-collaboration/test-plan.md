# Test Plan

Authoritative: [spec.md](spec.md)、[plan.md](plan.md)。先写测试计划，审批后才实施。

## Scope And Environments

本轮文档变更仅运行git diff --check与本地Markdown链接/必备节/AC覆盖检查；以下均为未来实现门禁，不把TODO写成PASS。

PRE_MERGE使用固定模型事件与fake工具/本地provider stub，不依赖密钥或付费调用。integration只用隔离测试资源；真实模型/LightRAG验证属于ROLLOUT，须单独审批。

## Cases

| ID | Class | Layer | Scenario | Expected result | Command/evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | unit | 普通寒暄与澄清 | Copilot零Worker；澄清结束本run，不后台等待；最终回答仍经门禁 | AC2,3,9；见下方命令映射 | TODO |
| V2 | PRE_MERGE | agent/eval | 事实问答 | Research工具反馈后再调用模型；无Planning；候选不被伪装成最终方案 | AC1,3,4；见下方命令映射 | TODO |
| V3 | PRE_MERGE | agent/eval | 两款PC游戏/预算/不同玩法/排除拥有完整路径 | Research→Planning→Copilot；方案经组合验证；无第四个Answer角色 | AC1–5,9；见下方命令映射 | TODO |
| V4 | PRE_MERGE | unit | Planning错误组合与无解 | validate_plan拒绝超价/重复/类型缺失；模型观察错误后调整；无解输出infeasible | AC1,4,5,10；见下方命令映射 | TODO |
| V5 | PRE_MERGE | unit | 当前要求覆盖画像与预算语义 | 本轮200总预算不当单价；未知币种不默认；模糊字段未验证；不得修改持久画像 | AC5,8；见下方命令映射 | TODO |
| V6 | PRE_MERGE | agent/eval | 补查/重新规划/无进展 | 明确缺口允许一次修复；新增相关证据才重规划；同指纹循环停止；Worker不能互调 | AC6,7；见下方命令映射 | TODO |
| V7 | PRE_MERGE | unit | 预算边界 | 局部与共享额度取严；包装器只计委派；根控制次数独立；预留最终化；最后一token不宣称精准费用 | AC7,11；见下方命令映射 | TODO |
| V8 | PRE_MERGE | adapter | 最终化事件隔离 | 混合文本/tool_calls不泄漏；多个委派或finalize混合拒绝；冻结后无工具；finalization模型计数 | AC2,7,9；见下方命令映射 | TODO |
| V9 | PRE_MERGE | unit/integration | 权限/提示注入/伪造证据 | 文档注入无扩权；拒绝userId、跨run证据、未知工具、写操作、超长/未知JSON字段 | AC4,8；见下方命令映射 | TODO |
| V10 | PRE_MERGE | adapter | 来源部分/全部失败 | 真实保留可用证据；LightRAG不重标本地源；当前事实缺失不编造；超时与no_result有区别 | AC10；见下方命令映射 | TODO |
| V11 | PRE_MERGE | HTTP/SSE | 开始失败/中途错误/正常结束 | 原URL/DTO/message/error兼容；无内部事件和敏感参数；正常完成仅持久化一次 | AC9；见下方命令映射 | TODO |
| V12 | PRE_MERGE | race | 用户断连与取消、同会话并发 | 停止root/worker/model/tool；run间不串证据；无泄漏/竞态；取消无成功存储/摘要 | AC7–9；见下方命令映射 | TODO |
| V13 | PRE_MERGE | storage stub/integration | 持久化与摘要失败 | 答案存储失败不报成功；完整结束后摘要best-effort且单调；历史证据重新入run验证 | AC9；见下方命令映射 | TODO |
| V14 | PRE_MERGE | observability | 日志/metrics与usage | 固定标签、role和stop reason正确；无正文/身份内容；未知usage=unavailable | AC11；见下方命令映射 | TODO |
| V15 | PRE_MERGE | config | 模式配置和回滚 | legacy默认；非法模式/advanced冲突失败；新旧Skill隔离；同run不切模式；无隐式旧引擎重跑 | AC12；见下方命令映射 | TODO |
| V16 | PRE_MERGE | eval | 版本化baseline/candidate固定fixtures | 安全/硬约束100%；适用质量指标不低于基线；新增能力独立报告；无真实付费调用 | AC3–12；见下方命令映射 | TODO |
| V17 | PRE_MERGE | static | 架构与文档门禁 | 框架DTO不越界；生产改动均映射AC；AGENTS与架构一致；spec/链接检查通过 | AC1–12；见下方命令映射 | TODO |
| V18 | PRE_MERGE | CI/frontend | 全仓兼容 | 既有测试与web构建通过；Linux CI与race有实际执行证据 | AC9,12；见下方命令映射 | TODO |
| S1 | ROLLOUT | smoke | 寒暄/事实/推荐/复杂约束/澄清/缺口 | 实际role/tool轨迹、有效引用、约束、延迟与usage可核查 | 经批准的隔离环境与模型版本 | TODO |
| S2 | ROLLOUT | rollback | three_agent切回legacy | 新请求使用旧模式，原协议与存储可读，无数据迁移 | 发布开关与回滚记录 | TODO |

## Canonical Commands And Evidence

- V1–V10、V13–V15：先按仓库 local-verification 指引选具体命名测试，再 `make test`；ADK适配器包也须纳入，不只测实体。
- V11：HTTP/SSE wire测试，断流与error输出必须验证真实消费边界。
- V12：`make test-race`；取消需有同步点/等待退出断言，不能只sleep后猜完成。
- V16：`make eval`，保留固定模型请求/工具次数、期望的Agent序列、fixture版本及baseline/candidate结果。
- V17：`git diff --check`、`make architecture`、`make spec-drift BASE_REF=origin/codex/clean-architecture-refactor`；实现PR变基后更新BASE_REF。
- V18：`make verify`、`make web-test`、`make web-build`、`make ci BASE_REF=origin/codex/clean-architecture-refactor` 与GitHub Actions。实际Makefile若调用重叠目标，只记录一次真实执行，不虚增检查数量。

## Deterministic Agent Cases

脚本模型明确返回ToolCalls→ToolMessage观察→下一次模型；断言三角色各自至少一个多轮测试，不能只有构造器测试。分别覆盖“继续”“停止”“补查”“无解”“预算不足”“工具失败”。稳定断言结构与业务结果，不断言模型隐式思维链。所有方案事实来自fixture工具，拒绝生成不存在的证据ID。

组合路径必须检查总价格为实际候选总和、两个用途覆盖、排除拥有；单项score全部通过不代表组合通过。新模式调用轨迹不存在旧独立Router/QueryPlanner/Answer请求。根最终化调用单独计费，不要求每条业务路径固定调用次数。

## Not Applicable

本次不做数据库schema迁移、写工具的幂等执行、LightRAG写入/备份改造、部署镜像变更，因无对应变更不增加这些门禁。并非跳过已有会话存储、只读provider、取消或前端协议回归。

## Exit Criteria

所有PRE_MERGE实际执行并通过；安全/硬约束用例100%；缺失环境/零测试匹配均记UNVERIFIED或BLOCKED，不是PASS。付费smoke不能代替确定性测试。当前是DRAFT，所有行为验收未执行。
