package com.xiaolanhe.agent.service;

import com.xiaolanhe.agent.model.SynthesisRequest;
import com.xiaolanhe.agent.model.SynthesisResult;
import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository;
import com.xiaolanhe.search.model.EvidenceBundle;
import com.xiaolanhe.search.model.SearchAgentRequest;
import com.xiaolanhe.search.service.SearchAgentService;
import java.time.Duration;
import java.time.Instant;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

@Service
public class ChatService {

    private static final Logger log = LoggerFactory.getLogger(ChatService.class);

    private final ConversationRepository conversationRepository;
    private final MemoryProfileAgentService memoryProfileAgentService;
    private final SearchAgentService searchAgentService;
    private final SynthesisAgentService synthesisAgentService;
    private final String chatModel;

    public ChatService(ConversationRepository conversationRepository,
                       MemoryProfileAgentService memoryProfileAgentService,
                       SearchAgentService searchAgentService,
                       SynthesisAgentService synthesisAgentService,
                       @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel) {
        this.conversationRepository = conversationRepository;
        this.memoryProfileAgentService = memoryProfileAgentService;
        this.searchAgentService = searchAgentService;
        this.synthesisAgentService = synthesisAgentService;
        this.chatModel = chatModel;
        log.info("ChatService initialized. model={}", chatModel);
    }

    public ChatResponseData chat(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        try {
            log.info("Calling synthesis agent. sessionId={}, model={}", context.sessionId(), chatModel);
            SynthesisResult result = synthesisAgentService.synthesize(
                    new SynthesisRequest(
                            userMessage,
                            "chat",
                            context.contextSnapshot(),
                            context.evidenceBundle()
                    )
            );
            persistAssistant(context, result.content());
            return new ChatResponseData(context.sessionId(), result.content(), OffsetDateTime.now());
        } catch (Exception ex) {
            log.warn("Synthesis agent call failed. sessionId={}", context.sessionId(), ex);
            throw new IllegalStateException("Synthesis agent call failed", ex);
        }
    }

    public Flux<String> stream(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        StringBuilder answerBuilder = new StringBuilder();
        Instant requestStart = Instant.now();
        final boolean[] firstChunkLogged = {false};

        return synthesisAgentService.streamSynthesis(
                        new SynthesisRequest(
                                userMessage,
                                "chat",
                                context.contextSnapshot(),
                                context.evidenceBundle()
                        )
                )
                .doOnSubscribe(subscription -> log.info("Calling synthesis agent stream. sessionId={}, model={}", context.sessionId(), chatModel))
                .doOnNext(chunk -> {
                    answerBuilder.append(chunk);
                    if (!firstChunkLogged[0]) {
                        firstChunkLogged[0] = true;
                        long firstChunkLatency = Duration.between(requestStart, Instant.now()).toMillis();
                        log.info("First stream chunk received. sessionId={}, latencyMs={}", context.sessionId(), firstChunkLatency);
                    }
                    log.info("Stream chunk received. sessionId={}, chunk={}", context.sessionId(), trim(chunk, 120));
                })
                .doOnComplete(() -> {
                    persistAssistant(context, answerBuilder.toString());
                    long totalLatency = Duration.between(requestStart, Instant.now()).toMillis();
                    log.info("Stream chat finished. sessionId={}, answerLength={}, totalLatencyMs={}", context.sessionId(), answerBuilder.length(), totalLatency);
                })
                .doOnError(ex -> log.warn("Stream chat failed. sessionId={}", context.sessionId(), ex));
    }

    private ChatSessionContext prepareContext(String sessionId, String userMessage) {
        String resolvedSessionId = StringUtils.hasText(sessionId) ? sessionId : UUID.randomUUID().toString();
        long sessionDbId = conversationRepository.findOrCreateSession(resolvedSessionId);
        conversationRepository.saveMessage(sessionDbId, "user", userMessage, null, Map.of());

        MemoryProfileAgentService.ContextSnapshot contextSnapshot = memoryProfileAgentService.loadContext(sessionDbId);
        SearchPlan searchPlan = buildSearchPlan(userMessage);
        EvidenceBundle evidenceBundle = searchPlan.requiresEvidence()
                ? searchAgentService.retrieveEvidence(new SearchAgentRequest(
                        userMessage,
                        null,
                        null,
                        searchPlan.needLocalKnowledge(),
                        searchPlan.needWebSearch(),
                        searchPlan.freshnessRequired(),
                        5
                ))
                : new EvidenceBundle(userMessage, false, false, false, List.of(), List.of());

        log.info("Chat request received. sessionId={}, query={}", resolvedSessionId, trim(userMessage, 80));
        log.info(
                "Search plan created. sessionId={}, needLocalKnowledge={}, needWebSearch={}, freshnessRequired={}, evidenceItemCount={}",
                resolvedSessionId,
                searchPlan.needLocalKnowledge(),
                searchPlan.needWebSearch(),
                searchPlan.freshnessRequired(),
                evidenceBundle.items().size()
        );

        return new ChatSessionContext(
                resolvedSessionId,
                sessionDbId,
                contextSnapshot,
                evidenceBundle
        );
    }

    private void persistAssistant(ChatSessionContext context, String answer) {
        conversationRepository.saveMessage(context.sessionDbId(), "assistant", answer, chatModel, Map.of());
        memoryProfileAgentService.refreshSummaryIfNeeded(context.sessionDbId());
    }

    private SearchPlan buildSearchPlan(String userMessage) {
        String normalized = userMessage == null ? "" : userMessage.trim().toLowerCase();
        boolean isGreeting = normalized.length() <= 6
                && (normalized.contains("你好")
                || normalized.contains("hi")
                || normalized.contains("hello"));
        boolean freshnessRequired = normalized.contains("最新")
                || normalized.contains("今天")
                || normalized.contains("刚更新")
                || normalized.contains("当前活动")
                || normalized.contains("版本")
                || normalized.contains("补丁")
                || normalized.contains("hotfix")
                || normalized.contains("update")
                || normalized.contains("today");
        boolean needLocalKnowledge = !isGreeting;
        boolean needWebSearch = freshnessRequired;
        return new SearchPlan(needLocalKnowledge, needWebSearch, freshnessRequired);
    }

    private String trim(String value, int maxLength) {
        if (value == null || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    private record ChatSessionContext(
            String sessionId,
            long sessionDbId,
            MemoryProfileAgentService.ContextSnapshot contextSnapshot,
            EvidenceBundle evidenceBundle
    ) {
    }

    private record SearchPlan(boolean needLocalKnowledge, boolean needWebSearch, boolean freshnessRequired) {
        private boolean requiresEvidence() {
            return needLocalKnowledge || needWebSearch;
        }
    }

    public record ChatResponseData(String sessionId, String answer, OffsetDateTime createdAt) {
    }
}
