package com.xiaolanhe.agent.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.xiaolanhe.agent.model.IntentType;
import com.xiaolanhe.agent.model.MemoryType;
import com.xiaolanhe.agent.model.ResponseMode;
import com.xiaolanhe.agent.model.RetrievalPlan;
import com.xiaolanhe.agent.model.RouteType;
import com.xiaolanhe.agent.model.TaskPlan;
import com.xiaolanhe.agent.model.TaskState;
import com.xiaolanhe.agent.model.TaskType;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.UUID;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class MainAgentService {

    private static final Logger log = LoggerFactory.getLogger(MainAgentService.class);

    private final ChatClient mainAgentPlanningChatClient;
    private final ChatClient mainAgentDirectChatClient;
    private final ObjectMapper objectMapper;

    public MainAgentService(@Qualifier("mainAgentPlanningChatClient") ChatClient mainAgentPlanningChatClient,
                            @Qualifier("mainAgentDirectChatClient") ChatClient mainAgentDirectChatClient,
                            ObjectMapper objectMapper) {
        this.mainAgentPlanningChatClient = mainAgentPlanningChatClient;
        this.mainAgentDirectChatClient = mainAgentDirectChatClient;
        this.objectMapper = objectMapper;
    }

    public TaskPlan plan(String userMessage, String planningContext) {
        try {
            String raw = mainAgentPlanningChatClient.prompt()
                    .user(buildPlanningInput(userMessage, planningContext))
                    .call()
                    .content();
            MainAgentPlanPayload payload = objectMapper.readValue(extractJson(raw), MainAgentPlanPayload.class);
            return toTaskPlan(userMessage, payload);
        } catch (Exception ex) {
            log.warn("Main agent planning fallback triggered for query={}", userMessage, ex);
            return fallbackPlan(userMessage, planningContext);
        }
    }

    private String buildPlanningInput(String userMessage, String planningContext) {
        return """
                【用户问题】
                %s

                【轻量上下文】
                %s
                """.formatted(
                defaultText(userMessage, "无"),
                defaultText(planningContext, "无")
        ).trim();
    }

    private String buildDirectReplyInput(TaskPlan taskPlan, String userMessage) {
        return """
                【主路由】
                %s

                【用户问题】
                %s

                【规划备注】
                %s
                """.formatted(
                taskPlan.routeType().name(),
                defaultText(userMessage, "无"),
                formatNotes(taskPlan.notes())
        ).trim();
    }

    private TaskPlan toTaskPlan(String userMessage, MainAgentPlanPayload payload) {
        RouteType routeType = parseRouteType(payload.routeType());
        boolean directRoute = routeType == RouteType.DIRECT_CHAT || routeType == RouteType.CLARIFY;
        RetrievalDirective retrieval = payload.retrieval() == null ? new RetrievalDirective() : payload.retrieval();
        List<String> initialQuerySteps = initialQuerySteps(retrieval);
        List<String> initialSubQueries = initialSubQueries(userMessage, retrieval);
        boolean needSearch = payload.needSearch() && routeType == RouteType.EVIDENCE_ANSWER;
        boolean needMemory = directRoute ? false : payload.needMemory();
        RetrievalPlan retrievalPlan = needSearch
                ? new RetrievalPlan(
                userMessage,
                normalize(userMessage),
                defaultText(retrieval.queryIntent(), "factual"),
                retrieval.freshnessRequired(),
                retrieval.needLocalKnowledge(),
                retrieval.needWebSearch(),
                retrieval.needLowLevelRetrieval(),
                retrieval.needHighLevelRetrieval(),
                initialQuerySteps,
                initialSubQueries,
                retrieval.topK() <= 0 ? 5 : retrieval.topK(),
                retrieval.rerankEnabled(),
                safeList(retrieval.notes())
        )
                : null;

        return new TaskPlan(
                UUID.randomUUID().toString(),
                routeType,
                parseTaskType(payload.taskType()),
                parseIntentType(payload.intentType()),
                parseResponseMode(payload.responseMode()),
                needMemory,
                needSearch,
                payload.needVerification(),
                payload.needSkill(),
                needMemory ? parseMemoryTypes(payload.memoryTypes()) : List.of(),
                retrievalPlan,
                List.of(),
                TaskState.PLAN,
                safeList(payload.notes())
        );
    }

    private TaskPlan fallbackPlan(String userMessage, String planningContext) {
        boolean needMemory = StringUtils.hasText(planningContext);
        RetrievalPlan retrievalPlan = fallbackRetrievalPlan(userMessage);

        return new TaskPlan(
                UUID.randomUUID().toString(),
                RouteType.EVIDENCE_ANSWER,
                TaskType.SIMPLE_QA,
                IntentType.FACTUAL_LOOKUP,
                ResponseMode.QA,
                needMemory,
                true,
                true,
                false,
                defaultMemoryTypes(needMemory, false),
                retrievalPlan,
                List.of(),
                TaskState.PLAN,
                List.of(
                        "主规划失败，已回退到保守的证据增强回答链路。",
                        needMemory ? "保留已有轻量会话上下文，降低连续追问理解偏差。" : "当前未注入额外会话上下文。"
                )
        );
    }

    public String directReply(TaskPlan taskPlan, String userMessage) {
        return defaultText(
                mainAgentDirectChatClient.prompt()
                        .user(buildDirectReplyInput(taskPlan, userMessage))
                        .call()
                        .content(),
                "您好，请再说具体一点，我好帮您。"
        );
    }

    public reactor.core.publisher.Flux<String> streamDirectReply(TaskPlan taskPlan, String userMessage) {
        return mainAgentDirectChatClient.prompt()
                .user(buildDirectReplyInput(taskPlan, userMessage))
                .stream()
                .content();
    }

    private RetrievalPlan fallbackRetrievalPlan(String originalQuery) {
        return new RetrievalPlan(
                originalQuery,
                normalize(originalQuery),
                "factual",
                false,
                true,
                false,
                true,
                false,
                List.of("兜底规划：以原问题为中心构建基础证据。"),
                List.of(originalQuery.trim()),
                5,
                true,
                List.of("主规划失败时默认优先本地知识证据，避免误走其他链路。")
        );
    }

    private TaskType parseTaskType(String value) {
        try {
            return TaskType.valueOf(defaultText(value, TaskType.SIMPLE_QA.name()));
        } catch (Exception ex) {
            return TaskType.SIMPLE_QA;
        }
    }

    private IntentType parseIntentType(String value) {
        try {
            return IntentType.valueOf(defaultText(value, IntentType.FACTUAL_LOOKUP.name()));
        } catch (Exception ex) {
            return IntentType.FACTUAL_LOOKUP;
        }
    }

    private RouteType parseRouteType(String value) {
        try {
            return RouteType.valueOf(defaultText(value, RouteType.EVIDENCE_ANSWER.name()));
        } catch (Exception ex) {
            return RouteType.EVIDENCE_ANSWER;
        }
    }

    private ResponseMode parseResponseMode(String value) {
        String mode = defaultText(value, "qa").toLowerCase(Locale.ROOT);
        return switch (mode) {
            case "chat" -> ResponseMode.CHAT;
            case "guide" -> ResponseMode.GUIDE;
            case "compare" -> ResponseMode.COMPARE;
            case "recommendation" -> ResponseMode.RECOMMENDATION;
            case "clarify" -> ResponseMode.CLARIFY;
            default -> ResponseMode.QA;
        };
    }

    private List<MemoryType> parseMemoryTypes(List<String> values) {
        if (values == null || values.isEmpty()) {
            return List.of();
        }
        List<MemoryType> result = new ArrayList<>();
        for (String value : values) {
            try {
                result.add(MemoryType.valueOf(value));
            } catch (Exception ignored) {
            }
        }
        return result.isEmpty() ? List.of() : List.copyOf(result);
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

    private List<MemoryType> defaultMemoryTypes(boolean needMemory, boolean recommendation) {
        if (!needMemory) {
            return List.of();
        }
        if (recommendation) {
            return List.of(MemoryType.RECENT_SESSION, MemoryType.SESSION_SUMMARY, MemoryType.RESOURCE_CONSTRAINTS);
        }
        return List.of(MemoryType.RECENT_SESSION, MemoryType.SESSION_SUMMARY);
    }

    private String normalize(String userMessage) {
        if (!StringUtils.hasText(userMessage)) {
            return "";
        }
        return userMessage.trim().toLowerCase(Locale.ROOT);
    }

    private String defaultText(String value, String fallback) {
        return StringUtils.hasText(value) ? value : fallback;
    }

    private List<String> safeList(List<String> values) {
        return values == null ? List.of() : List.copyOf(values);
    }

    private String formatNotes(List<String> notes) {
        if (notes == null || notes.isEmpty()) {
            return "无";
        }
        return String.join(" | ", notes);
    }

    private List<String> initialQuerySteps(RetrievalDirective retrieval) {
        List<String> steps = new ArrayList<>();
        steps.add("主 Agent 已识别基础检索目标");
        if (retrieval.freshnessRequired()) {
            steps.add("补充时效信息检索");
        }
        if (retrieval.needHighLevelRetrieval()) {
            steps.add("补充高层检索维度");
        }
        if (retrieval.needLowLevelRetrieval()) {
            steps.add("补充低层检索维度");
        }
        return List.copyOf(steps);
    }

    private List<String> initialSubQueries(String userMessage, RetrievalDirective retrieval) {
        LinkedHashSet<String> queries = new LinkedHashSet<>();
        if (StringUtils.hasText(userMessage)) {
            queries.add(userMessage.trim());
        }
        if (retrieval.freshnessRequired()) {
            queries.add((userMessage + " 最新").trim());
            queries.add((userMessage + " 官方公告").trim());
        }
        if ("recommendation".equals(defaultText(retrieval.queryIntent(), ""))) {
            queries.add((userMessage + " 值不值得").trim());
        }
        if ("comparison".equals(defaultText(retrieval.queryIntent(), ""))) {
            queries.add((userMessage + " 对比").trim());
        }
        if ("strategy".equals(defaultText(retrieval.queryIntent(), ""))) {
            queries.add((userMessage + " 攻略").trim());
        }
        return queries.stream()
                .filter(StringUtils::hasText)
                .limit(4)
                .toList();
    }

    private record MainAgentPlanPayload(
            String routeType,
            String taskType,
            String intentType,
            String responseMode,
            boolean needMemory,
            boolean needSearch,
            boolean needVerification,
            boolean needSkill,
            List<String> memoryTypes,
            RetrievalDirective retrieval,
            List<String> notes
    ) {
    }

    private record RetrievalDirective(
            String queryIntent,
            boolean freshnessRequired,
            boolean needLocalKnowledge,
            boolean needWebSearch,
            boolean needLowLevelRetrieval,
            boolean needHighLevelRetrieval,
            int topK,
            boolean rerankEnabled,
            List<String> notes
    ) {
        private RetrievalDirective() {
            this("factual", false, true, false, true, false, 5, true, List.of());
        }
    }
}
