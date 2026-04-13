package com.xiaolanhe.agent.model;

import java.util.List;

public record TaskPlan(
        String taskId,
        TaskType taskType,
        IntentType intentType,
        ResponseMode responseMode,
        boolean needMemory,
        boolean needSearch,
        boolean needVerification,
        boolean needSkill,
        List<MemoryType> memoryTypes,
        RetrievalPlan retrievalPlan,
        List<String> skillNames,
        TaskState currentState,
        List<String> notes
) {
}
