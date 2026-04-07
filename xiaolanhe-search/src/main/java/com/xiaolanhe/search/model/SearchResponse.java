package com.xiaolanhe.search.model;

import com.xiaolanhe.domain.search.model.WebSearchResult;
import java.util.List;

public record SearchResponse(
        boolean enabled,
        boolean cacheHit,
        String provider,
        String query,
        List<WebSearchResult> items,
        String note
) {
    public SearchResponse withCacheHit(boolean cacheHit) {
        return new SearchResponse(enabled, cacheHit, provider, query, items, note);
    }
}