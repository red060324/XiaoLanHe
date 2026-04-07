package com.xiaolanhe.web.dto.knowledge;

import java.util.List;

public record KnowledgeSearchResponse(
        String query,
        List<KnowledgeSnippetResponse> items
) {
}