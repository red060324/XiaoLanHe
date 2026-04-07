package com.xiaolanhe.web.dto.search;

import java.util.List;

public record WebSearchResponse(
        boolean enabled,
        boolean cacheHit,
        String provider,
        String query,
        List<WebSearchResultResponse> items,
        String note
) {
}