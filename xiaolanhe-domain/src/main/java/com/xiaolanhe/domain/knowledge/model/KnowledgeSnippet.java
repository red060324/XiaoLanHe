package com.xiaolanhe.domain.knowledge.model;

public record KnowledgeSnippet(
        long chunkId,
        long documentId,
        String title,
        String gameCode,
        String regionCode,
        String patchVersion,
        String sourceUrl,
        String snippet,
        int score
) {
}