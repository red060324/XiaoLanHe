package com.xiaolanhe.search.service;

import com.xiaolanhe.search.config.SearchProperties;
import com.xiaolanhe.search.model.SearchResponse;
import java.time.Duration;
import java.util.List;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class WebSearchService {

    private final SearchProperties searchProperties;
    private final SearchCacheService searchCacheService;

    public WebSearchService(SearchProperties searchProperties, SearchCacheService searchCacheService) {
        this.searchProperties = searchProperties;
        this.searchCacheService = searchCacheService;
    }

    public SearchResponse search(String query) {
        if (!searchProperties.enabled()) {
            return new SearchResponse(false, false, searchProperties.provider(), query, List.of(),
                    "Web search is disabled in the current profile.");
        }

        SearchResponse cached = searchCacheService.get(query);
        if (cached != null) {
            return cached;
        }

        SearchResponse response = new SearchResponse(
                true,
                false,
                searchProperties.provider(),
                query,
                List.of(),
                buildNote()
        );
        searchCacheService.put(query, response, cacheTtl());
        return response;
    }

    private String buildNote() {
        if (!StringUtils.hasText(searchProperties.endpoint())) {
            return "Search provider is not configured yet. Wire a provider endpoint or adapter in xiaolanhe-search.";
        }
        return "Search provider endpoint is configured, but the live provider adapter has not been implemented yet.";
    }

    private Duration cacheTtl() {
        return searchProperties.cacheTtl() == null ? Duration.ofMinutes(10) : searchProperties.cacheTtl();
    }
}