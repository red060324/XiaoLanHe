package com.xiaolanhe.agent.service;

import com.xiaolanhe.agent.model.ChatCommand;
import com.xiaolanhe.agent.model.ChatResult;
import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import com.xiaolanhe.infrastructure.config.AgentProperties;
import com.xiaolanhe.infrastructure.messaging.AgentEventPublisher;
import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository;
import com.xiaolanhe.rag.service.KnowledgeDocumentService;
import com.xiaolanhe.search.model.SearchResponse;
import com.xiaolanhe.search.service.WebSearchService;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class ChatService {

    private static final Logger log = LoggerFactory.getLogger(ChatService.class);

    private final ConversationRepository conversationRepository;
    private final AgentProperties agentProperties;
    private final AgentEventPublisher agentEventPublisher;
    private final KnowledgeDocumentService knowledgeDocumentService;
    private final WebSearchService webSearchService;
    private final ChatClient chatClient;
    private final String apiKey;
    private final String chatModel;

    public ChatService(ConversationRepository conversationRepository,
                       AgentProperties agentProperties,
                       AgentEventPublisher agentEventPublisher,
                       KnowledgeDocumentService knowledgeDocumentService,
                       WebSearchService webSearchService,
                       ObjectProvider<ChatClient.Builder> chatClientBuilderProvider,
                       @Value("${spring.ai.openai.api-key:}") String apiKey,
                       @Value("${spring.ai.openai.chat.options.model:gpt-4.1-mini}") String chatModel) {
        this.conversationRepository = conversationRepository;
        this.agentProperties = agentProperties;
        this.agentEventPublisher = agentEventPublisher;
        this.knowledgeDocumentService = knowledgeDocumentService;
        this.webSearchService = webSearchService;
        this.apiKey = apiKey;
        this.chatModel = chatModel;
        ChatClient.Builder builder = chatClientBuilderProvider.getIfAvailable();
        this.chatClient = builder == null ? null : builder.build();
    }

    public ChatResult chat(ChatCommand command) {
        String sessionId = StringUtils.hasText(command.sessionId()) ? command.sessionId() : UUID.randomUUID().toString();
        String gameCode = StringUtils.hasText(command.gameCode()) ? command.gameCode() : agentProperties.defaultGame();
        String regionCode = StringUtils.hasText(command.regionCode()) ? command.regionCode() : agentProperties.defaultRegion();

        long sessionDbId = conversationRepository.findOrCreateSession(sessionId, gameCode, regionCode);
        conversationRepository.saveMessage(sessionDbId, "user", command.message(), null, Map.of(
                "gameCode", gameCode,
                "regionCode", regionCode
        ));

        List<KnowledgeSnippet> knowledgeSnippets = knowledgeDocumentService.search(command.message(), gameCode, regionCode, 3);
        SearchResponse searchResponse = shouldUseWebSearch(command.message()) ? webSearchService.search(command.message()) : null;

        boolean fallback = useFallback();
        String answer = fallback
                ? buildFallbackAnswer(gameCode, regionCode, command.message(), knowledgeSnippets, searchResponse)
                : generateModelAnswer(gameCode, regionCode, command.message(), knowledgeSnippets, searchResponse);
        String modelName = fallback ? "local-fallback" : chatModel;

        conversationRepository.saveMessage(sessionDbId, "assistant", answer, modelName, Map.of(
                "fallback", fallback,
                "agentMode", agentProperties.mode(),
                "knowledgeHits", knowledgeSnippets.size(),
                "searchEnabled", searchResponse != null && searchResponse.enabled()
        ));

        agentEventPublisher.publishChatAudit(sessionId, gameCode, regionCode, fallback, command.message());

        return new ChatResult(sessionId, answer, modelName, fallback, OffsetDateTime.now());
    }

    private boolean useFallback() {
        return chatClient == null || !StringUtils.hasText(apiKey) || "replace-me".equals(apiKey);
    }

    private boolean shouldUseWebSearch(String message) {
        String lower = message.toLowerCase();
        return lower.contains("latest")
                || lower.contains("today")
                || lower.contains("update")
                || lower.contains("patch")
                || lower.contains("news");
    }

    private String buildFallbackAnswer(String gameCode,
                                       String regionCode,
                                       String userMessage,
                                       List<KnowledgeSnippet> knowledgeSnippets,
                                       SearchResponse searchResponse) {
        StringBuilder builder = new StringBuilder();
        builder.append("XiaoLanHe is running in local development mode.")
                .append(" Current game=").append(gameCode)
                .append(", region=").append(regionCode)
                .append(". I have saved your session context and assembled local knowledge before the full agent workflow is online.")
                .append(" Your message was: ").append(userMessage);

        if (!knowledgeSnippets.isEmpty()) {
            builder.append(" Local knowledge hits: ");
            for (int i = 0; i < knowledgeSnippets.size(); i++) {
                KnowledgeSnippet snippet = knowledgeSnippets.get(i);
                if (i > 0) {
                    builder.append(" | ");
                }
                builder.append(snippet.title()).append(" -> ").append(trim(snippet.snippet(), 120));
            }
        }

        if (searchResponse != null) {
            builder.append(" Web search note: ").append(searchResponse.note());
        }
        return builder.toString();
    }

    private String generateModelAnswer(String gameCode,
                                       String regionCode,
                                       String userMessage,
                                       List<KnowledgeSnippet> knowledgeSnippets,
                                       SearchResponse searchResponse) {
        try {
            return chatClient.prompt()
                    .system(system -> system.text("""
                            You are XiaoLanHe, a game assistant for players.
                            Prioritize game, region, and patch context in your answer.
                            Use the supplied local knowledge snippets first.
                            If information is incomplete, give a safe answer first and do not invent version details.
                            """))
                    .user(user -> user.text("""
                            Current game: %s
                            Current region: %s
                            User question: %s

                            Local knowledge:
                            %s

                            Web search status:
                            %s
                            """.formatted(
                            gameCode,
                            regionCode,
                            userMessage,
                            formatKnowledge(knowledgeSnippets),
                            formatSearch(searchResponse)
                    )))
                    .call()
                    .content();
        } catch (Exception ex) {
            log.warn("Chat model call failed, fallback to local response", ex);
            return buildFallbackAnswer(gameCode, regionCode, userMessage, knowledgeSnippets, searchResponse);
        }
    }

    private String formatKnowledge(List<KnowledgeSnippet> snippets) {
        if (snippets.isEmpty()) {
            return "No local knowledge hit.";
        }
        StringBuilder builder = new StringBuilder();
        for (KnowledgeSnippet snippet : snippets) {
            builder.append("- title=").append(snippet.title())
                    .append(", patch=").append(snippet.patchVersion())
                    .append(", snippet=").append(trim(snippet.snippet(), 300))
                    .append('\n');
        }
        return builder.toString();
    }

    private String formatSearch(SearchResponse response) {
        if (response == null) {
            return "Search not triggered for this query.";
        }
        return "enabled=" + response.enabled() + ", provider=" + response.provider() + ", note=" + response.note();
    }

    private String trim(String value, int maxLength) {
        if (value == null || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }
}