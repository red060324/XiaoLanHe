package com.xiaolanhe.search.model;

import java.util.List;

public record SearchAgentRequest(
        String query,
        String normalizedQuery,
        String queryIntent,
        String taskType,
        String responseMode,
        boolean needLocalKnowledge,
        boolean needWebSearch,
        boolean freshnessRequired,
        boolean needLowLevelRetrieval,
        boolean needHighLevelRetrieval,
        List<String> querySteps,
        List<String> subQueries,
        List<String> taskNotes,
        int topK,
        boolean rerankEnabled
) {
}
