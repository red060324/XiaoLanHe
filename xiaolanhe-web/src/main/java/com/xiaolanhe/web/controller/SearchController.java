package com.xiaolanhe.web.controller;

import com.xiaolanhe.web.dto.search.WebSearchResponse;
import com.xiaolanhe.web.dto.search.WebSearchResultResponse;
import com.xiaolanhe.search.model.SearchResponse;
import com.xiaolanhe.search.service.WebSearchService;
import java.util.List;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/search")
public class SearchController {

    private final WebSearchService webSearchService;

    public SearchController(WebSearchService webSearchService) {
        this.webSearchService = webSearchService;
    }

    @GetMapping("/web")
    public WebSearchResponse search(@RequestParam String query) {
        SearchResponse response = webSearchService.search(query);
        List<WebSearchResultResponse> items = response.items().stream()
                .map(item -> new WebSearchResultResponse(item.title(), item.url(), item.snippet(), item.source()))
                .toList();
        return new WebSearchResponse(response.enabled(), response.cacheHit(), response.provider(), response.query(), items, response.note());
    }
}