# Internal Contracts v2

- Status: DRAFT；内部业务契约，不新增公开 HTTP DTO。
- Authoritative: [spec.md](spec.md)。Eino schema/message/tool DTO 在适配层转换，不进入 Entity/UseCase。

## Common Envelope / Bounds

server字段：schemaVersion=2、runId、taskId、sequence、skillId/version、deadline、可信user scope。模型不可覆盖；每轮委派有独立taskId，证据仅当前run。结构化输出拒绝未知字段和尾随内容；总JSON≤64KiB、普通说明≤500字符、目标≤2000字符、items≤10、missingFacts≤8。原用户问题按既有16KiB输入限制，Worker上下文由投影裁剪而非无限复制。

## Goal And Constraints

```text
Goal: objective, requestedOperation(fact|recommend|compare|compose), requirements[]
Constraint: id, kind, value, source(current_turn|confirmed_context|profile),
            sourceSpan?, strength(hard|soft), verification(verified|unverified)
Kinds: total_budget_minor, per_item_budget_minor, currency, region,
       platforms, item_count, excluded_owned, required_facets
```

金额非负整数最小货币单位，必须绑定币种；总价与单价分开；组合检查用服务端事实重新计算。用于完整方案的硬约束必须verified。sourceSpan只能证明有原文，不证明语义解析正确；数值/布尔/枚举走字段解析，模糊“便宜点”“适合小孩”等不作为已满足硬约束。历史画像不覆盖本轮明确要求，未确定币种/地区在影响价格时澄清。拥有状态由可信UserID作用域读取，不接受模型提交的ownedIds作事实。

## Copilot Tools

| 名称 | 模型可提交 | 代码门禁/结果 |
|---|---|---|
| prepare_task | goal、constraint proposals、skillId建议 | Registry解析/字段校验/不可扩权，返回可信TaskContext或clarification_required |
| research | objective、requiredFacets、已有missingFactIds | 投影TaskContext、来源白名单、工具权限和预算，返回ResearchArtifact |
| planning | goal引用、candidateIds、evidenceIds | 必须属于本run；服务端注入约束/身份，返回PlanningArtifact |
| finalize_response | kind、planId?、evidenceIds、limitationIds | 校验引用并冻结结果，进入不可逆最终化；关闭工具，用Copilot生成最终文字 |

Worker委派按Agent-as-Tool执行，但必须通过UseCase拥有的门禁包装；不能裸露一个接受任意字符串就跳过预算的AgentTool。read/search工具不能注册在Copilot上绕过Worker职责。

## Research

Input：objective、requiredFacets、sourcePolicy、待查缺口；必要查询上下文，不含父完整聊天。内部QueryUnits≤8；现有source/mode whitelist继续有效。Tools：search_lightrag、search_catalog、search_forum、开关及Skill双重允许的search_web。每个参数按当前适配器schema校验；不新增泛URL任意抓取工具。

Output：status、candidateIds、evidenceIds、coveredFacets、missingFacts、stopReason。Evidence 内容由真实工具适配层收集，UseCase分配ID，带实际来源与observedAt；模型不能凭字符串“登记证据”。候选与证据对应，未知字段标未知；不输出最终排名或替用户选择。

## Planning

Input：Goal、verified Constraints、候选及其证据、最小偏好投影。Tools：read_catalog（限定候选与市场）、read_entitlements（可信用户）、score_constraints（单项）、validate_plan（组合）。validate_plan 是新只读确定性工具，按候选ID从本run已验证事实取价格与属性；不信任模型传入price/owned/合计。缺事实返回needs_information，不用零值代表免费/未拥有。

Output：status、planId（server分配）、items[]、constraintChecks[]、tradeoffs[]、missingFacts[]、stopReason。items含subjectId、用途、evidenceIds；constraintChecks含constraintId、satisfied|violated|unknown及服务器校验结果。complete必须所有hard checks satisfied；infeasible与partial不可改写成complete。模型初步声称matched不是最终真值；返回前代码再次核验拥有状态/价格及组合条件。

## Missing Facts / Stop

MissingFact：id（server）、subjectId?、facet、whyNeeded、blocking、已有查询fingerprint。研究重试由Copilot请求，规划不得直接研究。只有相关新增事实或已确认新约束才允许重新规划；全相同输入、相同证据版本的重复指纹返回no_progress。

Status：complete、partial、needs_information、infeasible、unavailable、bounded。StopReason：complete、no_evidence、no_progress、invalid_contract、dependency_unavailable、max_model_calls、max_tool_calls、max_delegations、max_iterations、deadline、cancelled。外部provider错误转换为固定类别，不传原始敏感内容。

## Public Chat Compatibility

保持 ChatRequest(sessionId,message)、ChatResponse(sessionId,answer,createdAt) 与现有URL、认证和16KiB校验。SSE沿用message文本及error协议；本期不新增进度/结束事件，UI不需要理解Agent框架。相同SSE消费方能消费新模式结果。只有Copilot最终化文本进入message，Worker事件仅内部处理。complete需正常流结束并存储成功；断流不把部分文本作为完成答案持久化。
