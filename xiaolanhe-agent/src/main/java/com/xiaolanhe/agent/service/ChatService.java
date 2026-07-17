package com.xiaolanhe.agent.service;

import com.xiaolanhe.agent.model.RetrievalPlan;
import com.xiaolanhe.agent.model.RouteType;
import com.xiaolanhe.agent.model.SynthesisRequest;
import com.xiaolanhe.agent.model.SynthesisResult;
import com.xiaolanhe.agent.model.TaskPlan;
import com.xiaolanhe.agent.model.VerificationResult;
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
    private final MainAgentService mainAgentService;
    private final MemoryProfileAgentService memoryProfileAgentService;
    private final SearchAgentService searchAgentService;
    private final SynthesisAgentService synthesisAgentService;
    private final String chatModel;

    public ChatService(ConversationRepository conversationRepository,
                       MainAgentService mainAgentService,
                       MemoryProfileAgentService memoryProfileAgentService,
                       SearchAgentService searchAgentService,
                       SynthesisAgentService synthesisAgentService,
                       @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel) {
        this.conversationRepository = conversationRepository;
        this.mainAgentService = mainAgentService;
        this.memoryProfileAgentService = memoryProfileAgentService;
        this.searchAgentService = searchAgentService;
        this.synthesisAgentService = synthesisAgentService;
        this.chatModel = chatModel;
        log.info("ChatService initialized. model={}", chatModel);
    }

    public ChatResponseData chat(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        try {
            if (isDirectByMainAgent(context.taskPlan())) {
                log.info("Step[main-agent-direct] start. sessionId={}, routeType={}", context.sessionId(), context.taskPlan().routeType());
                String answer = mainAgentService.directReply(context.taskPlan(), userMessage);
                persistAssistant(context, answer);
                log.info("Step[main-agent-direct] finish. sessionId={}, answerLength={}", context.sessionId(), answer.length());
                return new ChatResponseData(context.sessionId(), answer, OffsetDateTime.now());
            }

            log.info("Step[synthesis-agent] start. sessionId={}, routeType={}, model={}", context.sessionId(), context.taskPlan().routeType(), chatModel);
            log.info("Calling synthesis agent. sessionId={}, model={}", context.sessionId(), chatModel);
            SynthesisRequest synthesisRequest = new SynthesisRequest(
                    userMessage,
                    context.taskPlan().routeType().name(),
                    context.taskPlan().responseMode().code(),
                    context.taskPlan().notes(),
                    context.contextSnapshot(),
                    context.evidenceBundle()
            );
            SynthesisResult result = synthesisAgentService.synthesize(synthesisRequest);
            persistAssistant(context, result.content());
            return new ChatResponseData(context.sessionId(), result.content(), OffsetDateTime.now());
        } catch (Exception ex) {
            log.warn("Synthesis agent call failed. sessionId={}", context.sessionId(), ex);
            throw new IllegalStateException("Synthesis agent call failed", ex);
        }
    }

    public Flux<String> stream(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        if (isDirectByMainAgent(context.taskPlan())) {
            StringBuilder answerBuilder = new StringBuilder();
            Instant requestStart = Instant.now();
            final boolean[] firstChunkLogged = {false};

            return mainAgentService.streamDirectReply(context.taskPlan(), userMessage)
                    .doOnSubscribe(subscription -> log.info("Step[main-agent-direct] stream start. sessionId={}, routeType={}", context.sessionId(), context.taskPlan().routeType()))
                    .doOnNext(chunk -> {
                        answerBuilder.append(chunk);
                        if (!firstChunkLogged[0]) {
                            firstChunkLogged[0] = true;
                            long firstChunkLatency = Duration.between(requestStart, Instant.now()).toMillis();
                            log.info("Main-agent direct first stream chunk received. sessionId={}, latencyMs={}", context.sessionId(), firstChunkLatency);
                        }
                        log.info("Main-agent direct stream chunk received. sessionId={}, chunk={}", context.sessionId(), trim(chunk, 120));
                    })
                    .doOnComplete(() -> {
                        persistAssistant(context, answerBuilder.toString());
                        long totalLatency = Duration.between(requestStart, Instant.now()).toMillis();
                        log.info("Step[main-agent-direct] stream finish. sessionId={}, answerLength={}, totalLatencyMs={}", context.sessionId(), answerBuilder.length(), totalLatency);
                    })
                    .doOnError(ex -> log.warn("Main-agent direct stream failed. sessionId={}", context.sessionId(), ex));
        }

        SynthesisRequest synthesisRequest = new SynthesisRequest(
                userMessage,
                context.taskPlan().routeType().name(),
                context.taskPlan().responseMode().code(),
                context.taskPlan().notes(),
                context.contextSnapshot(),
                context.evidenceBundle()
        );
        StringBuilder answerBuilder = new StringBuilder();
        Instant requestStart = Instant.now();
        final boolean[] firstChunkLogged = {false};

        return synthesisAgentService.streamSynthesis(
                        synthesisRequest
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
                    VerificationResult verificationResult = synthesisAgentService.verifyAnswer(synthesisRequest, answerBuilder.toString());
                    log.info(
                            "Synthesis verification finished. sessionId={}, passed={}, revised={}, reason={}",
                            context.sessionId(),
                            verificationResult.passed(),
                            verificationResult.revised(),
                            verificationResult.reason()
                    );
                    persistAssistant(context, answerBuilder.toString());
                    long totalLatency = Duration.between(requestStart, Instant.now()).toMillis();
                    log.info("Stream chat finished. sessionId={}, answerLength={}, totalLatencyMs={}", context.sessionId(), answerBuilder.length(), totalLatency);
                })
                .doOnError(ex -> log.warn("Stream chat failed. sessionId={}", context.sessionId(), ex));
    }

    private ChatSessionContext prepareContext(String sessionId, String userMessage) {
        String resolvedSessionId = StringUtils.hasText(sessionId) ? sessionId : UUID.randomUUID().toString();
        log.info("Step[conversation] start. sessionId={}", resolvedSessionId);
        long sessionDbId = conversationRepository.findOrCreateSession(resolvedSessionId);
        conversationRepository.saveMessage(sessionDbId, "user", userMessage, null, Map.of());
        log.info("Step[conversation] finish. sessionId={}, sessionDbId={}", resolvedSessionId, sessionDbId);

        log.info("Step[memory-profile] start. sessionId={}", resolvedSessionId);
        MemoryProfileAgentService.ContextSnapshot planningContext = memoryProfileAgentService.loadContext(sessionDbId);
        log.info("Step[memory-profile] finish. sessionId={}, summaryLength={}, recentMessageCount={}",
                resolvedSessionId,
                planningContext.sessionSummary().length(),
                planningContext.recentMessages().size());

        log.info("Step[main-agent-plan] start. sessionId={}", resolvedSessionId);
        TaskPlan taskPlan = mainAgentService.plan(userMessage, planningContext.promptContext());
        log.info("Step[main-agent-plan] finish. sessionId={}, routeType={}, taskType={}, intentType={}",
                resolvedSessionId,
                taskPlan.routeType(),
                taskPlan.taskType(),
                taskPlan.intentType());
        MemoryProfileAgentService.ContextSnapshot contextSnapshot = taskPlan.needMemory()
                ? planningContext
                : new MemoryProfileAgentService.ContextSnapshot("", List.of(), "");
        if (!taskPlan.needMemory()) {
            log.info("Step[memory-injection] skipped. sessionId={}, routeType={}", resolvedSessionId, taskPlan.routeType());
        } else {
            log.info("Step[memory-injection] enabled. sessionId={}, routeType={}", resolvedSessionId, taskPlan.routeType());
        }

        EvidenceBundle evidenceBundle = retrieveEvidence(taskPlan);

        log.info("Chat request received. sessionId={}, query={}", resolvedSessionId, trim(userMessage, 80));
        log.info(
                "Task plan created. sessionId={}, routeType={}, taskType={}, intentType={}, responseMode={}, needMemory={}, needSearch={}, evidenceItemCount={}, taskNotes={}",
                resolvedSessionId,
                taskPlan.routeType(),
                taskPlan.taskType(),
                taskPlan.intentType(),
                taskPlan.responseMode(),
                taskPlan.needMemory(),
                taskPlan.needSearch(),
                evidenceBundle.items().size(),
                taskPlan.notes()
        );
        if (taskPlan.retrievalPlan() != null) {
            log.info(
                    "Retrieval plan created. sessionId={}, freshnessRequired={}, needLocalKnowledge={}, needWebSearch={}, subQueryCount={}",
                    resolvedSessionId,
                    taskPlan.retrievalPlan().freshnessRequired(),
                    taskPlan.retrievalPlan().needLocalKnowledge(),
                    taskPlan.retrievalPlan().needWebSearch(),
                    taskPlan.retrievalPlan().subQueries().size()
            );
        }

        return new ChatSessionContext(
                resolvedSessionId,
                sessionDbId,
                taskPlan,
                contextSnapshot,
                evidenceBundle
        );
    }

    private EvidenceBundle retrieveEvidence(TaskPlan taskPlan) {
        RetrievalPlan retrievalPlan = taskPlan.retrievalPlan();
        if (!taskPlan.useEvidenceRoute() || !taskPlan.needSearch() || retrievalPlan == null || !retrievalPlan.requiresEvidence()) {
            log.info("Step[search-agent] skipped. routeType={}, needSearch={}", taskPlan.routeType(), taskPlan.needSearch());
            return new EvidenceBundle("", false, false, false, List.of(), List.of());
        }

        log.info("Step[search-agent] start. routeType={}, queryIntent={}, needLocalKnowledge={}, needWebSearch={}",
                taskPlan.routeType(),
                retrievalPlan.queryIntent(),
                retrievalPlan.needLocalKnowledge(),
                retrievalPlan.needWebSearch());
        EvidenceBundle bundle = searchAgentService.retrieveEvidence(new SearchAgentRequest(
                retrievalPlan.originalQuery(),
                retrievalPlan.normalizedQuery(),
                retrievalPlan.queryIntent(),
                taskPlan.taskType().name(),
                taskPlan.responseMode().code(),
                retrievalPlan.needLocalKnowledge(),
                retrievalPlan.needWebSearch(),
                retrievalPlan.freshnessRequired(),
                retrievalPlan.needLowLevelRetrieval(),
                retrievalPlan.needHighLevelRetrieval(),
                retrievalPlan.querySteps(),
                retrievalPlan.subQueries(),
                taskPlan.notes(),
                retrievalPlan.topK(),
                retrievalPlan.rerankEnabled()
        ));
        log.info("Step[search-agent] finish. routeType={}, evidenceItemCount={}", taskPlan.routeType(), bundle.items().size());
        return bundle;
    }

    private void persistAssistant(ChatSessionContext context, String answer) {
        conversationRepository.saveMessage(context.sessionDbId(), "assistant", answer, chatModel, Map.of());
        memoryProfileAgentService.refreshSummaryIfNeeded(context.sessionDbId());
    }

    private String trim(String value, int maxLength) {
        if (value == null || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    private boolean isDirectByMainAgent(TaskPlan taskPlan) {
        return taskPlan.routeType() == RouteType.DIRECT_CHAT || taskPlan.routeType() == RouteType.CLARIFY;
    }

    private record ChatSessionContext(
            String sessionId,
            long sessionDbId,
            TaskPlan taskPlan,
            MemoryProfileAgentService.ContextSnapshot contextSnapshot,
            EvidenceBundle evidenceBundle
    ) {
    }

    public record ChatResponseData(String sessionId, String answer, OffsetDateTime createdAt) {
    }
}
