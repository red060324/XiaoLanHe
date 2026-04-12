package com.xiaolanhe.search.model;

import java.util.Map;

public record EvidenceItem(
        String sourceType,
        String title,
        String content,
        String sourceUrl,
        int score,
        Map<String, Object> metadata
) {
}
