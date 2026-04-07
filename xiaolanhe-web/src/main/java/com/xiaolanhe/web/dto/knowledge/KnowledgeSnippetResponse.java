package com.xiaolanhe.web.dto.knowledge;

public record KnowledgeSnippetResponse(
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