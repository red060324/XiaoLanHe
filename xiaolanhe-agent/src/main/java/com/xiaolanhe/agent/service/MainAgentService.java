package com.xiaolanhe.agent.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.xiaolanhe.agent.model.IntentType;
import com.xiaolanhe.agent.model.MemoryType;
import com.xiaolanhe.agent.model.ResponseMode;
import com.xiaolanhe.agent.model.RetrievalPlan;
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
    private final ObjectMapper objectMapper;

    public MainAgentService(@Qualifier("mainAgentPlanningChatClient") ChatClient mainAgentPlanningChatClient,
                            ObjectMapper objectMapper) {
        this.mainAgentPlanningChatClient = mainAgentPlanningChatClient;
        this.objectMapper = objectMapper;
    }

    public TaskPlan plan(String userMessage) {
        try {
            String raw = mainAgentPlanningChatClient.prompt()
                    .user(buildPlanningInput(userMessage))
                    .call()
                    .content();
            MainAgentPlanPayload payload = objectMapper.readValue(extractJson(raw), MainAgentPlanPayload.class);
            return toTaskPlan(userMessage, payload);
        } catch (Exception ex) {
            log.warn("Main agent planning fallback triggered for query={}", userMessage, ex);
            return fallbackPlan(userMessage);
        }
    }

    private String buildPlanningInput(String userMessage) {
        return """
                【用户问题】
                %s
                """.formatted(defaultText(userMessage, "无")).trim();
    }

    private TaskPlan toTaskPlan(String userMessage, MainAgentPlanPayload payload) {
        RetrievalDirective retrieval = payload.retrieval() == null ? new RetrievalDirective() : payload.retrieval();
        RetrievalPlan retrievalPlan = payload.needSearch()
                ? new RetrievalPlan(
                userMessage,
                normalize(userMessage),
                defaultText(retrieval.queryIntent(), "factual"),
                retrieval.freshnessRequired(),
                retrieval.needLocalKnowledge(),
                retrieval.needWebSearch(),
                retrieval.needLowLevelRetrieval(),
                retrieval.needHighLevelRetrieval(),
                List.of(),
                List.of(),
                retrieval.topK() <= 0 ? 5 : retrieval.topK(),
                retrieval.rerankEnabled(),
                safeList(retrieval.notes())
        )
                : null;

        return new TaskPlan(
                UUID.randomUUID().toString(),
                parseTaskType(payload.taskType()),
                parseIntentType(payload.intentType()),
                parseResponseMode(payload.responseMode()),
                payload.needMemory(),
                payload.needSearch(),
                payload.needVerification(),
                payload.needSkill(),
                parseMemoryTypes(payload.memoryTypes()),
                retrievalPlan,
                List.of(),
                TaskState.PLAN,
                safeList(payload.notes())
        );
    }

    private TaskPlan fallbackPlan(String userMessage) {
        String normalized = normalize(userMessage);
        boolean greeting = isGreeting(normalized);
        boolean followUp = isFollowUp(normalized);
        boolean freshness = needsFreshness(normalized);
        boolean compare = containsAny(normalized, "对比", "区别", "哪个好", "怎么选", "vs", "versus");
        boolean recommendation = containsAny(normalized, "值不值得", "要不要", "推荐", "建议我", "适合我", "抽不抽");
        boolean strategy = containsAny(normalized, "怎么", "攻略", "打法", "配队", "养成", "规划", "上分", "build");

        IntentType intentType = resolveIntentType(greeting, freshness, compare, recommendation, strategy);
        TaskType taskType = resolveTaskType(greeting, freshness, compare, recommendation, strategy);
        ResponseMode responseMode = resolveResponseMode(greeting, compare, recommendation, strategy);
        boolean needMemory = needsMemory(greeting, followUp, recommendation, compare, strategy);
        boolean needSearch = !greeting;
        RetrievalPlan retrievalPlan = needSearch ? fallbackRetrievalPlan(userMessage, normalized, freshness, strategy, compare, recommendation) : null;

        return new TaskPlan(
                UUID.randomUUID().toString(),
                taskType,
                intentType,
                responseMode,
                needMemory,
                needSearch,
                true,
                false,
                defaultMemoryTypes(needMemory, recommendation),
                retrievalPlan,
                List.of(),
                TaskState.PLAN,
                buildPlanNotes(greeting, followUp, freshness, recommendation, needMemory)
        );
    }

    private RetrievalPlan fallbackRetrievalPlan(String originalQuery,
                                                String normalizedQuery,
                                                boolean freshness,
                                                boolean strategy,
                                                boolean compare,
                                                boolean recommendation) {
        boolean highLevel = strategy || compare || recommendation || containsAny(normalizedQuery, "环境", "思路", "体系", "整体", "趋势");
        return new RetrievalPlan(
                originalQuery,
                normalizedQuery,
                resolveQueryIntent(freshness, strategy, compare, recommendation),
                freshness,
                true,
                freshness,
                true,
                highLevel,
                List.of("规则兜底：按问题类型进行基础检索"),
                List.of(originalQuery.trim()),
                highLevel ? 6 : (freshness ? 6 : 5),
                true,
                buildRetrievalNotes(freshness, highLevel)
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

    private ResponseMode parseResponseMode(String value) {
        String mode = defaultText(value, "qa").toLowerCase(Locale.ROOT);
        return switch (mode) {
            case "chat" -> ResponseMode.CHAT;
            case "guide" -> ResponseMode.GUIDE;
            case "compare" -> ResponseMode.COMPARE;
            case "recommendation" -> ResponseMode.RECOMMENDATION;
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

    private List<String> buildPlanNotes(boolean greeting,
                                        boolean followUp,
                                        boolean freshness,
                                        boolean recommendation,
                                        boolean needMemory) {
        List<String> notes = new ArrayList<>();
        if (greeting) {
            notes.add("当前问题接近闲聊，走最轻链路。");
        }
        if (followUp) {
            notes.add("检测到连续对话痕迹，保留会话上下文。");
        }
        if (freshness) {
            notes.add("问题带有明显时效性，优先考虑联网检索。");
        }
        if (recommendation) {
            notes.add("问题带有建议或取舍性质，保留记忆上下文。");
        }
        if (!needMemory) {
            notes.add("当前问题更适合隔离历史上下文，避免旧话题干扰。");
        }
        return List.copyOf(notes);
    }

    private List<String> buildRetrievalNotes(boolean freshness, boolean highLevel) {
        List<String> notes = new ArrayList<>();
        if (freshness) {
            notes.add("优先补充最新版本、活动、公告类证据。");
        }
        if (highLevel) {
            notes.add("问题更偏策略或对比，后续适合接入高层检索。");
        }
        return List.copyOf(notes);
    }

    private String resolveQueryIntent(boolean freshness, boolean strategy, boolean compare, boolean recommendation) {
        if (recommendation) {
            return "recommendation";
        }
        if (compare) {
            return "comparison";
        }
        if (strategy) {
            return "strategy";
        }
        if (freshness) {
            return "freshness";
        }
        return "factual";
    }

    private IntentType resolveIntentType(boolean greeting,
                                         boolean freshness,
                                         boolean compare,
                                         boolean recommendation,
                                         boolean strategy) {
        if (greeting) {
            return IntentType.GENERAL_CHAT;
        }
        if (recommendation) {
            return IntentType.PERSONALIZED_RECOMMENDATION;
        }
        if (compare) {
            return IntentType.COMPARISON;
        }
        if (strategy) {
            return IntentType.STRATEGY_GUIDE;
        }
        if (freshness) {
            return IntentType.FRESHNESS_LOOKUP;
        }
        return IntentType.FACTUAL_LOOKUP;
    }

    private TaskType resolveTaskType(boolean greeting,
                                     boolean freshness,
                                     boolean compare,
                                     boolean recommendation,
                                     boolean strategy) {
        if (greeting) {
            return TaskType.CHAT;
        }
        if (recommendation) {
            return TaskType.RECOMMENDATION;
        }
        if (compare) {
            return TaskType.COMPARE;
        }
        if (strategy) {
            return TaskType.STRATEGY;
        }
        if (freshness) {
            return TaskType.FACTUAL_FRESH;
        }
        return TaskType.SIMPLE_QA;
    }

    private ResponseMode resolveResponseMode(boolean greeting,
                                             boolean compare,
                                             boolean recommendation,
                                             boolean strategy) {
        if (greeting) {
            return ResponseMode.CHAT;
        }
        if (recommendation) {
            return ResponseMode.RECOMMENDATION;
        }
        if (compare) {
            return ResponseMode.COMPARE;
        }
        if (strategy) {
            return ResponseMode.GUIDE;
        }
        return ResponseMode.QA;
    }

    private boolean isGreeting(String normalized) {
        return normalized.length() <= 6 && containsAny(normalized, "你好", "hi", "hello", "嗨");
    }

    private boolean isFollowUp(String normalized) {
        return containsAny(
                normalized,
                "继续",
                "刚刚",
                "上次",
                "前面",
                "前文",
                "还是那个",
                "接着",
                "顺便",
                "那么",
                "那这个",
                "那这个呢"
        );
    }

    private boolean needsFreshness(String normalized) {
        return containsAny(
                normalized,
                "最新",
                "今天",
                "当前",
                "刚更新",
                "更新了",
                "新角色",
                "新英雄",
                "新传奇",
                "新干员",
                "新卡池",
                "新版本",
                "活动",
                "版本",
                "补丁",
                "公告",
                "前瞻",
                "卡池",
                "hotfix",
                "update",
                "today"
        );
    }

    private boolean needsMemory(boolean greeting,
                                boolean followUp,
                                boolean recommendation,
                                boolean compare,
                                boolean strategy) {
        if (greeting) {
            return false;
        }
        if (followUp) {
            return true;
        }
        return recommendation || compare || strategy;
    }

    private boolean containsAny(String value, String... keywords) {
        for (String keyword : keywords) {
            if (value.contains(keyword)) {
                return true;
            }
        }
        return false;
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

    private record MainAgentPlanPayload(
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
