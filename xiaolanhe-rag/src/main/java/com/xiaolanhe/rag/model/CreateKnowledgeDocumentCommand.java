package com.xiaolanhe.rag.model;

public record CreateKnowledgeDocumentCommand(
        String sourceType,
        String title,
        String sourceUrl,
        String gameCode,
        String regionCode,
        String patchVersion,
        String contentText
) {
}