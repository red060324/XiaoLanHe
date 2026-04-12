package com.xiaolanhe.search.model;

public record SearchAgentRequest(
        String query,
        String gameCode,
        String regionCode,
        boolean needLocalKnowledge,
        boolean needWebSearch,
        boolean freshnessRequired,
        int topK
) {
}
