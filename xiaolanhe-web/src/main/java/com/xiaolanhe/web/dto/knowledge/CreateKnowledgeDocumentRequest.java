package com.xiaolanhe.web.dto.knowledge;

import jakarta.validation.constraints.NotBlank;

public record CreateKnowledgeDocumentRequest(
        @NotBlank String sourceType,
        @NotBlank String title,
        String sourceUrl,
        String gameCode,
        String regionCode,
        String patchVersion,
        @NotBlank String contentText
) {
}