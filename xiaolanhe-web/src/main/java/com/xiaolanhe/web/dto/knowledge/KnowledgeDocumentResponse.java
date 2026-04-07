package com.xiaolanhe.web.dto.knowledge;

public record KnowledgeDocumentResponse(
        long documentId,
        int chunkCount,
        String title,
        String gameCode,
        String regionCode
) {
}