package com.xiaolanhe.rag.model;

public record KnowledgeDocumentSummary(
        long documentId,
        int chunkCount,
        String title,
        String gameCode,
        String regionCode
) {
}