package com.xiaolanhe.agent.service;

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
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class MainAgentService {

    public TaskPlan plan(String userMessage) {
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
        RetrievalPlan retrievalPlan = needSearch ? buildRetrievalPlan(userMessage, normalized, freshness, strategy, compare, recommendation) : null;

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

    private RetrievalPlan buildRetrievalPlan(String originalQuery,
                                             String normalizedQuery,
                                             boolean freshness,
                                             boolean strategy,
                                             boolean compare,
                                             boolean recommendation) {
        List<String> querySteps = new ArrayList<>();
        List<String> subQueries = new ArrayList<>();
        LinkedHashSet<String> deduplicatedQueries = new LinkedHashSet<>();

        querySteps.add("理解用户问题并抽取核心检索目标");
        deduplicatedQueries.add(originalQuery.trim());

        if (freshness) {
            querySteps.add("补充版本、活动、最新信息等时效检索");
            deduplicatedQueries.add(originalQuery.trim() + " 最新");
            deduplicatedQueries.add(originalQuery.trim() + " 官方公告");
            deduplicatedQueries.add(originalQuery.trim() + " 版本前瞻");
        }

        if (strategy) {
            querySteps.add("补充攻略、玩法和阵容检索");
            deduplicatedQueries.add(originalQuery.trim() + " 攻略");
            deduplicatedQueries.add(originalQuery.trim() + " 配队");
            deduplicatedQueries.add(originalQuery.trim() + " 玩法思路");
        }

        if (compare) {
            querySteps.add("补充对比维度检索");
            deduplicatedQueries.add(originalQuery.trim() + " 对比");
            deduplicatedQueries.add(originalQuery.trim() + " 区别");
        }

        if (recommendation) {
            querySteps.add("补充推荐和投入回报维度检索");
            deduplicatedQueries.add(originalQuery.trim() + " 推荐");
            deduplicatedQueries.add(originalQuery.trim() + " 值不值得");
            deduplicatedQueries.add(originalQuery.trim() + " 养成成本");
        }

        subQueries.addAll(deduplicatedQueries);

        boolean highLevel = strategy || compare || recommendation || containsAny(normalizedQuery, "环境", "思路", "体系", "整体");
        return new RetrievalPlan(
                originalQuery,
                normalizedQuery,
                resolveQueryIntent(freshness, strategy, compare, recommendation),
                freshness,
                true,
                freshness,
                true,
                highLevel,
                List.copyOf(querySteps),
                List.copyOf(subQueries),
                highLevel ? 6 : 5,
                true,
                buildRetrievalNotes(freshness, highLevel)
        );
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
}
