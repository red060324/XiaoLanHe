package com.xiaolanhe.search.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.xiaolanhe.search.config.SearchProperties;
import com.xiaolanhe.search.model.LightRagChunk;
import com.xiaolanhe.search.model.LightRagQueryResult;
import com.xiaolanhe.search.model.LightRagReference;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class LightRagClient {

    private static final Logger log = LoggerFactory.getLogger(LightRagClient.class);

    private final SearchProperties searchProperties;
    private final ObjectMapper objectMapper;
    private final HttpClient httpClient;

    public LightRagClient(SearchProperties searchProperties, ObjectMapper objectMapper) {
        this.searchProperties = searchProperties;
        this.objectMapper = objectMapper;
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(5))
                .build();
    }

    public LightRagQueryResult query(String query, String mode) {
        SearchProperties.LightRag lightRag = searchProperties.lightrag();
        if (lightRag == null || !lightRag.enabled()) {
            return new LightRagQueryResult(false, query, mode, "", List.of(), List.of(), "LightRAG is disabled in the current profile.");
        }
        if (!StringUtils.hasText(lightRag.baseUrl())) {
            return new LightRagQueryResult(false, query, mode, "", List.of(), List.of(), "LightRAG base URL is not configured.");
        }

        String normalizedMode = StringUtils.hasText(mode) ? mode.trim() : defaultMode();
        try {
            String url = lightRag.baseUrl().replaceAll("/+$", "") + "/query/data";
            String payload = objectMapper.writeValueAsString(new QueryRequest(
                    query,
                    normalizedMode,
                    lightRag.includeReferences(),
                    lightRag.includeChunkContent()
            ));

            HttpRequest.Builder builder = HttpRequest.newBuilder()
                    .uri(URI.create(url))
                    .timeout(resolveTimeout(lightRag))
                    .header("Content-Type", "application/json")
                    .header("Accept", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(payload, StandardCharsets.UTF_8));

            if (StringUtils.hasText(lightRag.apiKey())) {
                builder.header("X-API-Key", lightRag.apiKey().trim());
            }

            HttpResponse<String> response = httpClient.send(builder.build(), HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (response.statusCode() >= 400) {
                return new LightRagQueryResult(
                        false,
                        query,
                        normalizedMode,
                        "",
                        List.of(),
                        List.of(),
                        "LightRAG returned HTTP " + response.statusCode()
                );
            }

            JsonNode root = objectMapper.readTree(response.body());
            String status = root.path("status").asText("");
            if (!"success".equalsIgnoreCase(status)) {
                return new LightRagQueryResult(
                        false,
                        query,
                        normalizedMode,
                        "",
                        List.of(),
                        List.of(),
                        defaultText(root.path("message").asText(""), "LightRAG query returned no usable result.")
                );
            }

            JsonNode data = root.path("data");
            List<LightRagChunk> chunks = parseChunks(data.path("chunks"));
            List<LightRagReference> references = parseReferences(data.path("references"));
            return new LightRagQueryResult(
                    true,
                    query,
                    normalizedMode,
                    root.path("message").asText(""),
                    chunks,
                    references,
                    chunks.isEmpty() ? "LightRAG returned no chunks." : "LightRAG returned " + chunks.size() + " chunks."
            );
        } catch (Exception ex) {
            log.warn("LightRAG query failed for query {}", query, ex);
            return new LightRagQueryResult(
                    false,
                    query,
                    normalizedMode,
                    "",
                    List.of(),
                    List.of(),
                    "LightRAG request failed: " + ex.getClass().getSimpleName()
            );
        }
    }

    private List<LightRagChunk> parseChunks(JsonNode node) {
        if (!node.isArray()) {
            return List.of();
        }
        List<LightRagChunk> chunks = new ArrayList<>();
        for (JsonNode item : node) {
            String content = item.path("content").asText("");
            if (!StringUtils.hasText(content)) {
                continue;
            }
            chunks.add(new LightRagChunk(
                    item.path("reference_id").asText(""),
                    content,
                    item.path("file_path").asText(""),
                    item.path("chunk_id").asText("")
            ));
        }
        return List.copyOf(chunks);
    }

    private List<LightRagReference> parseReferences(JsonNode node) {
        if (!node.isArray()) {
            return List.of();
        }
        List<LightRagReference> references = new ArrayList<>();
        for (JsonNode item : node) {
            List<String> content = new ArrayList<>();
            JsonNode contentNode = item.path("content");
            if (contentNode.isArray()) {
                for (JsonNode chunk : contentNode) {
                    if (chunk.isTextual() && StringUtils.hasText(chunk.asText())) {
                        content.add(chunk.asText());
                    }
                }
            }
            references.add(new LightRagReference(
                    item.path("reference_id").asText(""),
                    item.path("file_path").asText(""),
                    List.copyOf(content)
            ));
        }
        return List.copyOf(references);
    }

    private Duration resolveTimeout(SearchProperties.LightRag lightRag) {
        if (lightRag.timeout() == null || lightRag.timeout().isNegative() || lightRag.timeout().isZero()) {
            return Duration.ofSeconds(20);
        }
        return lightRag.timeout();
    }

    private String defaultMode() {
        SearchProperties.LightRag lightRag = searchProperties.lightrag();
        if (lightRag == null || !StringUtils.hasText(lightRag.queryMode())) {
            return "mix";
        }
        return lightRag.queryMode().trim();
    }

    private String defaultText(String value, String fallback) {
        return StringUtils.hasText(value) ? value : fallback;
    }

    private record QueryRequest(
            String query,
            String mode,
            boolean include_references,
            boolean include_chunk_content
    ) {
    }
}
