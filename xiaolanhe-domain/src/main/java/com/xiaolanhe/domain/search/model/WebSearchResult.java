package com.xiaolanhe.domain.search.model;

public record WebSearchResult(
        String title,
        String url,
        String snippet,
        String source
) {
}