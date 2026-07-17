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
    private final LightRagSearchService lightRagSearchService;
    private final WebSearchService webSearchService;
    private final ChatClient searchAgentPlanningChatClient;
    private final ObjectMapper objectMapper;

    public DefaultSearchAgentService(KnowledgeDocumentService knowledgeDocumentService,
                                     LightRagSearchService lightRagSearchService,
                                     WebSearchService webSearchService,
                                     @Qualifier("searchAgentPlanningChatClient") ChatClient searchAgentPlanningChatClient,
                                     ObjectMapper objectMapper) {
        this.knowledgeDocumentService = knowledgeDocumentService;
        this.lightRagSearchService = lightRagSearchService;
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
        log.info(
                "SearchAgent decomposition complete. query={}, objective={}, subQueryCount={}, querySteps={}",
                trim(request.query(), 80),
                searchObjective(request),
                effectiveQueries.size(),
                decomposition.querySteps()
        );
        notes.addAll(decomposition.notes());
        notes.add("SearchAgent objective: " + searchObjective(request));
        if (!decomposition.querySteps().isEmpty()) {
            notes.add("SearchAgent querySteps: " + String.join(" | ", decomposition.querySteps()));
        }
        if (!effectiveQueries.isEmpty()) {
            notes.add("SearchAgent subQueries: " + String.join(" || ", effectiveQueries));
        }

        if (request.needLocalKnowledge()) {
            int totalSnippets = 0;
            for (String query : effectiveQueries) {
                LightRagSearchService.Result lightRagResult = lightRagSearchService.search(
                        query,
                        request.needLowLevelRetrieval(),
                        request.needHighLevelRetrieval(),
                        topK
                );
                if (lightRagResult.available()) {
                    rawItems.addAll(lightRagResult.items());
                    totalSnippets += lightRagResult.items().size();
                    lightRagResult.notes().forEach(note -> notes.add("[LightRAG][" + query + "] " + note));
                    continue;
                }

                notes.add("[LightRAG][" + query + "] " + String.join(" | ", lightRagResult.notes()));
                List<KnowledgeSnippet> snippets = knowledgeDocumentService.search(query, null, null, topK);
                totalSnippets += snippets.size();
                rawItems.addAll(snippets.stream().map(this::toKnowledgeEvidence).toList());
            }
            notes.add("Local knowledge search executed " + effectiveQueries.size() + " queries and returned " + totalSnippets + " snippets/items.");
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
        return queries.stream().limit(6).toList();
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
                    payload.querySteps() == null ? List.of() : List.copyOf(payload.querySteps()),
                    payload.notes() == null ? List.of() : List.copyOf(payload.notes())
            );
        } catch (Exception ex) {
            log.warn("Search decomposition fallback triggered for query={}", request.query(), ex);
            return new SearchDecomposition(
                    fallbackQueries(request),
                    fallbackSteps(request),
                    List.of("模型查询分解失败，回退到规则兜底查询。")
            );
        }
    }

    private String buildPlanningInput(SearchAgentRequest request) {
        return """
                【用户问题】
                %s

                【归一化问题】
                %s

                【检索意图】
                %s

                【任务类型】
                %s

                【输出模式】
                %s

                【是否时效问题】
                %s

                【是否偏低层检索】
                %s

                【是否偏高层检索】
                %s

                【主控初步 querySteps】
                %s

                【主控初步 subQueries】
                %s

                【主控备注】
                %s
                """.formatted(
                defaultText(request.query(), "无"),
                defaultText(request.normalizedQuery(), "无"),
                defaultText(request.queryIntent(), "factual"),
                defaultText(request.taskType(), "SIMPLE_QA"),
                defaultText(request.responseMode(), "qa"),
                request.freshnessRequired(),
                request.needLowLevelRetrieval(),
                request.needHighLevelRetrieval(),
                formatNotes(request.querySteps()),
                formatNotes(request.subQueries()),
                formatNotes(request.taskNotes())
        ).trim();
    }

    private List<String> normalizedQueries(List<String> generatedQueries, SearchAgentRequest request) {
        LinkedHashSet<String> queries = new LinkedHashSet<>();
        if (StringUtils.hasText(request.query())) {
            queries.add(request.query().trim());
        }
        if (request.subQueries() != null) {
            request.subQueries().stream()
                    .filter(StringUtils::hasText)
                    .map(this::sanitizeQuery)
                    .filter(StringUtils::hasText)
                    .forEach(queries::add);
        }
        if (generatedQueries != null) {
            generatedQueries.stream()
                    .filter(StringUtils::hasText)
                    .map(this::sanitizeQuery)
                    .filter(StringUtils::hasText)
                    .forEach(queries::add);
        }
        return queries.stream().limit(6).toList();
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

    private String trim(String value, int maxLength) {
        if (!StringUtils.hasText(value) || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    private String sanitizeQuery(String value) {
        if (!StringUtils.hasText(value)) {
            return "";
        }
        return value.replace("？", "")
                .replace("?", "")
                .replace("，", " ")
                .replace(",", " ")
                .replace("。", " ")
                .trim();
    }

    private String formatNotes(List<String> notes) {
        if (notes == null || notes.isEmpty()) {
            return "无";
        }
        return String.join(" | ", notes);
    }

    private List<String> fallbackQueries(SearchAgentRequest request) {
        LinkedHashSet<String> queries = new LinkedHashSet<>();
        if (StringUtils.hasText(request.query())) {
            queries.add(request.query().trim());
        }
        if (request.subQueries() != null) {
            request.subQueries().stream()
                    .filter(StringUtils::hasText)
                    .map(this::sanitizeQuery)
                    .filter(StringUtils::hasText)
                    .forEach(queries::add);
        }
        if ("recommendation".equals(request.queryIntent()) || "recommendation".equalsIgnoreCase(request.responseMode())) {
            queries.add(sanitizeQuery(request.query() + " 值不值得"));
            queries.add(sanitizeQuery(request.query() + " 适合谁"));
        }
        if ("comparison".equals(request.queryIntent()) || "compare".equalsIgnoreCase(request.responseMode())) {
            queries.add(sanitizeQuery(request.query() + " 对比"));
            queries.add(sanitizeQuery(request.query() + " 区别"));
        }
        if ("strategy".equals(request.queryIntent()) || "guide".equalsIgnoreCase(request.responseMode())) {
            queries.add(sanitizeQuery(request.query() + " 攻略"));
            queries.add(sanitizeQuery(request.query() + " 玩法思路"));
        }
        if (request.freshnessRequired()) {
            queries.add(sanitizeQuery(request.query() + " 最新"));
            queries.add(sanitizeQuery(request.query() + " 官方公告"));
        }
        return queries.stream().filter(StringUtils::hasText).limit(6).toList();
    }

    private List<String> fallbackSteps(SearchAgentRequest request) {
        List<String> steps = new ArrayList<>();
        steps.add("规则兜底：保留原问题作为基础查询");
        if (request.querySteps() != null && !request.querySteps().isEmpty()) {
            steps.add("规则兜底：继承主控提供的初步检索步骤");
        }
        if (request.freshnessRequired()) {
            steps.add("规则兜底：补充时效信息查询");
        }
        if ("recommendation".equals(request.queryIntent()) || "recommendation".equalsIgnoreCase(request.responseMode())) {
            steps.add("规则兜底：补充推荐维度查询");
        }
        if ("comparison".equals(request.queryIntent()) || "compare".equalsIgnoreCase(request.responseMode())) {
            steps.add("规则兜底：补充对比维度查询");
        }
        if ("strategy".equals(request.queryIntent()) || "guide".equalsIgnoreCase(request.responseMode())) {
            steps.add("规则兜底：补充攻略维度查询");
        }
        return List.copyOf(steps);
    }

    private String searchObjective(SearchAgentRequest request) {
        if (request.freshnessRequired()) {
            return "freshness";
        }
        if ("recommendation".equals(request.queryIntent())) {
            return "recommendation";
        }
        if ("comparison".equals(request.queryIntent())) {
            return "comparison";
        }
        if ("strategy".equals(request.queryIntent())) {
            return "strategy";
        }
        if (request.needHighLevelRetrieval()) {
            return "high_level_reasoning";
        }
        return "factual_lookup";
    }

    private record SearchPlanningPayload(
            List<String> querySteps,
            List<String> subQueries,
            List<String> notes
    ) {
    }

    private record SearchDecomposition(
            List<String> subQueries,
            List<String> querySteps,
            List<String> notes
    ) {
    }
}
