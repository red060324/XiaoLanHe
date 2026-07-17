package com.xiaolanhe.search.service;

import com.xiaolanhe.search.config.SearchProperties;
import com.xiaolanhe.search.model.LightRagChunk;
import com.xiaolanhe.search.model.EvidenceItem;
import com.xiaolanhe.search.model.LightRagQueryResult;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class LightRagSearchService {

    private final SearchProperties searchProperties;
    private final LightRagClient lightRagClient;

    public LightRagSearchService(SearchProperties searchProperties, LightRagClient lightRagClient) {
        this.searchProperties = searchProperties;
        this.lightRagClient = lightRagClient;
    }

    public Result search(String query, boolean lowLevelPreferred, boolean highLevelPreferred, int limit) {
        if (searchProperties.lightrag() == null || !searchProperties.lightrag().enabled()) {
            return Result.unavailable("LightRAG is disabled.");
        }

        String mode = resolveMode(lowLevelPreferred, highLevelPreferred);
        LightRagQueryResult queryResult = lightRagClient.query(query, mode);
        if (!queryResult.available()) {
            return Result.unavailable(queryResult.note());
        }

        List<EvidenceItem> items = new ArrayList<>();
        int rank = 0;
        for (LightRagChunk chunk : queryResult.chunks()) {
            if (items.size() >= limit) {
                break;
            }
            Map<String, Object> metadata = new LinkedHashMap<>();
            metadata.put("engine", "lightrag");
            metadata.put("mode", queryResult.mode());
            metadata.put("referenceId", chunk.referenceId());
            metadata.put("filePath", chunk.filePath());
            metadata.put("chunkId", chunk.chunkId());
            items.add(new EvidenceItem(
                    "knowledge",
                    displayTitle(chunk.filePath()),
                    chunk.content(),
                    chunk.filePath(),
                    Math.max(10, 100 - rank * 5),
                    metadata
            ));
            rank++;
        }

        List<String> notes = new ArrayList<>();
        notes.add("LightRAG mode: " + queryResult.mode());
        notes.add(queryResult.note());
        if (StringUtils.hasText(queryResult.response())) {
            notes.add("LightRAG response preview: " + trim(queryResult.response(), 120));
        }
        return new Result(true, List.copyOf(items), List.copyOf(notes));
    }

    private String resolveMode(boolean lowLevelPreferred, boolean highLevelPreferred) {
        if (lowLevelPreferred && highLevelPreferred) {
            return defaultMode();
        }
        if (highLevelPreferred) {
            return "global";
        }
        if (lowLevelPreferred) {
            return "local";
        }
        return defaultMode();
    }

    private String defaultMode() {
        SearchProperties.LightRag lightRag = searchProperties.lightrag();
        if (lightRag == null || !StringUtils.hasText(lightRag.queryMode())) {
            return "mix";
        }
        return lightRag.queryMode().trim();
    }

    private String displayTitle(String filePath) {
        if (!StringUtils.hasText(filePath)) {
            return "LightRAG reference";
        }
        String normalized = filePath.replace('\\', '/');
        int index = normalized.lastIndexOf('/');
        return index >= 0 && index < normalized.length() - 1 ? normalized.substring(index + 1) : normalized;
    }

    private String trim(String value, int maxLength) {
        if (!StringUtils.hasText(value) || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    public record Result(
            boolean available,
            List<EvidenceItem> items,
            List<String> notes
    ) {
        public static Result unavailable(String note) {
            return new Result(false, List.of(), List.of(note));
        }
    }
}
