package com.xiaolanhe.agent.service;

import com.xiaolanhe.infrastructure.config.AgentProperties;
import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository;
import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository.ConversationMessageRecord;
import com.xiaolanhe.infrastructure.persistence.repository.ConversationRepository.SessionMemoryState;
import java.util.List;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class MemoryProfileAgentService {

    private static final Logger log = LoggerFactory.getLogger(MemoryProfileAgentService.class);

    private final ConversationRepository conversationRepository;
    private final ChatClient memorySummaryChatClient;
    private final AgentProperties agentProperties;

    public MemoryProfileAgentService(ConversationRepository conversationRepository,
                                     @Qualifier("memorySummaryChatClient") ChatClient memorySummaryChatClient,
                                     AgentProperties agentProperties) {
        this.conversationRepository = conversationRepository;
        this.memorySummaryChatClient = memorySummaryChatClient;
        this.agentProperties = agentProperties;
    }

    public ContextSnapshot loadContext(long sessionId) {
        AgentProperties.Memory memory = agentProperties.memory();
        if (memory == null || !memory.enabled()) {
            return new ContextSnapshot("", List.of(), "");
        }

        SessionMemoryState sessionMemoryState = conversationRepository.loadSessionMemoryState(sessionId);
        List<ConversationMessageRecord> recentMessages = conversationRepository.findRecentMessages(
                sessionId,
                Math.max(1, memory.recentWindowSize())
        );

        String promptContext = buildPromptContext(sessionMemoryState.summaryText(), recentMessages);
        log.info(
                "Memory context loaded. sessionDbId={}, summaryLength={}, recentMessageCount={}",
                sessionId,
                sessionMemoryState.summaryText().length(),
                recentMessages.size()
        );
        return new ContextSnapshot(sessionMemoryState.summaryText(), recentMessages, promptContext);
    }

    public void refreshSummaryIfNeeded(long sessionId) {
        AgentProperties.Memory memory = agentProperties.memory();
        if (memory == null || !memory.enabled()) {
            return;
        }

        int totalMessages = conversationRepository.countMessages(sessionId);
        if (totalMessages < Math.max(memory.summaryTriggerMessageCount(), memory.recentWindowSize())) {
            return;
        }

        List<ConversationMessageRecord> summaryMessages = conversationRepository.findMessagesForSummary(
                sessionId,
                Math.max(1, memory.recentWindowSize())
        );
        if (summaryMessages.isEmpty()) {
            return;
        }

        SessionMemoryState existing = conversationRepository.loadSessionMemoryState(sessionId);
        String summaryInput = buildSummaryInput(existing.summaryText(), summaryMessages);
        String summaryText = memorySummaryChatClient.prompt()
                .user(summaryInput)
                .call()
                .content();

        if (!StringUtils.hasText(summaryText)) {
            log.warn("Memory summary returned empty. sessionDbId={}", sessionId);
            return;
        }

        conversationRepository.updateSessionSummary(sessionId, summaryText.trim(), totalMessages - memory.recentWindowSize());
        log.info(
                "Memory summary refreshed. sessionDbId={}, totalMessages={}, retainedRecentMessages={}, summaryLength={}",
                sessionId,
                totalMessages,
                memory.recentWindowSize(),
                summaryText.length()
        );
    }

    private String buildPromptContext(String summaryText, List<ConversationMessageRecord> recentMessages) {
        StringBuilder builder = new StringBuilder();
        if (StringUtils.hasText(summaryText)) {
            builder.append("【会话摘要】\n")
                    .append(summaryText.trim())
                    .append("\n\n");
        }
        if (!recentMessages.isEmpty()) {
            builder.append("【最近对话】\n");
            for (ConversationMessageRecord recentMessage : recentMessages) {
                builder.append(roleLabel(recentMessage.role()))
                        .append("：")
                        .append(recentMessage.content())
                        .append('\n');
            }
        }
        return builder.toString().trim();
    }

    private String buildSummaryInput(String existingSummary, List<ConversationMessageRecord> messages) {
        StringBuilder builder = new StringBuilder();
        if (StringUtils.hasText(existingSummary)) {
            builder.append("已有摘要：\n")
                    .append(existingSummary.trim())
                    .append("\n\n");
        }
        builder.append("请基于以下会话记录生成新的长期记忆摘要：\n");
        for (ConversationMessageRecord message : messages) {
            builder.append(roleLabel(message.role()))
                    .append("：")
                    .append(message.content())
                    .append('\n');
        }
        return builder.toString();
    }

    private String roleLabel(String role) {
        return "assistant".equals(role) ? "助手" : "用户";
    }

    public record ContextSnapshot(
            String sessionSummary,
            List<ConversationMessageRecord> recentMessages,
            String promptContext
    ) {
    }
}
