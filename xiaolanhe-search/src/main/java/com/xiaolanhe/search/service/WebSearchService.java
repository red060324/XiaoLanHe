package com.xiaolanhe.search.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.xiaolanhe.domain.search.model.WebSearchResult;
import com.xiaolanhe.search.config.SearchProperties;
import com.xiaolanhe.search.model.SearchResponse;
import java.net.URI;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.util.StringUtils;
import org.springframework.stereotype.Service;

@Service
public class WebSearchService {

    private static final Logger log = LoggerFactory.getLogger(WebSearchService.class);

    private final SearchProperties searchProperties;
    private final ObjectMapper objectMapper;
    private final HttpClient httpClient;

    public WebSearchService(SearchProperties searchProperties, ObjectMapper objectMapper) {
        this.searchProperties = searchProperties;
        this.objectMapper = objectMapper;
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(5))
                .build();
    }

    public SearchResponse search(String query) {
        if (!searchProperties.enabled()) {
            return new SearchResponse(false, false, searchProperties.provider(), query, List.of(),
                    "Web search is disabled in the current profile.");
        }

        SearchResponse response = fetchFromSearxng(query);
        return response;
    }

    private SearchResponse fetchFromSearxng(String query) {
        if (!StringUtils.hasText(searchProperties.endpoint())) {
            return new SearchResponse(true, false, searchProperties.provider(), query, List.of(),
                    "SearXNG endpoint is not configured.");
        }
        try {
            String endpoint = searchProperties.endpoint().replaceAll("/+$", "");
            String url = endpoint + "/search?q=" + URLEncoder.encode(query, StandardCharsets.UTF_8)
                    + "&format=json&language=zh-CN";
            HttpRequest request = HttpRequest.newBuilder()
                    .uri(URI.create(url))
                    .timeout(Duration.ofSeconds(10))
                    .GET()
                    .build();
            HttpResponse<String> response = httpClient.send(request, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (response.statusCode() >= 400) {
                return new SearchResponse(true, false, searchProperties.provider(), query, List.of(),
                        "SearXNG returned HTTP " + response.statusCode());
            }
            String body = response.body();

            JsonNode root = objectMapper.readTree(body);
            List<WebSearchResult> items = new ArrayList<>();
            JsonNode results = root.path("results");
            if (results.isArray()) {
                for (JsonNode item : results) {
                    if (items.size() >= 5) {
                        break;
                    }
                    items.add(new WebSearchResult(
                            item.path("title").asText(""),
                            item.path("url").asText(""),
                            item.path("content").asText(""),
                            item.path("engine").asText(searchProperties.provider())
                    ));
                }
            }

            return new SearchResponse(
                    true,
                    false,
                    searchProperties.provider(),
                    query,
                    items,
                    items.isEmpty() ? "SearXNG returned no results." : "SearXNG returned " + items.size() + " results."
            );
        } catch (Exception ex) {
            log.warn("SearXNG search failed for query {}", query, ex);
            return new SearchResponse(true, false, searchProperties.provider(), query, List.of(),
                    "SearXNG request failed: " + ex.getClass().getSimpleName());
        }
    }
}
