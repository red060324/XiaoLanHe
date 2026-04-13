package com.xiaolanhe.agent.model;

import java.util.List;

public record RetrievalPlan(
        String originalQuery,
        String normalizedQuery,
        String queryIntent,
        boolean freshnessRequired,
        boolean needLocalKnowledge,
        boolean needWebSearch,
        boolean needLowLevelRetrieval,
        boolean needHighLevelRetrieval,
        List<String> querySteps,
        List<String> subQueries,
        int topK,
        boolean rerankEnabled,
        List<String> notes
) {
    public boolean requiresEvidence() {
        return needLocalKnowledge || needWebSearch;
    }
}
