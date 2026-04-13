package com.xiaolanhe.search.service;

import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import com.xiaolanhe.domain.search.model.WebSearchResult;
import com.xiaolanhe.rag.service.KnowledgeDocumentService;
import com.xiaolanhe.search.model.EvidenceBundle;
import com.xiaolanhe.search.model.EvidenceItem;
import com.xiaolanhe.search.model.SearchAgentRequest;
import com.xiaolanhe.search.model.SearchResponse;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class DefaultSearchAgentService implements SearchAgentService {

    private static final int DEFAULT_TOP_K = 5;

    private final KnowledgeDocumentService knowledgeDocumentService;
    private final WebSearchService webSearchService;

    public DefaultSearchAgentService(KnowledgeDocumentService knowledgeDocumentService,
                                     WebSearchService webSearchService) {
        this.knowledgeDocumentService = knowledgeDocumentService;
        this.webSearchService = webSearchService;
    }

    @Override
    public EvidenceBundle retrieveEvidence(SearchAgentRequest request) {
        int topK = normalizeTopK(request.topK());
        List<EvidenceItem> rawItems = new ArrayList<>();
        List<String> notes = new ArrayList<>();
        List<String> effectiveQueries = effectiveQueries(request);

        if (request.needLocalKnowledge()) {
            int totalSnippets = 0;
            for (String query : effectiveQueries) {
                List<KnowledgeSnippet> snippets = knowledgeDocumentService.search(
                        query,
                        request.gameCode(),
                        request.regionCode(),
                        topK
                );
                totalSnippets += snippets.size();
                rawItems.addAll(
                        snippets.stream()
                                .map(this::toKnowledgeEvidence)
                                .toList()
                );
            }
            notes.add("Local knowledge search executed " + effectiveQueries.size() + " queries and returned " + totalSnippets + " snippets.");
        }

        if (request.needWebSearch()) {
            int totalResults = 0;
            for (String query : effectiveQueries) {
                SearchResponse response = webSearchService.search(query);
                totalResults += response.items().size();
                rawItems.addAll(
                        response.items().stream()
                                .map(this::toWebEvidence)
                                .toList()
                );
                notes.add("[" + query + "] " + response.note());
            }
            notes.add("Web search executed " + effectiveQueries.size() + " queries and returned " + totalResults + " results.");
        }

        List<EvidenceItem> mergedItems = mergeAndTrim(rawItems, request.freshnessRequired(), topK);
        return new EvidenceBundle(
                request.query(),
                request.needLocalKnowledge(),
                request.needWebSearch(),
                request.freshnessRequired(),
                mergedItems,
                notes
        );
    }

    private EvidenceItem toKnowledgeEvidence(KnowledgeSnippet snippet) {
        Map<String, Object> metadata = new LinkedHashMap<>();
        metadata.put("chunkId", snippet.chunkId());
        metadata.put("documentId", snippet.documentId());
        metadata.put("gameCode", snippet.gameCode());
        metadata.put("regionCode", snippet.regionCode());
        metadata.put("patchVersion", snippet.patchVersion());

        return new EvidenceItem(
                "knowledge",
                snippet.title(),
                snippet.snippet(),
                snippet.sourceUrl(),
                snippet.score(),
                metadata
        );
    }

    private EvidenceItem toWebEvidence(WebSearchResult result) {
        Map<String, Object> metadata = new LinkedHashMap<>();
        metadata.put("engine", result.source());

        return new EvidenceItem(
                "web",
                result.title(),
                result.snippet(),
                result.url(),
                50,
                metadata
        );
    }

    private List<EvidenceItem> mergeAndTrim(List<EvidenceItem> rawItems, boolean freshnessRequired, int topK) {
        Map<String, EvidenceItem> deduplicated = new LinkedHashMap<>();
        rawItems.stream()
                .sorted((left, right) -> compareEvidence(left, right, freshnessRequired))
                .forEach(item -> deduplicated.putIfAbsent(dedupeKey(item), item));
        return deduplicated.values().stream().limit(topK).toList();
    }

    private int compareEvidence(EvidenceItem left, EvidenceItem right, boolean freshnessRequired) {
        if (freshnessRequired) {
            if (!left.sourceType().equals(right.sourceType())) {
                return "web".equals(left.sourceType()) ? -1 : 1;
            }
        }
        return Integer.compare(right.score(), left.score());
    }

    private String dedupeKey(EvidenceItem item) {
        String title = StringUtils.hasText(item.title()) ? item.title().trim().toLowerCase() : "";
        String url = StringUtils.hasText(item.sourceUrl()) ? item.sourceUrl().trim().toLowerCase() : "";
        String content = StringUtils.hasText(item.content()) ? item.content().trim().toLowerCase() : "";
        if (StringUtils.hasText(url)) {
            return item.sourceType() + "::" + url;
        }
        return item.sourceType() + "::" + title + "::" + content;
    }

    private int normalizeTopK(int topK) {
        if (topK <= 0) {
            return DEFAULT_TOP_K;
        }
        return Math.min(topK, 10);
    }

    private List<String> effectiveQueries(SearchAgentRequest request) {
        LinkedHashSet<String> queries = new LinkedHashSet<>();
        if (StringUtils.hasText(request.query())) {
            queries.add(request.query().trim());
        }
        if (request.subQueries() != null) {
            request.subQueries().stream()
                    .filter(StringUtils::hasText)
                    .map(String::trim)
                    .forEach(queries::add);
        }
        return List.copyOf(queries);
    }
}
