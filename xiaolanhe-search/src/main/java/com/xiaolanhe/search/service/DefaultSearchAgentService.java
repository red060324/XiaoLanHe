package com.xiaolanhe.search.service;

import com.fasterxml.jackson.databind.ObjectMapper;
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
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class DefaultSearchAgentService implements SearchAgentService {

    private static final Logger log = LoggerFactory.getLogger(DefaultSearchAgentService.class);
    private static final int DEFAULT_TOP_K = 5;

    private final KnowledgeDocumentService knowledgeDocumentService;
    private final WebSearchService webSearchService;
    private final ChatClient searchAgentPlanningChatClient;
    private final ObjectMapper objectMapper;

    public DefaultSearchAgentService(KnowledgeDocumentService knowledgeDocumentService,
                                     WebSearchService webSearchService,
                                     @Qualifier("searchAgentPlanningChatClient") ChatClient searchAgentPlanningChatClient,
                                     ObjectMapper objectMapper) {
        this.knowledgeDocumentService = knowledgeDocumentService;
        this.webSearchService = webSearchService;
        this.searchAgentPlanningChatClient = searchAgentPlanningChatClient;
        this.objectMapper = objectMapper;
    }

    @Override
    public EvidenceBundle retrieveEvidence(SearchAgentRequest request) {
        int topK = normalizeTopK(request.topK());
        List<EvidenceItem> rawItems = new ArrayList<>();
        List<String> notes = new ArrayList<>();
        SearchDecomposition decomposition = decomposeQueries(request);
        List<String> effectiveQueries = decomposition.subQueries();
        notes.addAll(decomposition.notes());

        if (request.needLocalKnowledge()) {
            int totalSnippets = 0;
            for (String query : effectiveQueries) {
                List<KnowledgeSnippet> snippets = knowledgeDocumentService.search(
                        query,
                        null,
                        null,
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

    private SearchDecomposition decomposeQueries(SearchAgentRequest request) {
        try {
            String raw = searchAgentPlanningChatClient.prompt()
                    .user(buildPlanningInput(request))
                    .call()
                    .content();
            SearchPlanningPayload payload = objectMapper.readValue(extractJson(raw), SearchPlanningPayload.class);
            List<String> queries = normalizedQueries(payload.subQueries(), request);
            return new SearchDecomposition(
                    queries.isEmpty() ? effectiveQueries(request) : queries,
                    payload.notes() == null ? List.of() : List.copyOf(payload.notes())
            );
        } catch (Exception ex) {
            log.warn("Search decomposition fallback triggered for query={}", request.query(), ex);
            return new SearchDecomposition(effectiveQueries(request), List.of("SearchAgent 使用规则兜底查询。"));
        }
    }

    private String buildPlanningInput(SearchAgentRequest request) {
        return """
                【用户问题】
                %s

                【检索意图】
                %s

                【是否时效问题】
                %s

                【是否偏低层检索】
                %s

                【是否偏高层检索】
                %s
                """.formatted(
                defaultText(request.query(), "无"),
                defaultText(request.queryIntent(), "factual"),
                request.freshnessRequired(),
                request.needLowLevelRetrieval(),
                request.needHighLevelRetrieval()
        ).trim();
    }

    private List<String> normalizedQueries(List<String> generatedQueries, SearchAgentRequest request) {
        LinkedHashSet<String> queries = new LinkedHashSet<>();
        if (StringUtils.hasText(request.query())) {
            queries.add(request.query().trim());
        }
        if (generatedQueries != null) {
            generatedQueries.stream()
                    .filter(StringUtils::hasText)
                    .map(String::trim)
                    .forEach(queries::add);
        }
        return List.copyOf(queries);
    }

    private String extractJson(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "{}";
        }
        String trimmed = raw.trim();
        if (trimmed.startsWith("```")) {
            int firstBrace = trimmed.indexOf('{');
            int lastBrace = trimmed.lastIndexOf('}');
            if (firstBrace >= 0 && lastBrace > firstBrace) {
                return trimmed.substring(firstBrace, lastBrace + 1);
            }
        }
        return trimmed;
    }

    private String defaultText(String value, String fallback) {
        return StringUtils.hasText(value) ? value : fallback;
    }

    private record SearchPlanningPayload(
            List<String> querySteps,
            List<String> subQueries,
            List<String> notes
    ) {
    }

    private record SearchDecomposition(
            List<String> subQueries,
            List<String> notes
    ) {
    }
}
