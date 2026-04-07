package com.xiaolanhe.web.dto.search;

public record WebSearchResultResponse(
        String title,
        String url,
        String snippet,
        String source
) {
}