package com.xiaolanhe.infrastructure.messaging;

import com.xiaolanhe.infrastructure.config.MessagingProperties;
import java.time.OffsetDateTime;
import java.util.Map;
import org.apache.rocketmq.spring.core.RocketMQTemplate;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.stereotype.Component;
import org.springframework.util.StringUtils;

@Component
public class AgentEventPublisher {

    private static final Logger log = LoggerFactory.getLogger(AgentEventPublisher.class);

    private final RocketMQTemplate rocketMQTemplate;
    private final MessagingProperties messagingProperties;

    public AgentEventPublisher(ObjectProvider<RocketMQTemplate> rocketMQTemplateProvider,
                               MessagingProperties messagingProperties) {
        this.rocketMQTemplate = rocketMQTemplateProvider.getIfAvailable();
        this.messagingProperties = messagingProperties;
    }

    public void publishChatAudit(String sessionId,
                                 String gameCode,
                                 String regionCode,
                                 boolean fallback,
                                 String userMessage) {
        if (rocketMQTemplate == null) {
            return;
        }
        String topic = messagingProperties.topics().chatAudit();
        if (!StringUtils.hasText(topic)) {
            return;
        }
        Map<String, Object> payload = Map.of(
                "eventType", "CHAT_AUDIT",
                "sessionId", sessionId,
                "gameCode", gameCode,
                "regionCode", regionCode,
                "fallback", fallback,
                "userMessage", userMessage,
                "occurredAt", OffsetDateTime.now().toString()
        );
        try {
            rocketMQTemplate.convertAndSend(topic, payload);
        } catch (Exception ex) {
            log.warn("Failed to publish chat audit event to RocketMQ topic {}", topic, ex);
        }
    }
}