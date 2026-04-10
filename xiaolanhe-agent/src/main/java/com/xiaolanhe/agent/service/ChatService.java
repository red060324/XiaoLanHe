package com.xiaolanhe.agent.service;

import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

@Service
public class ChatService {

    private static final Logger log = LoggerFactory.getLogger(ChatService.class);

    private final ConversationRepository conversationRepository;
    private final MemoryProfileAgentService memoryProfileAgentService;
    private final ChatClient chatClient;
    private final String chatModel;

    public ChatService(ConversationRepository conversationRepository,
                       MemoryProfileAgentService memoryProfileAgentService,
                       ChatClient chatClient,
                       @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel) {
        this.conversationRepository = conversationRepository;
        this.memoryProfileAgentService = memoryProfileAgentService;
        this.chatClient = chatClient;
        this.chatModel = chatModel;
        log.info("ChatService initialized with Spring AI ChatClient via DashScope compatible mode. model={}", chatModel);
    }

    public ChatResponseData chat(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        try {
            log.info("Calling chat model. sessionId={}, model={}", context.sessionId(), chatModel);
            String answer = chatClient.prompt()
                    .user(context.promptInput())
                    .call()
                    .content();
            persistAssistant(context, answer);
            return new ChatResponseData(context.sessionId(), answer, OffsetDateTime.now());
        } catch (Exception ex) {
            log.warn("Chat model call failed. sessionId={}", context.sessionId(), ex);
            throw new IllegalStateException("Chat model call failed", ex);
        }
    }

    public Flux<String> stream(String sessionId, String userMessage) {
        ChatSessionContext context = prepareContext(sessionId, userMessage);
        StringBuilder answerBuilder = new StringBuilder();
        Instant requestStart = Instant.now();
        final boolean[] firstChunkLogged = {false};

        return chatClient.prompt()
                .user(context.promptInput())
                .stream()
                .content()
                .doOnSubscribe(subscription -> log.info("Calling stream chat model. sessionId={}, model={}", context.sessionId(), chatModel))
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
        log.info("Chat request received. sessionId={}, query={}", resolvedSessionId, trim(userMessage, 80));
        return new ChatSessionContext(
                resolvedSessionId,
                sessionDbId,
                userMessage,
                buildPromptInput(contextSnapshot, userMessage)
        );
    }

    private void persistAssistant(ChatSessionContext context, String answer) {
        conversationRepository.saveMessage(context.sessionDbId(), "assistant", answer, chatModel, Map.of());
        memoryProfileAgentService.refreshSummaryIfNeeded(context.sessionDbId());
    }

    private String buildPromptInput(MemoryProfileAgentService.ContextSnapshot contextSnapshot, String userMessage) {
        if (!StringUtils.hasText(contextSnapshot.promptContext())) {
            return userMessage;
        }
        return """
                以下是当前会话上下文，请仅作为辅助参考，不要机械复述。

                %s

                请直接回答最近一条用户消息。
                """.formatted(contextSnapshot.promptContext()).trim();
    }

    private String trim(String value, int maxLength) {
        if (value == null || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    private record ChatSessionContext(String sessionId, long sessionDbId, String userMessage, String promptInput) {
    }

    public record ChatResponseData(String sessionId, String answer, OffsetDateTime createdAt) {
    }
}
